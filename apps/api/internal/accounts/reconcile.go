package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
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

// Pending lists an eligible bounded batch of tombstones whose Clerk deletion
// is still outstanding. Recently attempted rows remain cooling down for one
// minute and are skipped across worker replicas.
func (s *Store) Pending(ctx context.Context, limit int) ([]PendingDeletion, error) {
	if limit <= 0 || limit > PendingRetryBatchSize {
		limit = PendingRetryBatchSize
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT clerk_subject, COALESCE(supabase_user_id, ''), created_at
		FROM account_deletions WHERE state = 'pending'
		  AND updated_at <= now() - interval '1 minute'
		ORDER BY updated_at, created_at, clerk_subject LIMIT $1`, limit)
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

// MarkAttempt atomically claims a pending deletion for one minute, moving it
// behind untouched entries and preventing duplicate requests across replicas.
func (s *Store) MarkAttempt(ctx context.Context, subject string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE account_deletions
		SET updated_at = now() WHERE clerk_subject = $1 AND state = 'pending'
		  AND updated_at <= now() - interval '1 minute'`, subject)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Journal exports the durable deletion journal as opaque JSON. It contains no
// email or account content and is intended to be copied outside the backup.
func (s *Store) Journal(ctx context.Context) ([]JournalEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT clerk_subject, COALESCE(supabase_user_id, ''), state, created_at, updated_at,
		       completed_at,
	       COALESCE((SELECT json_agg(provider_email_hash ORDER BY provider_email_hash)
	                 FROM account_deletion_provider_hashes h
	                 WHERE h.clerk_subject = d.clerk_subject), '[]'::json)
		FROM account_deletions d ORDER BY created_at, clerk_subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []JournalEntry{}
	for rows.Next() {
		var e JournalEntry
		var createdAt, updatedAt time.Time
		var completedAt sql.NullTime
		var providerHashes string
		if err := rows.Scan(&e.ClerkSubject, &e.SupabaseUserID, &e.State, &createdAt, &updatedAt, &completedAt,
			&providerHashes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(providerHashes), &e.ProviderEmailHashes); err != nil {
			return nil, err
		}
		e.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		e.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if completedAt.Valid {
			e.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// JournalEntry is one opaque tombstone record. Times are RFC3339 strings so a
// journal survives a text export/import without losing precision.
type JournalEntry struct {
	ClerkSubject        string   `json:"clerk_subject"`
	SupabaseUserID      string   `json:"supabase_user_id,omitempty"`
	ProviderEmailHashes []string `json:"provider_email_hashes,omitempty"`
	State               Status   `json:"state"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
	CompletedAt         string   `json:"completed_at,omitempty"`
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
		if subject == "" || len(subject) > 256 || strings.IndexFunc(subject, unicode.IsControl) >= 0 {
			return 0, errors.New("accounts: invalid journal subject")
		}
		supabaseID := strings.TrimSpace(entry.SupabaseUserID)
		if supabaseID != "" && !validUUID(supabaseID) {
			return 0, errors.New("accounts: invalid journal Supabase owner")
		}
		state := entry.State
		if state != StatusPending && state != StatusCompleted {
			return 0, fmt.Errorf("accounts: invalid journal state %q", state)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, entry.CreatedAt)
		if err != nil {
			return 0, errors.New("accounts: invalid journal creation time")
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
		if err != nil {
			return 0, errors.New("accounts: invalid journal update time")
		}
		completedAt := any(nil)
		if state == StatusCompleted {
			parsed, err := time.Parse(time.RFC3339Nano, entry.CompletedAt)
			if err != nil {
				return 0, errors.New("accounts: completed journal entry has invalid completion time")
			}
			completedAt = parsed
		} else if entry.CompletedAt != "" {
			return 0, errors.New("accounts: pending journal entry has a completion time")
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO account_deletions (clerk_subject, supabase_user_id, state, created_at, updated_at, completed_at)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6)
			ON CONFLICT (clerk_subject) DO UPDATE SET
			    state = CASE WHEN account_deletions.state = 'completed'
			                 THEN 'completed' ELSE EXCLUDED.state END,
			    supabase_user_id = COALESCE(account_deletions.supabase_user_id, EXCLUDED.supabase_user_id),
			    created_at = LEAST(account_deletions.created_at, EXCLUDED.created_at),
			    updated_at = GREATEST(account_deletions.updated_at, EXCLUDED.updated_at),
			    completed_at = COALESCE(account_deletions.completed_at, EXCLUDED.completed_at)
			WHERE account_deletions.supabase_user_id IS NULL
			   OR EXCLUDED.supabase_user_id IS NULL
			   OR account_deletions.supabase_user_id = EXCLUDED.supabase_user_id`,
			subject, supabaseID, state, createdAt, updatedAt, completedAt)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected != 1 {
			return 0, errors.New("accounts: journal conflicts with an existing Supabase binding")
		}
		for _, hash := range entry.ProviderEmailHashes {
			if !validProviderEmailHash(hash) {
				return 0, errors.New("accounts: invalid journal provider email hash")
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO account_deletion_provider_hashes (provider_email_hash, clerk_subject)
				VALUES ($1, $2) ON CONFLICT (provider_email_hash) DO UPDATE
				SET clerk_subject = account_deletion_provider_hashes.clerk_subject
				WHERE account_deletion_provider_hashes.clerk_subject = EXCLUDED.clerk_subject`, hash, subject)
			if err != nil {
				return 0, err
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				return 0, errors.New("accounts: provider email hash belongs to another deletion")
			}
		}
		imported++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return imported, nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

func validProviderEmailHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
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
	if err := preserveDeletedProviderEmails(ctx, tx, subject, owners); err != nil {
		return err
	}
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

// preserveDeletedProviderEmails keeps only a SHA-256 fingerprint of each
// known provider id. The advisory lock serializes callback insertion with
// deletion; both paths take the owner lock before this provider-id lock.
func preserveDeletedProviderEmails(ctx context.Context, tx *sql.Tx, subject string, owners []string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT provider_email_id FROM reminder_provider_emails WHERE user_id = ANY($1::text[])
		UNION
		SELECT provider_email_id FROM reminder_jobs WHERE user_id = ANY($1::text[]) AND provider_email_id IS NOT NULL
		ORDER BY provider_email_id`, owners)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := lockProviderEmail(ctx, tx, id); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(id))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_deletion_provider_hashes (provider_email_hash, clerk_subject)
			VALUES ($1, $2) ON CONFLICT (provider_email_hash) DO NOTHING`, hex.EncodeToString(hash[:]), subject); err != nil {
			return err
		}
	}
	return nil
}

func lockProviderEmail(ctx context.Context, tx *sql.Tx, id string) error {
	var one int
	return tx.QueryRowContext(ctx,
		`SELECT 1 FROM (SELECT pg_advisory_xact_lock(hashtext('provider:' || $1)::bigint)) AS l`, id).Scan(&one)
}

func lockOwner(ctx context.Context, tx *sql.Tx, id string) error {
	var one int
	return tx.QueryRowContext(ctx,
		`SELECT 1 FROM (SELECT pg_advisory_xact_lock(hashtext($1)::bigint)) AS l`, id).Scan(&one)
}
