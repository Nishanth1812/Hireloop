package applications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hireloop/api/internal/reminders"
)

// Field bounds. The database constraints in migration 005 mirror the text
// bounds here so oversized input is rejected by both layers.
const (
	maxRequiredTextLength = 200
	maxOptionalTextLength = 200
	maxURLLength          = 2048
	maxNotesLength        = 2000
	maxStageNoteLength    = 500

	maxSearchLength = 100
	// DefaultPageSize and MaxPageSize bound every list response: the tracker
	// must never be able to ask for (or be handed) an unbounded page.
	DefaultPageSize = 25
	MaxPageSize     = 50

	// QueryKeys are the only accepted list query parameters; anything else is
	// rejected rather than silently ignored. QueryPage is deliberately absent:
	// offset paging is unstable under concurrent writes, so a list is walked
	// with the opaque keyset cursor in QueryCursor.
	QuerySearch   = "q"
	QueryStatus   = "status"
	QueryChannel  = "channel"
	QueryCursor   = "cursor"
	QueryPageSize = "page_size"

	// maxCursorLength bounds the opaque cursor so a caller cannot send an
	// unbounded token. A real cursor is a few hundred bytes at most.
	maxCursorLength = 1024
)

const (
	StatusSaved        = "saved"
	StatusApplied      = "applied"
	StatusInterviewing = "interviewing"
	StatusOffer        = "offer"
	StatusAccepted     = "accepted"
	StatusRejected     = "rejected"
	StatusWithdrawn    = "withdrawn"
)

func validStatus(value string) bool {
	switch value {
	case StatusSaved, StatusApplied, StatusInterviewing, StatusOffer, StatusAccepted, StatusRejected, StatusWithdrawn:
		return true
	default:
		return false
	}
}

type Input struct {
	Company   string `json:"company"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Address   string `json:"address"`
	URL       string `json:"url"`
	Channel   string `json:"channel"`
	JobID     string `json:"job_id"`
	AppliedOn string `json:"applied_on"`
	Notes     string `json:"notes"`
}

type Application struct {
	ID        string `json:"id"`
	Company   string `json:"company"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Address   string `json:"address"`
	URL       string `json:"url"`
	Channel   string `json:"channel"`
	JobID     string `json:"job_id"`
	AppliedOn string `json:"applied_on"`
	Notes     string `json:"notes"`
}

// PatchInput mirrors Input with pointer fields so a missing key means
// "unchanged" while an explicit empty string means "clear this optional
// field".
type PatchInput struct {
	Company   *string `json:"company"`
	Role      *string `json:"role"`
	Status    *string `json:"status"`
	Source    *string `json:"source"`
	Address   *string `json:"address"`
	URL       *string `json:"url"`
	Channel   *string `json:"channel"`
	JobID     *string `json:"job_id"`
	AppliedOn *string `json:"applied_on"`
	Notes     *string `json:"notes"`
	StageNote *string `json:"stage_note"`
}

func (i *Input) Valid() bool {
	i.Company = strings.TrimSpace(i.Company)
	i.Role = strings.TrimSpace(i.Role)
	i.Source = strings.TrimSpace(i.Source)
	i.Address = strings.TrimSpace(i.Address)
	i.URL = strings.TrimSpace(i.URL)
	i.Channel = strings.TrimSpace(i.Channel)
	i.JobID = strings.TrimSpace(i.JobID)
	i.AppliedOn = strings.TrimSpace(i.AppliedOn)
	i.Notes = strings.TrimSpace(i.Notes)
	return validText(i.Company) && validText(i.Role) && validStatus(i.Status) &&
		validOptionalText(i.Source) && validOptionalText(i.Address) &&
		validURL(i.URL) && validChannel(i.Channel) && validOptionalText(i.JobID) &&
		validDate(i.AppliedOn) && validNotes(i.Notes)
}

func (i *PatchInput) Valid() bool {
	if i.Company == nil && i.Role == nil && i.Status == nil && i.Source == nil && i.Address == nil &&
		i.URL == nil && i.Channel == nil && i.JobID == nil && i.AppliedOn == nil && i.Notes == nil {
		return false
	}
	if i.Company != nil {
		*i.Company = strings.TrimSpace(*i.Company)
		if !validText(*i.Company) {
			return false
		}
	}
	if i.Role != nil {
		*i.Role = strings.TrimSpace(*i.Role)
		if !validText(*i.Role) {
			return false
		}
	}
	if i.Source != nil {
		*i.Source = strings.TrimSpace(*i.Source)
		if !validOptionalText(*i.Source) {
			return false
		}
	}
	if i.Address != nil {
		*i.Address = strings.TrimSpace(*i.Address)
		if !validOptionalText(*i.Address) {
			return false
		}
	}
	if i.URL != nil {
		*i.URL = strings.TrimSpace(*i.URL)
		if !validURL(*i.URL) {
			return false
		}
	}
	if i.Channel != nil {
		*i.Channel = strings.TrimSpace(*i.Channel)
		if !validChannel(*i.Channel) {
			return false
		}
	}
	if i.JobID != nil {
		*i.JobID = strings.TrimSpace(*i.JobID)
		if !validOptionalText(*i.JobID) {
			return false
		}
	}
	if i.AppliedOn != nil {
		*i.AppliedOn = strings.TrimSpace(*i.AppliedOn)
		if !validDate(*i.AppliedOn) {
			return false
		}
	}
	if i.Notes != nil {
		*i.Notes = strings.TrimSpace(*i.Notes)
		if !validNotes(*i.Notes) {
			return false
		}
	}
	if i.StageNote != nil {
		*i.StageNote = strings.TrimSpace(*i.StageNote)
		if i.Status == nil || len(*i.StageNote) > maxStageNoteLength || strings.ContainsRune(*i.StageNote, 0) {
			return false
		}
	}
	return i.Status == nil || validStatus(*i.Status)
}

func validText(value string) bool {
	return value != "" && len(value) <= maxRequiredTextLength && !strings.ContainsRune(value, 0)
}

// validOptionalText allows a blank value (the field is optional) while still
// rejecting overlong input or PostgreSQL-incompatible NUL characters. Trimming
// already happened, so an all-whitespace value becomes "" and is accepted.
func validOptionalText(value string) bool {
	return len(value) <= maxOptionalTextLength && !strings.ContainsRune(value, 0)
}

// validURL accepts only absolute http(s) links so a stored URL can always be
// rendered as a safe link. Anything else (javascript:, relative paths, bare
// hosts) is rejected rather than repaired.
func validURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > maxURLLength || strings.ContainsRune(value, 0) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// validChannel accepts only the two agreed campus values; a blank value means
// the candidate did not record it.
func validChannel(value string) bool {
	return value == "" || value == "campus" || value == "off-campus"
}

// validDate accepts a blank value or an ISO date (YYYY-MM-DD). A date is never
// combined with a time: the product plan forbids inventing one.
func validDate(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validNotes(value string) bool {
	return len(value) <= maxNotesLength && !strings.ContainsRune(value, 0)
}

func ValidID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

// Query is a validated, bounded list query. The zero value means "no filters,
// first page, default page size".
type Query struct {
	Search   string
	Status   string
	Channel  string
	PageSize int
	// After is the keyset position the page starts after. nil means the first
	// page. ParseQuery produces it; callers never build one from raw input.
	After *Cursor
}

// Cursor is a decoded keyset position: the (created_at, id) pair of the last
// row on the previous page. The pair is unique because id is the primary key,
// so it is a deterministic tie-break when several rows share a timestamp.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// ListResult carries the page plus honest totals. Counts describes the same
// filters except status, so the per-stage summary stays truthful when a status
// filter is active: total counts only rows the page can return.
type ListResult struct {
	Items    []Application
	PageSize int
	Total    int
	Counts   Counts
	// NextCursor is the opaque token that returns the rows after this page, or
	// "" when this is the last page.
	NextCursor string
}

// ErrApplicationNotFound means the application does not belong to the caller.
// The HTTP layer turns it into a 404 without revealing whether another owner
// holds the id.
var ErrApplicationNotFound = errors.New("application not found")

// Event is one immutable history entry. An application always has a `created`
// event; every actual stage change adds a `stage_changed` event in the same
// transaction as the update. Absent optional columns render as empty strings,
// matching the application read projection.
type Event struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	EventType     string `json:"event_type"`
	FromStatus    string `json:"from_status"`
	ToStatus      string `json:"to_status"`
	Note          string `json:"note"`
	OccurredAt    string `json:"occurred_at"`
}

const (
	EventCreated      = "created"
	EventStageChanged = "stage_changed"
)

// EventCursor is a decoded history keyset position: the (occurred_at, id) pair
// of the last row on the previous page. Both parts are needed because several
// events can share a timestamp.
type EventCursor struct {
	OccurredAt time.Time
	ID         string
}

// EventQuery is a validated, bounded history query. ApplicationID is supplied
// by the route from the verified owner's application id, never by raw input.
type EventQuery struct {
	ApplicationID string
	PageSize      int
	After         *EventCursor
}

// EventListResult carries one page of history plus the opaque continuation
// token. History is a feed, not a counter, so no total is reported.
type EventListResult struct {
	Items      []Event
	PageSize   int
	NextCursor string
}

type Counts struct {
	Saved        int `json:"saved"`
	Applied      int `json:"applied"`
	Interviewing int `json:"interviewing"`
	Offer        int `json:"offer"`
	Accepted     int `json:"accepted"`
	Rejected     int `json:"rejected"`
	Withdrawn    int `json:"withdrawn"`
}

// ParseQuery validates and normalizes raw list query parameters. Unknown
// parameters are rejected so a typo cannot silently widen a result set.
func ParseQuery(values url.Values) (Query, error) {
	for key := range values {
		switch key {
		case QuerySearch, QueryStatus, QueryChannel, QueryCursor, QueryPageSize:
		default:
			return Query{}, fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	query := Query{
		Search:  values.Get(QuerySearch),
		Status:  values.Get(QueryStatus),
		Channel: values.Get(QueryChannel),
	}
	if raw := values.Get(QueryPageSize); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > MaxPageSize {
			return Query{}, fmt.Errorf("%s must be between 1 and %d", QueryPageSize, MaxPageSize)
		}
		query.PageSize = pageSize
	}
	if !query.valid() {
		return Query{}, fmt.Errorf("invalid application query")
	}
	// The cursor is bound to the normalized filters: a token minted for one
	// filter set must not be silently applied to another, which would skip or
	// repeat rows without the caller noticing. An explicitly empty cursor is
	// invalid rather than "first page", so a client bug cannot hide here.
	if values.Has(QueryCursor) {
		cursor, err := query.decodeCursor(values.Get(QueryCursor))
		if err != nil {
			return Query{}, err
		}
		query.After = &cursor
	}
	return query, nil
}

func (q *Query) valid() bool {
	q.Search = strings.TrimSpace(q.Search)
	q.Status = strings.TrimSpace(q.Status)
	q.Channel = strings.TrimSpace(q.Channel)
	if len(q.Search) > maxSearchLength || strings.ContainsRune(q.Search, 0) {
		return false
	}
	if q.Status != "" && !validStatus(q.Status) {
		return false
	}
	if !validChannel(q.Channel) {
		return false
	}
	if q.PageSize == 0 {
		q.PageSize = DefaultPageSize
	}
	return q.PageSize >= 1 && q.PageSize <= MaxPageSize
}

// Normalize applies defaults to a Query built in-process (tests, future
// callers) and reports whether it is usable.
func (q *Query) Normalize() bool { return q.valid() }

// cursorToken is the decoded form of the opaque cursor. Version lets a future
// format reject old tokens explicitly instead of misreading them.
type cursorToken struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"c"`
	ID        string `json:"i"`
	Filters   string `json:"f"`
}

const cursorVersion = 1

// cursorFilterHash binds a cursor to the filters that produced it. It hashes
// the normalized query so insignificant differences (a trimmed search, an
// omitted empty filter) still match.
func (q Query) cursorFilterHash() string {
	sum := sha256.Sum256([]byte("q=" + q.Search + "\x00status=" + q.Status + "\x00channel=" + q.Channel))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// encodeCursor builds the opaque continuation token for a position.
func (q Query) encodeCursor(createdAt time.Time, id string) string {
	payload, err := json.Marshal(cursorToken{
		Version:   cursorVersion,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		ID:        id,
		Filters:   q.cursorFilterHash(),
	})
	if err != nil {
		// The struct only contains strings and an int, so this cannot fail.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// EncodeCursor builds a continuation token for a query and position. Tokens
// are not signed; authorization is enforced by the owner-scoped query.
func EncodeCursor(query Query, createdAt time.Time, id string) string {
	return query.encodeCursor(createdAt, id)
}

// decodeCursor validates an opaque cursor against the current filters. A
// well-formed cursor stays usable even if the row it points at was deleted: it
// is a position, not a row reference.
func (q Query) decodeCursor(raw string) (Cursor, error) {
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
	var token cursorToken
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if token.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if token.Filters != q.cursorFilterHash() {
		return Cursor{}, fmt.Errorf("%s does not match these filters", QueryCursor)
	}
	if !ValidID(token.ID) {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, token.CreatedAt)
	if err != nil {
		return Cursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	return Cursor{CreatedAt: createdAt, ID: token.ID}, nil
}

// ParseEventQuery validates the bounded history query and binds any cursor to
// the application in the route. Unknown parameters are rejected rather than
// ignored, the same rule the application list uses.
func ParseEventQuery(values url.Values, applicationID string) (EventQuery, error) {
	if !ValidID(applicationID) {
		return EventQuery{}, fmt.Errorf("invalid application ID")
	}
	for key := range values {
		switch key {
		case QueryCursor, QueryPageSize:
		default:
			return EventQuery{}, fmt.Errorf("unsupported event query parameter %q", key)
		}
	}
	query := EventQuery{ApplicationID: applicationID}
	if raw := values.Get(QueryPageSize); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > MaxPageSize {
			return EventQuery{}, fmt.Errorf("%s must be between 1 and %d", QueryPageSize, MaxPageSize)
		}
		query.PageSize = pageSize
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultPageSize
	}
	if values.Has(QueryCursor) {
		cursor, err := query.decodeEventCursor(values.Get(QueryCursor))
		if err != nil {
			return EventQuery{}, err
		}
		query.After = &cursor
	}
	return query, nil
}

type eventCursorToken struct {
	Version     int    `json:"v"`
	OccurredAt  string `json:"t"`
	ID          string `json:"i"`
	Application string `json:"a"`
}

const eventCursorVersion = 1

func (q EventQuery) encodeEventCursor(occurredAt time.Time, id string) string {
	payload, err := json.Marshal(eventCursorToken{
		Version:     eventCursorVersion,
		OccurredAt:  occurredAt.UTC().Format(time.RFC3339Nano),
		ID:          id,
		Application: q.ApplicationID,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// EncodeEventCursor builds a continuation token for a history query and
// position. Tokens are not signed; ownership is enforced by the store query.
func EncodeEventCursor(query EventQuery, occurredAt time.Time, id string) string {
	return query.encodeEventCursor(occurredAt, id)
}

// decodeEventCursor validates a token before it reaches SQL. A cursor minted
// for one application is rejected for another, so a page walk cannot silently
// switch to a different application's history.
func (q EventQuery) decodeEventCursor(raw string) (EventCursor, error) {
	if raw == "" {
		return EventCursor{}, fmt.Errorf("%s must not be empty", QueryCursor)
	}
	if len(raw) > maxCursorLength {
		return EventCursor{}, fmt.Errorf("%s is too long", QueryCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return EventCursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	var token eventCursorToken
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return EventCursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	if token.Version != eventCursorVersion || token.Application != q.ApplicationID || !ValidID(token.ID) {
		return EventCursor{}, fmt.Errorf("%s does not match these events", QueryCursor)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, token.OccurredAt)
	if err != nil {
		return EventCursor{}, fmt.Errorf("%s is not a valid cursor", QueryCursor)
	}
	return EventCursor{OccurredAt: occurredAt, ID: token.ID}, nil
}

type Store interface {
	Create(context.Context, string, Input) (Application, error)
	List(context.Context, string, Query) (ListResult, error)
	Get(context.Context, string, string) (Application, error)
	Patch(context.Context, string, string, PatchInput) (Application, error)
	Events(context.Context, string, string, EventQuery) (EventListResult, error)
	Delete(context.Context, string, string) (bool, error)
}

type PostgresStore struct {
	DB *sql.DB
	// Jobs is the reminders store that owns reminder jobs for this
	// application's tasks; nil skips deletion suppression.
	Jobs *reminders.Store
}

// applicationColumns is the read projection used by every returning query. New
// columns read with COALESCE(..., ”) so a row written before migration 005
// still decodes as "not recorded" instead of failing a scan.
const applicationColumns = `id, company, role, status,
	COALESCE(source, ''), COALESCE(address, ''), COALESCE(url, ''),
	COALESCE(channel, ''), COALESCE(job_id, ''), COALESCE(applied_on::text, ''), COALESCE(notes, '')`

// applicationScanDest returns the scan destinations for the applicationColumns
// projection, in column order. database/sql requires one pointer per selected
// column, so these cannot be a shared string.
func applicationScanDest(a *Application) []any {
	return []any{&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address,
		&a.URL, &a.Channel, &a.JobID, &a.AppliedOn, &a.Notes}
}

func (s PostgresStore) Create(ctx context.Context, userID string, in Input) (Application, error) {
	var a Application
	// Empty optional values reach Postgres as NULL, never as an empty string.
	// The channel column only permits NULL or the two campus values, so writing
	// "" would violate its CHECK constraint, and an empty date is not a valid
	// date. COALESCE(..., '') on read turns NULL back into "not recorded".
	var appliedOn any
	if in.AppliedOn != "" {
		appliedOn = in.AppliedOn
	}
	var channel any
	if in.Channel != "" {
		channel = in.Channel
	}
	// The row and its creation event must commit together: there is no state
	// in which an application exists without its first history entry.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO applications (user_id, company, role, status, source, address, url, channel, job_id, applied_on, notes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+applicationColumns,
		userID, in.Company, in.Role, in.Status, in.Source, in.Address, in.URL, channel, in.JobID, appliedOn, in.Notes).Scan(applicationScanDest(&a)...)
	if err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_events (user_id, application_id, event_type, to_status)
		VALUES ($1, $2, $3, $4)`, userID, a.ID, EventCreated, in.Status)
	if err != nil {
		return a, err
	}
	return a, tx.Commit()
}

func (s PostgresStore) List(ctx context.Context, userID string, query Query) (ListResult, error) {
	result := ListResult{PageSize: query.PageSize, Items: []Application{}}

	// Count and page must describe one consistent view, so both run inside a
	// single read-only REPEATABLE READ transaction. Under the default READ
	// COMMITTED a concurrent commit between the two statements could make the
	// total disagree with the rows the page returns.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ListResult{}, err
	}
	defer tx.Rollback()

	// Counts ignore the status filter so the stage summary describes the whole
	// (otherwise filtered) set; the total and page honor the status filter.
	baseWhere, baseArgs := filterClause(query, false, userID)
	pageWhere, pageArgs := filterClause(query, true, userID)

	totalExpr := "count(*)"
	countArgs := baseArgs
	if query.Status != "" {
		countArgs = append(append([]any{}, baseArgs...), query.Status)
		totalExpr = fmt.Sprintf("count(*) FILTER (WHERE status = $%d)", len(countArgs))
	}
	countRow := tx.QueryRowContext(ctx, `SELECT `+totalExpr+
		`, count(*) FILTER (WHERE status = 'saved'), count(*) FILTER (WHERE status = 'applied'),
		count(*) FILTER (WHERE status = 'interviewing'), count(*) FILTER (WHERE status = 'offer'),
		count(*) FILTER (WHERE status = 'accepted'), count(*) FILTER (WHERE status = 'rejected'),
		count(*) FILTER (WHERE status = 'withdrawn')`+
		` FROM applications WHERE `+baseWhere, countArgs...)
	if err := countRow.Scan(&result.Total, &result.Counts.Saved, &result.Counts.Applied, &result.Counts.Interviewing,
		&result.Counts.Offer, &result.Counts.Accepted, &result.Counts.Rejected, &result.Counts.Withdrawn); err != nil {
		return ListResult{}, err
	}

	// No rows in this snapshot means there is no page to seek into.
	if result.Total == 0 {
		return result, nil
	}

	// Keyset (seek) pagination. The page starts strictly after the cursor's
	// (created_at, id) pair, so rows inserted or deleted between requests
	// cannot shift this page the way OFFSET would. Fetching one extra row tells
	// us whether another page exists without a second count.
	pageSQL := `SELECT ` + applicationColumns + `, created_at FROM applications WHERE ` + pageWhere
	args := append([]any{}, pageArgs...)
	if query.After != nil {
		args = append(args, query.After.CreatedAt, query.After.ID)
		pageSQL += fmt.Sprintf(` AND (created_at, id) < ($%d::timestamptz, $%d::uuid)`, len(pageArgs)+1, len(pageArgs)+2)
	}
	args = append(args, query.PageSize+1)
	pageSQL += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := tx.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	page := make([]pageRow, 0, query.PageSize+1)
	for rows.Next() {
		var row pageRow
		if err := rows.Scan(append(applicationScanDest(&row.Application), &row.CreatedAt)...); err != nil {
			return ListResult{}, err
		}
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	if len(page) > query.PageSize {
		// The extra row proves another page exists; the cursor points at the
		// last row this page actually returns, never at the look-ahead row.
		last := page[query.PageSize-1]
		result.NextCursor = query.encodeCursor(last.CreatedAt, last.ID)
		page = page[:query.PageSize]
	}
	for _, row := range page {
		result.Items = append(result.Items, row.Application)
	}
	return result, nil
}

// pageRow is a list row plus the created_at value the keyset cursor needs. The
// timestamp is not part of the public record; it only positions the next page.
type pageRow struct {
	Application
	CreatedAt time.Time
}

// filterClause builds a parameterized WHERE clause. Column names and
// placeholders are constants; user input only ever appears as a bound
// parameter.
func filterClause(query Query, includeStatus bool, userID string) (string, []any) {
	conditions := []string{"user_id = $1"}
	args := []any{userID}
	if query.Search != "" {
		args = append(args, likePattern(query.Search))
		index := len(args)
		conditions = append(conditions, fmt.Sprintf(`(company ILIKE $%d ESCAPE '\' OR role ILIKE $%d ESCAPE '\' OR COALESCE(job_id, '') ILIKE $%d ESCAPE '\')`, index, index, index))
	}
	if includeStatus && query.Status != "" {
		args = append(args, query.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if query.Channel != "" {
		args = append(args, query.Channel)
		conditions = append(conditions, fmt.Sprintf("channel = $%d", len(args)))
	}
	return strings.Join(conditions, " AND "), args
}

// likePattern escapes the wildcard characters a user may type so a search for
// "50%" or "a_b" matches literally instead of widening the result set.
func likePattern(search string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
	return "%" + escaped + "%"
}

func (s PostgresStore) Get(ctx context.Context, userID, id string) (Application, error) {
	var a Application
	err := s.DB.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE user_id = $1 AND id = $2`, userID, id).Scan(applicationScanDest(&a)...)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Patch(ctx context.Context, userID, id string, input PatchInput) (Application, error) {
	var a Application
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()

	// Lock the row first. Concurrent stage updates then serialize: each one
	// sees the status committed by the previous one, so the ledger records a
	// real chain of changes rather than two events from the same former stage.
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT status FROM applications WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, id).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}

	sets := make([]string, 0, 10)
	args := []any{userID, id}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if input.Company != nil {
		set("company", *input.Company)
	}
	if input.Role != nil {
		set("role", *input.Role)
	}
	if input.Status != nil {
		set("status", *input.Status)
	}
	if input.Source != nil {
		set("source", *input.Source)
	}
	if input.Address != nil {
		set("address", *input.Address)
	}
	if input.URL != nil {
		set("url", *input.URL)
	}
	if input.Channel != nil {
		if *input.Channel == "" {
			// Clearing the channel writes SQL NULL: the column only permits NULL
			// or the two campus values, so "" would violate its CHECK.
			sets = append(sets, "channel = NULL")
		} else {
			set("channel", *input.Channel)
		}
	}
	if input.JobID != nil {
		set("job_id", *input.JobID)
	}
	if input.Notes != nil {
		set("notes", *input.Notes)
	}
	if input.AppliedOn != nil {
		if *input.AppliedOn == "" {
			// Clearing the date must write SQL NULL; an empty string is not a
			// valid date.
			sets = append(sets, "applied_on = NULL")
		} else {
			set("applied_on", *input.AppliedOn)
		}
	}
	err = tx.QueryRowContext(ctx, `UPDATE applications SET `+strings.Join(sets, ", ")+
		` WHERE user_id = $1 AND id = $2 RETURNING `+applicationColumns, args...).Scan(applicationScanDest(&a)...)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}

	// A patch that leaves the stage unchanged is an ordinary edit, not a stage
	// change: it must not create a duplicate history entry.
	if input.Status != nil && *input.Status != previous {
		var note any
		if input.StageNote != nil && *input.StageNote != "" {
			note = *input.StageNote
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO application_events
			(user_id, application_id, event_type, from_status, to_status, note)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			userID, id, EventStageChanged, previous, *input.Status, note)
		if err != nil {
			return a, err
		}
	}
	return a, tx.Commit()
}

// eventColumns is the read projection for history rows. Nullable columns read
// with COALESCE so a missing value becomes "not recorded" instead of failing a
// scan. occurred_at is appended by the page query as a real timestamp because
// the keyset cursor needs it.
const eventColumns = `id, application_id, event_type,
	COALESCE(from_status, ''), COALESCE(to_status, ''), COALESCE(note, '')`

type eventPageRow struct {
	Event      Event
	OccurredAt time.Time
}

// Events returns one owner-scoped page of an application's history, newest
// first. The application is checked inside the same read-only snapshot as the
// rows, so a cursor minted for another owner's application cannot leak data.
func (s PostgresStore) Events(ctx context.Context, userID, applicationID string, query EventQuery) (EventListResult, error) {
	query.ApplicationID = applicationID
	result := EventListResult{PageSize: query.PageSize, Items: []Event{}}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return EventListResult{}, err
	}
	defer tx.Rollback()

	var owned int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM applications WHERE user_id = $1 AND id = $2`, userID, applicationID).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) {
		return EventListResult{}, ErrApplicationNotFound
	}
	if err != nil {
		return EventListResult{}, err
	}

	pageSQL := `SELECT ` + eventColumns + `, occurred_at FROM application_events
		WHERE user_id = $1 AND application_id = $2`
	args := []any{userID, applicationID}
	if query.After != nil {
		args = append(args, query.After.OccurredAt, query.After.ID)
		pageSQL += fmt.Sprintf(` AND (occurred_at, id) < ($%d::timestamptz, $%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, query.PageSize+1)
	pageSQL += ` ORDER BY occurred_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := tx.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return EventListResult{}, err
	}
	defer rows.Close()
	page := make([]eventPageRow, 0, query.PageSize+1)
	for rows.Next() {
		var row eventPageRow
		if err := rows.Scan(&row.Event.ID, &row.Event.ApplicationID, &row.Event.EventType,
			&row.Event.FromStatus, &row.Event.ToStatus, &row.Event.Note, &row.OccurredAt); err != nil {
			return EventListResult{}, err
		}
		row.Event.OccurredAt = row.OccurredAt.UTC().Format(time.RFC3339Nano)
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		return EventListResult{}, err
	}
	if len(page) > query.PageSize {
		last := page[query.PageSize-1]
		result.NextCursor = query.encodeEventCursor(last.OccurredAt, last.Event.ID)
		page = page[:query.PageSize]
	}
	for _, row := range page {
		result.Items = append(result.Items, row.Event)
	}
	return result, nil
}

func (s PostgresStore) Delete(ctx context.Context, userID, id string) (bool, error) {
	// Suppress queued reminder jobs for this application's tasks before the
	// cascade deletes them, so the tombstones record why they will not fire.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if s.Jobs != nil {
		if err := s.Jobs.SuppressByApplication(ctx, tx, userID, id, reminders.SuppressDeleted); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM applications WHERE user_id=$1 AND id=$2`, userID, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		_ = tx.Rollback()
		return false, nil
	}
	return true, tx.Commit()
}
