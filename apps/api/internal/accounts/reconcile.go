package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Delete writes a pending tombstone and erases every Hireloop-owned row in one
// transaction. It is idempotent: repeating it never downgrades a completed
// tombstone and simply re-runs the (side-effect-free) erasure. Clerk deletion
// is a separate, durable step so a provider outage cannot leave data behind
// while reporting success.
func (s *Store) Delete(ctx context.Context, subject string) (DeleteResult, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return DeleteResult{}, errors.New("accounts: subject required")
	}
	if s == nil || s.DB == nil {
		return DeleteResult{}, errors.New("accounts: store unavailable")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeleteResult{}, err
	}
	defer tx.Rollback()

	// Resolve the optional binding before erasing it, and serialize against
	// any owner-scoped write already in flight. The trigger takes the same
	// advisory lock for the same identifiers.
	supabaseID, err := bindingTx(ctx, tx, subject)
	if err != nil {
		return DeleteResult{}, err
	}
	for _, id := range ownersFor(subject, supabaseID) {
		if err := lockOwner(ctx, tx, id); err != nil {
			return DeleteResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_deletions (clerk_subject, supabase_user_id, state)
		VALUES ($1, NULLIF($2, ''), 'pending')
		ON CONFLICT (clerk_subject) DO NOTHING`, subject, supabaseID); err != nil {
		return DeleteResult{}, err
	}
	if err := eraseOwner(ctx, tx, subject, supabaseID); err != nil {
		return DeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeleteResult{}, err
	}

	state, err := s.Status(ctx, subject)
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{State: state, ClerkSubject: subject, SupabaseUserID: supabaseID}, nil
}

// Complete records that Clerk confirmed deletion. It never clears the
// tombstone: the record is retained for the whole backup horizon.
func (s *Store) Complete(ctx context.Context, subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return errors.New("accounts: subject required")
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE account_deletions SET state = 'completed', completed_at = now(), updated_at = now()
		WHERE clerk_subject = $1 AND state = 'pending'`, subject)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var state string
		if err := s.DB.QueryRowContext(ctx,
			`SELECT state FROM account_deletions WHERE clerk_subject = $1`, subject).Scan(&state); err != nil {
			return err
		}
		if state != string(StatusCompleted) {
			return errors.New("accounts: tombstone is not pending")
		}
	}
	return nil
}

// Pending lists tombstoned subjects whose Clerk deletion is still outstanding.
func (s *Store) Pending(ctx context.Context) ([]PendingDeletion, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT clerk_subject, COALESCE(supabase_user_id, ''), created_at
		FROM account_deletions WHERE state = 'pending' ORDER BY created_at, clerk_subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PendingDeletion{}
	for rows.Next() {
		var p PendingDeletion
		if err := rows.Scan(&p.ClerkSubject, &p.SupabaseUserID, &p.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Journal exports the durable deletion journal as opaque JSON. It contains no
// email or account content and is intended to be copied outside the backup.
func (s *Store) Journal(ctx context.Context) ([]JournalEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT clerk_subject, COALESCE(supabase_user_id, ''), state, created_at, updated_at,
		       COALESCE(completed_at::text, '')
		FROM account_deletions ORDER BY created_at, clerk_subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []JournalEntry{}
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ClerkSubject, &e.SupabaseUserID, &e.State, &e.CreatedAt, &e.UpdatedAt, &e.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// JournalEntry is one opaque tombstone record. Times are RFC3339 strings so a
// journal survives a text export/import without losing precision.
type JournalEntry struct {
	ClerkSubject   string `json:"clerk_subject"`
	SupabaseUserID string `json:"supabase_user_id,omitempty"`
	State          Status `json:"state"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CompletedAt    string `json:"completed_at,omitempty"`
}

// MarshalJournal is the file format used by the restore runbook.
func MarshalJournal(entries []JournalEntry) ([]byte, error) { return json.Marshal(entries) }

// UnmarshalJournal reads that file format.
func UnmarshalJournal(data []byte) ([]JournalEntry, error) {
	var entries []JournalEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ImportJournal upserts tombstones from the live source into a restored target.
// It is idempotent and never downgrades a completed record to pending.
func (s *Store) ImportJournal(ctx context.Context, entries []JournalEntry) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	imported := 0
	for _, entry := range entries {
		subject := strings.TrimSpace(entry.ClerkSubject)
		if subject == "" || strings.ContainsAny(subject, "\r\n") {
			return 0, errors.New("accounts: invalid journal subject")
		}
		state := entry.State
		if state != StatusPending && state != StatusCompleted {
			return 0, fmt.Errorf("accounts: invalid journal state %q", state)
		}
		completedAt := any(nil)
		if state == StatusCompleted {
			if parsed, err := time.Parse(time.RFC3339, entry.CompletedAt); err == nil {
				completedAt = parsed
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO account_deletions (clerk_subject, supabase_user_id, state, completed_at)
			VALUES ($1, NULLIF($2, ''), $3, $4)
			ON CONFLICT (clerk_subject) DO UPDATE SET
			    state = CASE WHEN account_deletions.state = 'completed'
			                 THEN 'completed' ELSE EXCLUDED.state END,
			    supabase_user_id = COALESCE(account_deletions.supabase_user_id, EXCLUDED.supabase_user_id),
			    completed_at = COALESCE(account_deletions.completed_at, EXCLUDED.completed_at)`,
			subject, entry.SupabaseUserID, state, completedAt)
		if err != nil {
			return 0, err
		}
		imported++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return imported, nil
}

// Reconcile erases every tombstoned owner from the database. After a restore,
// call ImportJournal first so deletions newer than the backup are present.
// Re-running is safe and is how a partially failed reconciliation completes.
func (s *Store) Reconcile(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT clerk_subject, COALESCE(supabase_user_id, '') FROM account_deletions ORDER BY clerk_subject`)
	if err != nil {
		return 0, err
	}
	type target struct{ subject, supabaseID string }
	targets := []target{}
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.subject, &t.supabaseID); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, t := range targets {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		for _, id := range ownersFor(t.subject, t.supabaseID) {
			if err := lockOwner(ctx, tx, id); err != nil {
				_ = tx.Rollback()
				return 0, err
			}
		}
		if err := eraseOwner(ctx, tx, t.subject, t.supabaseID); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

// eraseOwner removes all ten Hireloop-owned data tables in dependency order.
// Provider event rows are deleted by owned job id and known provider email id
// before the mapping and job rows they point at; payloads cascade with jobs
// and are also removed explicitly so a missing cascade cannot retain PII.
func eraseOwner(ctx context.Context, tx *sql.Tx, subject, supabaseID string) error {
	owners := ownersFor(subject, supabaseID)
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM reminder_email_events
		  WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = ANY($1::text[]))
		     OR provider_email_id IN (SELECT provider_email_id FROM reminder_provider_emails WHERE user_id = ANY($1::text[]))`, []any{owners}},
		{`DELETE FROM reminder_delivery_payloads
		  WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = ANY($1::text[]))`, []any{owners}},
		{`DELETE FROM reminder_provider_emails WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM reminder_jobs WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM application_events WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM tasks WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM applications WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM user_notification_settings WHERE user_id = ANY($1::text[])`, []any{owners}},
		{`DELETE FROM user_identities WHERE clerk_subject = $1`, []any{subject}},
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return fmt.Errorf("accounts: erase: %w", err)
		}
	}
	// auth.users is owned by Supabase and is absent in some disposable setups.
	// Delete the bound legacy row only when it actually exists.
	var hasAuth bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('auth.users') IS NOT NULL`).Scan(&hasAuth); err != nil {
		return err
	}
	if hasAuth && supabaseID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth.users WHERE id::text = $1`, supabaseID); err != nil {
			return fmt.Errorf("accounts: erase auth user: %w", err)
		}
	}
	return nil
}

func lockOwner(ctx context.Context, tx *sql.Tx, id string) error {
	var one int
	return tx.QueryRowContext(ctx,
		`SELECT 1 FROM (SELECT pg_advisory_xact_lock(hashtext($1)::bigint)) AS l`, id).Scan(&one)
}
