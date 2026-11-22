// Package accounts owns the cross-domain account lifecycle: a complete,
// owner-scoped export, idempotent erasure of every Hireloop-held record, and
// restore reconciliation. No other module duplicates the deletion SQL.
//
// The owner always comes from a verified Clerk subject. An explicitly bound
// Supabase UUID is resolved from user_identities inside the same transaction,
// so both ownership key variants are erased without trusting client input.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/tasks"
)

// ExportSchemaVersion is incremented when the export shape changes. Consumers
// can use it to decide how to read an older archive.
const ExportSchemaVersion = 1

// ErrAccountDeleted means a tombstone exists for the subject. Protected routes
// and exports fail with it rather than serving or recreating deleted data.
var ErrAccountDeleted = errors.New("accounts: account is deleted")

// Status is the durable state of a deletion tombstone.
type Status string

const (
	StatusActive    Status = "active"
	StatusPending   Status = "pending"
	StatusCompleted Status = "completed"
)

// ExportIdentity carries only the verified identifiers, never session tokens
// or provider internals.
type ExportIdentity struct {
	ClerkSubject   string `json:"clerk_subject"`
	PrimaryEmail   string `json:"primary_email"`
	SupabaseUserID string `json:"supabase_user_id,omitempty"`
}

// NotificationSetting is one owner's reminder preference.
type NotificationSetting struct {
	UserID           string `json:"user_id"`
	RemindersEnabled bool   `json:"reminders_enabled"`
}

// Export is the complete account archive. Slices are always present, even for
// an empty account, and each array holds every row (never a page).
type Export struct {
	SchemaVersion       int                          `json:"schema_version"`
	ExportedAt          string                       `json:"exported_at"`
	Identity            ExportIdentity               `json:"identity"`
	Applications        []ExportApplication          `json:"applications"`
	Tasks               []tasks.Task                 `json:"tasks"`
	NotificationSettings []NotificationSetting       `json:"notification_settings"`
}

// ExportApplication embeds the application's fields and adds its full stage
// history, in the same order the detail view uses.
type ExportApplication struct {
	applications.Application
	Events []applications.Event `json:"events"`
}

// DeleteResult is the non-sensitive status returned by a deletion request.
type DeleteResult struct {
	State          Status `json:"state"`
	ClerkSubject   string `json:"clerk_subject"`
	SupabaseUserID string `json:"supabase_user_id,omitempty"`
}

// PendingDeletion is one tombstone the worker still has to finish at Clerk.
type PendingDeletion struct {
	ClerkSubject   string
	SupabaseUserID string
	CreatedAt      time.Time
}

// Store is the database-backed lifecycle owner.
type Store struct {
	DB *sql.DB
	// VerifiedEmail resolves the subject's verified primary email from Clerk's
	// Backend API at export time. Session JWT email claims are never used.
	// A nil resolver produces an empty email rather than inventing one.
	VerifiedEmail func(context.Context, string) (string, error)
}

// Status reports the tombstone state for subject. A missing tombstone is
// StatusActive. Any database problem is returned so callers fail closed.
func (s *Store) Status(ctx context.Context, subject string) (Status, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", errors.New("accounts: subject required")
	}
	if s == nil || s.DB == nil {
		return "", errors.New("accounts: store unavailable")
	}
	var state string
	err := s.DB.QueryRowContext(ctx,
		`SELECT state FROM account_deletions WHERE clerk_subject = $1`, subject).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusActive, nil
	}
	if err != nil {
		return "", err
	}
	return Status(state), nil
}

// Export reads one consistent REPEATABLE READ snapshot and returns every
// owner-scoped row. It deliberately does not reuse paginated list endpoints.
func (s *Store) Export(ctx context.Context, subject string) (Export, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return Export{}, errors.New("accounts: subject required")
	}
	if s == nil || s.DB == nil {
		return Export{}, errors.New("accounts: store unavailable")
	}

	email := ""
	if s.VerifiedEmail != nil {
		verified, err := s.VerifiedEmail(ctx, subject)
		if err != nil {
			return Export{}, fmt.Errorf("accounts: verified email: %w", err)
		}
		email = verified
	}

	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Export{}, err
	}
	defer tx.Rollback()

	if state, err := statusTx(ctx, tx, subject); err != nil {
		return Export{}, err
	} else if state != StatusActive {
		return Export{}, ErrAccountDeleted
	}
	supabaseID, err := bindingTx(ctx, tx, subject)
	if err != nil {
		return Export{}, err
	}
	owners := ownersFor(subject, supabaseID)

	var exportedAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT now()`).Scan(&exportedAt); err != nil {
		return Export{}, err
	}
	export := Export{
		SchemaVersion:        ExportSchemaVersion,
		ExportedAt:           exportedAt.UTC().Format(time.RFC3339),
		Identity:             ExportIdentity{ClerkSubject: subject, PrimaryEmail: email, SupabaseUserID: supabaseID},
		Applications:         []ExportApplication{},
		Tasks:                []tasks.Task{},
		NotificationSettings: []NotificationSetting{},
	}

	apps, err := exportApplications(ctx, tx, owners)
	if err != nil {
		return Export{}, err
	}
	events, err := exportEvents(ctx, tx, owners)
	if err != nil {
		return Export{}, err
	}
	byApp := make(map[string][]applications.Event)
	for _, event := range events {
		byApp[event.ApplicationID] = append(byApp[event.ApplicationID], event)
	}
	for _, app := range apps {
		appEvents := byApp[app.ID]
		if appEvents == nil {
			appEvents = []applications.Event{}
		}
		export.Applications = append(export.Applications, ExportApplication{Application: app, Events: appEvents})
	}

	export.Tasks, err = exportTasks(ctx, tx, owners)
	if err != nil {
		return Export{}, err
	}
	export.NotificationSettings, err = exportSettings(ctx, tx, owners)
	if err != nil {
		return Export{}, err
	}

	if err := tx.Commit(); err != nil {
		return Export{}, err
	}
	return export, nil
}

func statusTx(ctx context.Context, tx *sql.Tx, subject string) (Status, error) {
	var state string
	err := tx.QueryRowContext(ctx,
		`SELECT state FROM account_deletions WHERE clerk_subject = $1`, subject).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusActive, nil
	}
	if err != nil {
		return "", err
	}
	return Status(state), nil
}

func bindingTx(ctx context.Context, tx *sql.Tx, subject string) (string, error) {
	var id sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT supabase_user_id FROM user_identities WHERE clerk_subject = $1`, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !id.Valid {
		return "", nil
	}
	return id.String, nil
}

func ownersFor(subject, supabaseID string) []string {
	owners := []string{subject}
	if supabaseID != "" && supabaseID != subject {
		owners = append(owners, supabaseID)
	}
	return owners
}

// exportApplications scans the same owner-scoped projection the API uses.
func exportApplications(ctx context.Context, tx *sql.Tx, owners []string) ([]applications.Application, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, company, role, status,
		COALESCE(source, ''), COALESCE(address, ''), COALESCE(url, ''),
		COALESCE(channel, ''), COALESCE(job_id, ''), COALESCE(applied_on::text, ''), COALESCE(notes, '')
		FROM applications WHERE user_id = ANY($1::text[]) ORDER BY created_at DESC, id DESC`, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []applications.Application{}
	for rows.Next() {
		var a applications.Application
		if err := rows.Scan(&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address,
			&a.URL, &a.Channel, &a.JobID, &a.AppliedOn, &a.Notes); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func exportEvents(ctx context.Context, tx *sql.Tx, owners []string) ([]applications.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, application_id, event_type,
		COALESCE(from_status, ''), COALESCE(to_status, ''), COALESCE(note, ''), occurred_at
		FROM application_events WHERE user_id = ANY($1::text[])
		ORDER BY application_id, occurred_at DESC, id DESC`, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []applications.Event{}
	for rows.Next() {
		var e applications.Event
		var occurredAt time.Time
		if err := rows.Scan(&e.ID, &e.ApplicationID, &e.EventType, &e.FromStatus,
			&e.ToStatus, &e.Note, &occurredAt); err != nil {
			return nil, err
		}
		e.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		result = append(result, e)
	}
	return result, rows.Err()
}

func exportTasks(ctx context.Context, tx *sql.Tx, owners []string) ([]tasks.Task, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, application_id, title, status, due_precision,
		COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), due_at, COALESCE(time_zone, ''),
		COALESCE(reminder_zone, ''), created_at
		FROM tasks WHERE user_id = ANY($1::text[])
		ORDER BY created_at DESC, id DESC`, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []tasks.Task{}
	for rows.Next() {
		var t tasks.Task
		var date sql.NullString
		var at, created sql.NullTime
		var zone, reminderZone sql.NullString
		if err := rows.Scan(&t.ID, &t.ApplicationID, &t.Title, &t.Status, &t.DuePrecision,
			&date, &at, &zone, &reminderZone, &created); err != nil {
			return nil, err
		}
		t.DueDate, t.TimeZone, t.ReminderZone = date.String, zone.String, reminderZone.String
		if created.Valid {
			t.CreatedAt = created.Time.UTC().Format(time.RFC3339)
		}
		if t.DuePrecision == "instant" && at.Valid {
			t.DueAt = at.Time.UTC().Format(time.RFC3339Nano)
			// DueLocal preserves the original wall-clock presentation from the
			// stored instant and zone; recomputing it keeps the export faithful.
			t.DueLocal = renderLocal(at.Time, zone.String)
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func renderLocal(instant time.Time, zone string) string {
	if zone == "" {
		return ""
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return ""
	}
	return instant.In(loc).Format("2006-01-02T15:04")
}

func exportSettings(ctx context.Context, tx *sql.Tx, owners []string) ([]NotificationSetting, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT user_id, reminders_enabled FROM user_notification_settings WHERE user_id = ANY($1::text[]) ORDER BY user_id`, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NotificationSetting{}
	for rows.Next() {
		var s NotificationSetting
		if err := rows.Scan(&s.UserID, &s.RemindersEnabled); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
