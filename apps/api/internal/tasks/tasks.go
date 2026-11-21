package tasks

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"hireloop/api/internal/applications"

	// Embed the IANA database so an instant's original zone can be validated and
	// rendered on hosts (including Windows) with no system tzdata installed.
	_ "time/tzdata"
)

// ErrNotFound means the task does not exist for this owner. The HTTP layer
// turns it into a 404 without revealing whether another owner holds the id.
var ErrNotFound = errors.New("task not found")

// ErrInvalid indicates a task whose merged fields fail validation.
var ErrInvalid = errors.New("invalid task")

const (
	maxTitleLength    = 200
	maxTimeZoneLength = 64
)

// Status values are the manual workflow's lifecycle: an action starts open and
// is finished by being done or cancelled. Snoozing belongs to a later phase.
const (
	StatusOpen      = "open"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

const (
	PrecisionNone    = "none"
	PrecisionDate    = "date"
	PrecisionInstant = "instant"
)

// Input is the create payload. The deadline is a closed shape:
//   - PrecisionNone:    all three deadline fields empty
//   - PrecisionDate:    DueDate = YYYY-MM-DD, DueLocal and TimeZone empty
//   - PrecisionInstant: DueLocal = YYYY-MM-DDTHH:MM in TimeZone (IANA name),
//     DueDate empty. The API resolves the wall time to a UTC instant.
//
// Empty precision means "no deadline"; no value is ever inferred for the user.
type Input struct {
	ApplicationID string `json:"application_id"`
	Title         string `json:"title"`
	DuePrecision  string `json:"due_precision"`
	DueDate       string `json:"due_date"`
	DueLocal      string `json:"due_local"`
	TimeZone      string `json:"time_zone"`
}

// PatchInput has pointer fields so a missing key means "unchanged". Changing
// any deadline field replaces the whole deadline shape; the store re-validates
// the merged record before writing it.
type PatchInput struct {
	Title        *string `json:"title"`
	Status       *string `json:"status"`
	DuePrecision *string `json:"due_precision"`
	DueDate      *string `json:"due_date"`
	DueLocal     *string `json:"due_local"`
	TimeZone     *string `json:"time_zone"`
}

// Task is the wire record. DueLocal is derived from DueAt in the original
// zone on read, so editing pre-fills the same wall time the user chose.
type Task struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	DuePrecision  string `json:"due_precision"`
	DueDate       string `json:"due_date"`
	DueLocal      string `json:"due_local"`
	DueAt         string `json:"due_at"`
	TimeZone      string `json:"time_zone"`
	CreatedAt     string `json:"created_at"`
}

// Filter selects which lifecycle states a list returns. Only "all" widens the
// default open-actions view.
type Filter struct {
	ShowAll bool
}

type Store interface {
	Create(context.Context, string, Input) (Task, error)
	List(context.Context, string, Filter) ([]Task, error)
	Get(context.Context, string, string) (Task, error)
	Patch(context.Context, string, string, PatchInput) (Task, error)
}

// resolvedDeadline is the validated, database-ready form of Input's deadline.
type resolvedDeadline struct {
	precision string
	date      any // nil or YYYY-MM-DD string
	instant   any // nil or time.Time in UTC
	zone      any // nil or IANA name
}

var ianaZone = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)+$`)

func (i *Input) Valid() bool {
	_, _, ok := i.resolve()
	return ok
}

// resolve validates and normalizes the whole input, returning the fields the
// store binds. It trims the input in place so callers see the same values that
// were validated.
func (i *Input) resolve() (string, resolvedDeadline, bool) {
	i.ApplicationID = strings.TrimSpace(i.ApplicationID)
	i.Title = strings.TrimSpace(i.Title)
	i.DuePrecision = strings.TrimSpace(i.DuePrecision)
	i.DueDate = strings.TrimSpace(i.DueDate)
	i.DueLocal = strings.TrimSpace(i.DueLocal)
	i.TimeZone = strings.TrimSpace(i.TimeZone)

	if !applications.ValidID(i.ApplicationID) || !validTitle(i.Title) {
		return "", resolvedDeadline{}, false
	}
	if i.DuePrecision == "" {
		i.DuePrecision = PrecisionNone
	}
	deadline, ok := resolveDeadline(i.DuePrecision, i.DueDate, i.DueLocal, i.TimeZone)
	return i.DuePrecision, deadline, ok
}

func validTitle(value string) bool {
	return value != "" && len(value) <= maxTitleLength && !strings.ContainsRune(value, 0)
}

func resolveDeadline(precision, date, local, zone string) (resolvedDeadline, bool) {
	switch precision {
	case PrecisionNone:
		if date != "" || local != "" || zone != "" {
			return resolvedDeadline{}, false
		}
		return resolvedDeadline{precision: precision}, true

	case PrecisionDate:
		if date == "" || local != "" || zone != "" || !validDate(date) {
			return resolvedDeadline{}, false
		}
		return resolvedDeadline{precision: precision, date: date}, true

	case PrecisionInstant:
		if date != "" || local == "" || zone == "" || !validLocal(local) || !validZone(zone) {
			return resolvedDeadline{}, false
		}
		// Parse the wall time first for calendar/time range checks, then resolve
		// it in the chosen zone. time.Date handles DST gaps and overlaps
		// deterministically instead of guessing an offset in the browser.
		wall, err := time.Parse("2006-01-02T15:04", local)
		if err != nil {
			return resolvedDeadline{}, false
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return resolvedDeadline{}, false
		}
		instant := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc).UTC()
		// A wall time inside a spring-forward gap does not exist. Go normalizes
		// it to a real instant, but silently showing a different clock time
		// would be misleading, so require the user to enter a real local time.
		if _, derived := renderInstant(instant, zone); derived != local {
			return resolvedDeadline{}, false
		}
		return resolvedDeadline{precision: precision, instant: instant, zone: zone}, true

	default:
		return resolvedDeadline{}, false
	}
}

func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validLocal(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04", value)
	return err == nil && parsed.Format("2006-01-02T15:04") == value
}

func validZone(value string) bool {
	if len(value) > maxTimeZoneLength || strings.ContainsRune(value, 0) {
		return false
	}
	// "Local" is server-local and "UTC" is the one single-part name we accept;
	// every other zone must be a full IANA path such as Asia/Kolkata.
	if value == "UTC" {
		return true
	}
	if value == "Local" || !ianaZone.MatchString(value) {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

func validStatus(value string) bool {
	return value == StatusOpen || value == StatusDone || value == StatusCancelled
}

// Valid rejects an empty patch and any individually invalid field before the
// store reads the row. The complete deadline shape is re-validated after the
// patch is merged with the stored task.
func (i *PatchInput) Valid() bool {
	if i.Title == nil && i.Status == nil && i.DuePrecision == nil && i.DueDate == nil && i.DueLocal == nil && i.TimeZone == nil {
		return false
	}
	trim := func(value *string) string {
		*value = strings.TrimSpace(*value)
		return *value
	}
	if i.Title != nil && !validTitle(trim(i.Title)) {
		return false
	}
	if i.Status != nil && !validStatus(trim(i.Status)) {
		return false
	}
	if i.DuePrecision != nil {
		precision := trim(i.DuePrecision)
		if precision != PrecisionNone && precision != PrecisionDate && precision != PrecisionInstant {
			return false
		}
	}
	for _, field := range []*string{i.DueDate, i.DueLocal, i.TimeZone} {
		if field != nil && strings.ContainsRune(trim(field), 0) {
			return false
		}
	}
	return true
}

// taskRow is the nullable database shape. taskColumns reads it without
// inventing values: absent optional columns decode to zero values and render
// as empty strings.
type taskRow struct {
	ID            string
	ApplicationID string
	Title         string
	Status        string
	Precision     string
	Date          string
	At            sql.NullTime
	Zone          string
	CreatedAt     time.Time
}

const taskColumns = `id, application_id, title, status, due_precision,
	COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), due_at, COALESCE(time_zone, ''), created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var raw taskRow
	err := row.Scan(&raw.ID, &raw.ApplicationID, &raw.Title, &raw.Status, &raw.Precision,
		&raw.Date, &raw.At, &raw.Zone, &raw.CreatedAt)
	if err != nil {
		return Task{}, err
	}
	result := Task{
		ID:            raw.ID,
		ApplicationID: raw.ApplicationID,
		Title:         raw.Title,
		Status:        raw.Status,
		DuePrecision:  raw.Precision,
		DueDate:       raw.Date,
		TimeZone:      raw.Zone,
		CreatedAt:     raw.CreatedAt.UTC().Format(time.RFC3339),
	}
	if raw.Precision == PrecisionInstant && raw.At.Valid {
		result.DueAt, result.DueLocal = renderInstant(raw.At.Time, raw.Zone)
	}
	return result, nil
}

// renderInstant keeps the UTC instant and the original zone together. The
// local wall time is derived in that zone, so an instant entered in Asia/Kolkata
// still reads as the same clock time there, including across DST changes.
func renderInstant(instant time.Time, zone string) (string, string) {
	utc := instant.UTC()
	derived := ""
	if loc, err := time.LoadLocation(zone); err == nil {
		derived = utc.In(loc).Format("2006-01-02T15:04")
	}
	return utc.Format(time.RFC3339), derived
}

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, userID string, in Input) (Task, error) {
	precision, deadline, ok := in.resolve()
	if !ok {
		return Task{}, ErrInvalid
	}
	// The INSERT selects only when the linked application belongs to this
	// owner, so a guessed application id cannot attach a task to another
	// account's record. No row means 404, never a database error.
	row := s.DB.QueryRowContext(ctx, `INSERT INTO tasks
		(user_id, application_id, title, status, due_precision, due_date, due_at, time_zone)
		SELECT $1, $2, $3, '`+StatusOpen+`', $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM applications WHERE id = $2 AND user_id = $1)
		RETURNING `+taskColumns,
		userID, in.ApplicationID, in.Title, precision, deadline.date, deadline.instant, deadline.zone)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

func (s PostgresStore) List(ctx context.Context, userID string, filter Filter) ([]Task, error) {
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE user_id = $1`
	if !filter.ShowAll {
		query += ` AND status = '` + StatusOpen + `'`
	}
	query += ` ORDER BY COALESCE(due_date, (due_at AT TIME ZONE 'UTC')::date) ASC NULLS LAST,
		due_at ASC NULLS FIRST, created_at DESC, id DESC`
	rows, err := s.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s PostgresStore) Get(ctx context.Context, userID, id string) (Task, error) {
	task, err := scanTask(s.DB.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE user_id = $1 AND id = $2`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// Patch re-validates the merged record and writes all deadline columns at once.
// The row is locked for the duration so a concurrent edit cannot be lost.
func (s PostgresStore) Patch(ctx context.Context, userID, id string, input PatchInput) (Task, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()

	current, err := scanTask(tx.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}

	target := current.DuePrecision
	if input.DuePrecision != nil {
		target = strings.TrimSpace(*input.DuePrecision)
	}
	if target == "" {
		target = PrecisionNone
	}
	// Only carry forward fields that belong to the target precision. Switching
	// date -> instant (or clearing a deadline) must not drag the old fields
	// along, or the database CHECK would reject the otherwise valid edit.
	merged := Input{ApplicationID: current.ApplicationID, DuePrecision: target}
	switch target {
	case PrecisionNone:
		if input.DueDate != nil || input.DueLocal != nil || input.TimeZone != nil {
			return Task{}, ErrInvalid
		}
	case PrecisionDate:
		if input.DueLocal != nil || input.TimeZone != nil {
			return Task{}, ErrInvalid
		}
		merged.DueDate = current.DueDate
		if input.DueDate != nil {
			merged.DueDate = *input.DueDate
		}
	case PrecisionInstant:
		if input.DueDate != nil {
			return Task{}, ErrInvalid
		}
		merged.DueLocal, merged.TimeZone = current.DueLocal, current.TimeZone
		if input.DueLocal != nil {
			merged.DueLocal = *input.DueLocal
		}
		if input.TimeZone != nil {
			merged.TimeZone = *input.TimeZone
		}
	default:
		return Task{}, ErrInvalid
	}
	merged.Title = current.Title
	if input.Title != nil {
		merged.Title = *input.Title
	}
	status := current.Status
	if input.Status != nil {
		status = *input.Status
	}
	precision, deadline, ok := merged.resolve()
	if !ok || !validStatus(strings.TrimSpace(status)) {
		return Task{}, ErrInvalid
	}

	updated, err := scanTask(tx.QueryRowContext(ctx, `UPDATE tasks SET
		title = $3, status = $4, due_precision = $5, due_date = $6, due_at = $7, time_zone = $8
		WHERE user_id = $1 AND id = $2 RETURNING `+taskColumns,
		userID, id, merged.Title, strings.TrimSpace(status), precision,
		deadline.date, deadline.instant, deadline.zone))
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return updated, nil
}

// ValidID is shared with the application API so a task id gets the same strict
// 36-character UUID shape check before reaching the store.
func ValidID(id string) bool { return applications.ValidID(id) }
