package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/reminders"

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

// List bounds. A task list is always a page: the UI must never receive an
// unbounded feed and then quietly stop showing rows past a client-side cap.
const (
	DefaultPageSize = 25
	MaxPageSize     = 50

	QueryFilter     = "filter"
	QueryCursor     = "cursor"
	QueryPageSize   = "page_size"
	maxCursorLength = 1024

	FilterOpen = "open"
	FilterAll  = "all"

	// Keyset sentinel values. Postgres orders DATE 'infinity' after every real
	// date and TIMESTAMPTZ '-infinity' before every real instant, matching the
	// SQL view's NULLS LAST / NULLS FIRST ordering without a nullable tuple.
	noDeadlineDay = "infinity"
	noInstantAt   = "-infinity"
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
//   - PrecisionNone:    deadline fields empty
//   - PrecisionDate:    DueDate = YYYY-MM-DD, DueLocal and TimeZone empty.
//     ReminderZone may carry the browser's IANA zone for approximate nudges;
//     absent or invalid means no nudge and no invented cutoff.
//   - PrecisionInstant: DueLocal = YYYY-MM-DDTHH:MM in TimeZone (IANA name),
//     DueDate and ReminderZone empty. The API resolves the wall time to UTC.
//
// Empty precision means "no deadline"; no value is ever inferred for the user.
type Input struct {
	ApplicationID string `json:"application_id"`
	Title         string `json:"title"`
	DuePrecision  string `json:"due_precision"`
	DueDate       string `json:"due_date"`
	DueLocal      string `json:"due_local"`
	TimeZone      string `json:"time_zone"`
	ReminderZone  string `json:"reminder_zone"`
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
	ReminderZone *string `json:"reminder_zone"`
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
	ReminderZone  string `json:"reminder_zone"`
	CreatedAt     string `json:"created_at"`
}

// Filter is a validated, bounded list query. ShowAll widens the default
// open-actions view; PageSize and After describe one keyset page. ParseQuery
// builds it from raw input, so callers never construct a cursor by hand.
type Filter struct {
	ShowAll  bool
	PageSize int
	After    *Cursor
}

// Cursor is a decoded keyset position: the complete sort key of the last row
// on the previous page. All four parts are needed because several tasks can
// share a deadline day, an instant, or a created_at; id makes the order total.
type Cursor struct {
	Day       string // YYYY-MM-DD, or "infinity" for a task with no deadline
	At        string // RFC3339Nano UTC, or "-infinity" when due_at is NULL
	CreatedAt time.Time
	ID        string
}

// ListResult carries one page plus the honest total of matching rows, so a
// bounded page never masquerades as the whole list.
type ListResult struct {
	Items      []Task
	PageSize   int
	Total      int
	NextCursor string
}

type Store interface {
	Create(context.Context, string, Input) (Task, error)
	List(context.Context, string, Filter) (ListResult, error)
	Get(context.Context, string, string) (Task, error)
	Patch(context.Context, string, string, PatchInput) (Task, error)
}

// resolvedDeadline is the validated, database-ready form of Input's deadline.
type resolvedDeadline struct {
	precision    string
	date         any // nil or YYYY-MM-DD string
	instant      any // nil or time.Time in UTC
	zone         any // nil or IANA name for an exact instant
	reminderZone any // nil or IANA name for date-only approximate reminders
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
	i.ReminderZone = strings.TrimSpace(i.ReminderZone)

	if !applications.ValidID(i.ApplicationID) || !validTitle(i.Title) {
		return "", resolvedDeadline{}, false
	}
	if i.DuePrecision == "" {
		i.DuePrecision = PrecisionNone
	}
	deadline, ok := resolveDeadline(i.DuePrecision, i.DueDate, i.DueLocal, i.TimeZone, i.ReminderZone)
	return i.DuePrecision, deadline, ok
}

func validTitle(value string) bool {
	return value != "" && len(value) <= maxTitleLength && !strings.ContainsRune(value, 0)
}

func resolveDeadline(precision, date, local, zone, reminderZone string) (resolvedDeadline, bool) {
	switch precision {
	case PrecisionNone:
		if date != "" || local != "" || zone != "" || reminderZone != "" {
			return resolvedDeadline{}, false
		}
		return resolvedDeadline{precision: precision}, true

	case PrecisionDate:
		// The reminder zone is optional: a browser that cannot supply a
		// usable IANA zone simply omits it, and the task gets no approximate
		// reminder. A supplied zone must be a real IANA name, so garbage is
		// rejected rather than silently used to pick a local hour.
		if date == "" || local != "" || zone != "" || !validDate(date) {
			return resolvedDeadline{}, false
		}
		if reminderZone != "" && !validZone(reminderZone) {
			return resolvedDeadline{}, false
		}
		deadline := resolvedDeadline{precision: precision, date: date}
		if reminderZone != "" {
			deadline.reminderZone = reminderZone
		}
		return deadline, true

	case PrecisionInstant:
		if date != "" || local == "" || zone == "" || reminderZone != "" || !validLocal(local) || !validZone(zone) {
			return resolvedDeadline{}, false
		}
		wall, err := time.Parse(localLayout, local)
		if err != nil {
			return resolvedDeadline{}, false
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return resolvedDeadline{}, false
		}
		instant, ok := resolveWallTime(wall, loc)
		if !ok {
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

const localLayout = "2006-01-02T15:04"

func validLocal(value string) bool {
	parsed, err := time.Parse(localLayout, value)
	return err == nil && parsed.Format(localLayout) == value
}

// resolveWallTime maps a local wall time to exactly one instant in loc.
// Gaps (spring forward) have no instant and overlaps (fall back) have two:
// both are rejected so the API never silently picks an offset the user did
// not choose. Probing a day on either side finds the offsets in force before
// and after a transition, which covers non-hour shifts such as Lord Howe's
// 30-minute change.
func resolveWallTime(wall time.Time, loc *time.Location) (time.Time, bool) {
	candidate := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc)
	candidateUTC := candidate.UTC()
	// A spring-forward gap is normalized by time.Date to a different wall
	// time; showing that instead of what was entered would be misleading.
	if candidate.In(loc).Format(localLayout) != wall.Format(localLayout) {
		return time.Time{}, false
	}
	_, offset := candidate.In(loc).Zone()
	offsets := []int{offset}
	for _, delta := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		_, other := candidate.Add(delta).In(loc).Zone()
		if other != offset {
			offsets = append(offsets, other)
		}
	}
	naive := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC)
	for _, seconds := range offsets {
		alternative := naive.Add(-time.Duration(seconds) * time.Second)
		if !alternative.Equal(candidateUTC) && alternative.In(loc).Format(localLayout) == wall.Format(localLayout) {
			// Two different instants share this wall time: ask the caller to
			// supply an explicit offset or a different local time.
			return time.Time{}, false
		}
	}
	return candidateUTC, true
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

// ParseQuery validates and normalizes raw list parameters. Unknown parameters,
// an explicitly empty value and out-of-range page sizes are rejected instead
// of being ignored or clamped, so a client bug cannot widen a result set.
func ParseQuery(values url.Values) (Filter, error) {
	for key := range values {
		switch key {
		case QueryFilter, QueryCursor, QueryPageSize:
		default:
			return Filter{}, fmt.Errorf("unsupported task query parameter %q", key)
		}
	}
	filter := Filter{}
	if values.Has(QueryFilter) {
		if values.Get(QueryFilter) == "" {
			return Filter{}, fmt.Errorf("%s must not be empty", QueryFilter)
		}
		switch values.Get(QueryFilter) {
		case FilterOpen:
		case FilterAll:
			filter.ShowAll = true
		default:
			return Filter{}, fmt.Errorf("invalid %s", QueryFilter)
		}
	}
	if raw := values.Get(QueryPageSize); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size < 1 || size > MaxPageSize {
			return Filter{}, fmt.Errorf("%s must be between 1 and %d", QueryPageSize, MaxPageSize)
		}
		filter.PageSize = size
	}
	filter.normalize()
	// The cursor is bound to the filter that produced it: applying a token
	// minted for another view would skip or repeat rows without warning.
	if values.Has(QueryCursor) {
		cursor, err := filter.decodeCursor(values.Get(QueryCursor))
		if err != nil {
			return Filter{}, err
		}
		filter.After = &cursor
	}
	return filter, nil
}

// Normalize applies defaults to a Filter built in-process (tests, future
// callers) and reports whether it is usable.
func (f *Filter) Normalize() bool {
	f.normalize()
	return f.PageSize >= 1 && f.PageSize <= MaxPageSize
}

func (f *Filter) normalize() {
	if f.PageSize == 0 {
		f.PageSize = DefaultPageSize
	}
}

// taskCursorToken is the decoded form of the opaque cursor. Version lets a
// future format reject old tokens explicitly instead of misreading them.
type taskCursorToken struct {
	Version   int    `json:"v"`
	Day       string `json:"d"`
	At        string `json:"a"`
	CreatedAt string `json:"c"`
	ID        string `json:"i"`
	Filters   string `json:"f"`
}

const taskCursorVersion = 1

// cursorFilterHash binds a cursor to the view that produced it. Page size is
// deliberately absent: it changes how many rows a page holds, not where the
// sought position is.
func (f Filter) cursorFilterHash() string {
	view := FilterOpen
	if f.ShowAll {
		view = FilterAll
	}
	sum := sha256.Sum256([]byte("filter=" + view))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (f Filter) encodeCursor(day, at string, createdAt time.Time, id string) string {
	payload, err := json.Marshal(taskCursorToken{
		Version:   taskCursorVersion,
		Day:       day,
		At:        at,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		ID:        id,
		Filters:   f.cursorFilterHash(),
	})
	if err != nil {
		// Only strings and an int are marshaled, so this cannot fail.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// EncodeCursor builds a continuation token for a filter and position. Tokens
// are not signed; authorization is enforced by the owner-scoped query.
func EncodeCursor(filter Filter, day, at string, createdAt time.Time, id string) string {
	filter.normalize()
	return filter.encodeCursor(day, at, createdAt, id)
}

// decodeCursor validates a token before it reaches SQL. A well-formed cursor
// stays usable even if the row it points at was deleted: it is a position,
// not a row reference.
func (f Filter) decodeCursor(raw string) (Cursor, error) {
	if raw == "" {
		return Cursor{}, fmt.Errorf("%s must not be empty", QueryCursor)
	}
	if len(raw) > maxCursorLength {
		return Cursor{}, fmt.Errorf("%s is too long", QueryCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	var token taskCursorToken
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if token.Version != taskCursorVersion || token.Filters != f.cursorFilterHash() {
		return Cursor{}, fmt.Errorf("%s does not match these filters", QueryCursor)
	}
	if (token.Day != noDeadlineDay && !validDate(token.Day)) || !ValidID(token.ID) {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if token.At != noInstantAt {
		instant, err := time.Parse(time.RFC3339Nano, token.At)
		if err != nil || instant.UTC().Format(time.RFC3339Nano) != token.At {
			return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
		}
	}
	createdAt, err := time.Parse(time.RFC3339Nano, token.CreatedAt)
	if err != nil {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	return Cursor{Day: token.Day, At: token.At, CreatedAt: createdAt, ID: token.ID}, nil
}

// Valid rejects an empty patch and any individually invalid field before the
// store reads the row. The complete deadline shape is re-validated after the
// patch is merged with the stored task.
func (i *PatchInput) Valid() bool {
	if i.Title == nil && i.Status == nil && i.DuePrecision == nil && i.DueDate == nil && i.DueLocal == nil && i.TimeZone == nil && i.ReminderZone == nil {
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
	for _, field := range []*string{i.DueDate, i.DueLocal, i.TimeZone, i.ReminderZone} {
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
	ReminderZone  string
	CreatedAt     time.Time
}

const taskColumns = `id, application_id, title, status, due_precision,
	COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), due_at, COALESCE(time_zone, ''),
	COALESCE(reminder_zone, ''), created_at`

// taskPageColumns adds the computed deadline day to the read projection. It is
// ordered after taskColumns so the same base columns scan into the same places.
const taskPageColumns = taskColumns + `,
	COALESCE(to_char(due_date, 'YYYY-MM-DD'), to_char((due_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD'), 'infinity')`

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var raw taskRow
	err := row.Scan(&raw.ID, &raw.ApplicationID, &raw.Title, &raw.Status, &raw.Precision,
		&raw.Date, &raw.At, &raw.Zone, &raw.ReminderZone, &raw.CreatedAt)
	if err != nil {
		return Task{}, err
	}
	return taskFromRow(raw), nil
}

func taskFromRow(raw taskRow) Task {
	result := Task{
		ID:            raw.ID,
		ApplicationID: raw.ApplicationID,
		Title:         raw.Title,
		Status:        raw.Status,
		DuePrecision:  raw.Precision,
		DueDate:       raw.Date,
		TimeZone:      raw.Zone,
		ReminderZone:  raw.ReminderZone,
		CreatedAt:     raw.CreatedAt.UTC().Format(time.RFC3339),
	}
	if raw.Precision == PrecisionInstant && raw.At.Valid {
		result.DueAt, result.DueLocal = renderInstant(raw.At.Time, raw.Zone)
	}
	return result
}

// taskPageRow is a task plus the exact keyset position of its row. The sentinels
// keep the tuple non-null, so a row comparison can seek past it deterministically.
type taskPageRow struct {
	Task      Task
	Day       string
	At        string
	CreatedAt time.Time
}

func scanTaskPage(row scanner) (taskPageRow, error) {
	var raw taskRow
	var page taskPageRow
	err := row.Scan(&raw.ID, &raw.ApplicationID, &raw.Title, &raw.Status, &raw.Precision,
		&raw.Date, &raw.At, &raw.Zone, &raw.ReminderZone, &raw.CreatedAt, &page.Day)
	if err != nil {
		return taskPageRow{}, err
	}
	page.Task = taskFromRow(raw)
	page.CreatedAt = raw.CreatedAt
	page.At = noInstantAt
	if raw.At.Valid {
		page.At = raw.At.Time.UTC().Format(time.RFC3339Nano)
	}
	return page, nil
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
	return utc.Format(time.RFC3339Nano), derived
}

// PostgresStore is the SQL store. Jobs schedules reminder jobs for this
// store's tasks; nil means job scheduling is off (older callers).
type PostgresStore struct {
	DB *sql.DB
	// Jobs is the reminders store that owns reminder jobs; nil skips
	// scheduling, suppressing and reschedule handling.
	Jobs *reminders.Store
}

// scheduleIdentity is the part of a task that determines its reminder jobs.
// A patch that leaves it unchanged must not reset an in-flight job; a change
// to any part (including the browser zone of a date-only task) replaces the
// whole set of jobs inside the task's transaction.
type scheduleIdentity struct {
	precision string
	date      string
	zone      string
	at        time.Time
}

func currentSchedule(task Task) scheduleIdentity {
	identity := scheduleIdentity{precision: task.DuePrecision}
	switch task.DuePrecision {
	case PrecisionInstant:
		identity.zone = task.TimeZone
		if at, err := time.Parse(time.RFC3339Nano, task.DueAt); err == nil {
			identity.at = at.UTC()
		}
	case PrecisionDate:
		identity.date, identity.zone = task.DueDate, task.ReminderZone
	}
	return identity
}

func resolvedSchedule(deadline resolvedDeadline) scheduleIdentity {
	identity := scheduleIdentity{precision: deadline.precision}
	switch deadline.precision {
	case PrecisionInstant:
		identity.zone, _ = deadline.zone.(string)
		if at, ok := deadline.instant.(time.Time); ok {
			identity.at = at.UTC()
		}
	case PrecisionDate:
		identity.date, _ = deadline.date.(string)
		identity.zone, _ = deadline.reminderZone.(string)
	}
	return identity
}

func sameSchedule(a, b scheduleIdentity) bool {
	if a.precision != b.precision || a.date != b.date || a.zone != b.zone {
		return false
	}
	return a.at.Equal(b.at)
}

// applySchedule replaces a task's jobs with those its deadline warrants:
// two advance jobs for an exact instant, the two approximate local nudges for
// a date-only deadline with a browser zone, or none when the zone is absent,
// the deadline is cleared or every eligible time is already in the past.
func (s PostgresStore) applySchedule(ctx context.Context, tx *sql.Tx, userID, taskID string, deadline resolvedDeadline) error {
	if s.Jobs == nil {
		return nil
	}
	switch deadline.precision {
	case PrecisionInstant:
		if dueAt, ok := deadline.instant.(time.Time); ok {
			return s.Jobs.Schedule(ctx, tx, userID, taskID, dueAt)
		}
	case PrecisionDate:
		date, _ := deadline.date.(string)
		zone, _ := deadline.reminderZone.(string)
		// An absent browser zone is deliberate safe degradation: no zone is
		// invented and no approximate reminder is scheduled. Old date-only
		// tasks have no stored zone and follow the same path.
		slots := reminders.DateApproxSlots(date, zone, time.Now())
		return s.Jobs.ScheduleApprox(ctx, tx, userID, taskID, slots)
	}
	return s.Jobs.Suppress(ctx, tx, userID, taskID, reminders.SuppressRescheduled)
}

// syncReminderJobs maintains the durable-reminder invariants after a task
// patch, inside the same transaction as the task update: a finished or
// cancelled task suppresses unsent reminders, and any change to the deadline
// (including its precision or the date-only reminder zone) replaces the jobs.
func (s PostgresStore) syncReminderJobs(ctx context.Context, tx *sql.Tx, userID, taskID string, current Task, status string, deadline resolvedDeadline) error {
	if s.Jobs == nil {
		return nil
	}
	if trimmed := strings.TrimSpace(status); trimmed != StatusOpen {
		reason := reminders.SuppressCompleted
		if trimmed == StatusCancelled {
			reason = reminders.SuppressCancelled
		}
		return s.Jobs.Suppress(ctx, tx, userID, taskID, reason)
	}
	if sameSchedule(currentSchedule(current), resolvedSchedule(deadline)) {
		return nil
	}
	return s.applySchedule(ctx, tx, userID, taskID, deadline)
}

func (s PostgresStore) Create(ctx context.Context, userID string, in Input) (Task, error) {
	precision, deadline, ok := in.resolve()
	if !ok {
		return Task{}, ErrInvalid
	}
	insert := func(q reminders.Executor) (Task, error) {
		// The INSERT selects only when the linked application belongs to this
		// owner, so a guessed application id cannot attach a task to another
		// account's record. No row means 404, never a database error.
		row := q.QueryRowContext(ctx, `INSERT INTO tasks
			(user_id, application_id, title, status, due_precision, due_date, due_at, time_zone, reminder_zone)
			SELECT $1, $2, $3, '`+StatusOpen+`', $4, $5, $6, $7, $8
			WHERE EXISTS (SELECT 1 FROM applications WHERE id = $2 AND user_id = $1)
			RETURNING `+taskColumns,
			userID, in.ApplicationID, in.Title, precision, deadline.date, deadline.instant, deadline.zone, deadline.reminderZone)
		task, err := scanTask(row)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return task, err
	}
	if s.Jobs == nil {
		return insert(s.DB)
	}
	// Schedule the reminder inside the same transaction as the task write so
	// the job and the task cannot disagree.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	task, err := insert(tx)
	if err != nil {
		return Task{}, err
	}
	if err := s.applySchedule(ctx, tx, userID, task.ID, deadline); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s PostgresStore) List(ctx context.Context, userID string, filter Filter) (ListResult, error) {
	filter.normalize()
	result := ListResult{PageSize: filter.PageSize, Items: []Task{}}

	// The count and the page share one read-only snapshot so the total always
	// describes the rows this page can return, even under concurrent writes.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ListResult{}, err
	}
	defer tx.Rollback()

	where := `user_id = $1`
	if !filter.ShowAll {
		where += ` AND status = '` + StatusOpen + `'`
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE `+where, userID).Scan(&result.Total); err != nil {
		return ListResult{}, err
	}
	if result.Total == 0 {
		return result, nil
	}

	// Keyset (seek) pagination over the exact four-column sort key. Deadline
	// day and due_at are compared ASC with their sentinels, then created_at and
	// id DESC; one look-ahead row decides whether another page exists.
	pageSQL := `SELECT ` + taskPageColumns + ` FROM tasks WHERE ` + where
	args := []any{userID}
	if filter.After != nil {
		n := len(args) + 1
		args = append(args, filter.After.Day, filter.After.At, filter.After.CreatedAt, filter.After.ID)
		key := `COALESCE(due_date, (due_at AT TIME ZONE 'UTC')::date, 'infinity'::date)`
		at := `COALESCE(due_at, '-infinity'::timestamptz)`
		pageSQL += fmt.Sprintf(` AND ((%s, %s) > ($%d::date, $%d::timestamptz)
			OR ((%s, %s) = ($%d::date, $%d::timestamptz)
				AND (created_at < $%d::timestamptz
					OR (created_at = $%d::timestamptz AND id < $%d::uuid))))`,
			key, at, n, n+1, key, at, n, n+1, n+2, n+2, n+3)
	}
	args = append(args, filter.PageSize+1)
	pageSQL += ` ORDER BY COALESCE(due_date, (due_at AT TIME ZONE 'UTC')::date) ASC NULLS LAST,
		due_at ASC NULLS FIRST, created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := tx.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	page := make([]taskPageRow, 0, filter.PageSize+1)
	for rows.Next() {
		row, err := scanTaskPage(rows)
		if err != nil {
			return ListResult{}, err
		}
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	if len(page) > filter.PageSize {
		// The look-ahead row proves another page exists; the cursor points at
		// the last row actually returned, never at the look-ahead row.
		last := page[filter.PageSize-1]
		result.NextCursor = filter.encodeCursor(last.Day, last.At, last.CreatedAt, last.Task.ID)
		page = page[:filter.PageSize]
	}
	for _, row := range page {
		result.Items = append(result.Items, row.Task)
	}
	return result, nil
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
		if input.DueDate != nil || input.DueLocal != nil || input.TimeZone != nil || input.ReminderZone != nil {
			return Task{}, ErrInvalid
		}
	case PrecisionDate:
		if input.DueLocal != nil || input.TimeZone != nil {
			return Task{}, ErrInvalid
		}
		merged.DueDate, merged.ReminderZone = current.DueDate, current.ReminderZone
		if input.DueDate != nil {
			merged.DueDate = *input.DueDate
		}
		if input.ReminderZone != nil {
			merged.ReminderZone = *input.ReminderZone
		}
	case PrecisionInstant:
		if input.DueDate != nil || input.ReminderZone != nil {
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
	// Preserve an existing instant when its wall time and zone are unchanged.
	// Older records may have been saved during an ambiguous clock transition.
	if !ok && target == PrecisionInstant && current.DuePrecision == PrecisionInstant &&
		strings.TrimSpace(merged.DueLocal) == current.DueLocal && strings.TrimSpace(merged.TimeZone) == current.TimeZone &&
		applications.ValidID(merged.ApplicationID) && validTitle(strings.TrimSpace(merged.Title)) {
		if instant, err := time.Parse(time.RFC3339, current.DueAt); err == nil {
			merged.Title = strings.TrimSpace(merged.Title)
			precision, deadline, ok = PrecisionInstant, resolvedDeadline{precision: PrecisionInstant, instant: instant, zone: current.TimeZone}, true
		}
	}
	if !ok || !validStatus(strings.TrimSpace(status)) {
		return Task{}, ErrInvalid
	}

	updated, err := scanTask(tx.QueryRowContext(ctx, `UPDATE tasks SET
		title = $3, status = $4, due_precision = $5, due_date = $6, due_at = $7, time_zone = $8, reminder_zone = $9
		WHERE user_id = $1 AND id = $2 RETURNING `+taskColumns,
		userID, id, merged.Title, strings.TrimSpace(status), precision,
		deadline.date, deadline.instant, deadline.zone, deadline.reminderZone))
	if err != nil {
		return Task{}, err
	}
	if err := s.syncReminderJobs(ctx, tx, userID, id, current, status, deadline); err != nil {
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
