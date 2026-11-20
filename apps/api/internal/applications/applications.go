package applications

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Field bounds. The database constraints in migration 005 mirror the text
// bounds here so oversized input is rejected by both layers.
const (
	maxRequiredTextLength = 200
	maxOptionalTextLength = 200
	maxURLLength          = 2048
	maxNotesLength        = 2000

	maxSearchLength = 100
	// DefaultPageSize and MaxPageSize bound every list response: the tracker
	// must never be able to ask for (or be handed) an unbounded page.
	DefaultPageSize = 25
	MaxPageSize     = 50

	// QueryKeys are the only accepted list query parameters; anything else is
	// rejected rather than silently ignored.
	QuerySearch   = "q"
	QueryStatus   = "status"
	QueryChannel  = "channel"
	QueryPage     = "page"
	QueryPageSize = "page_size"
)

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
	return validText(i.Company) && validText(i.Role) && (i.Status == "saved" || i.Status == "applied") &&
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
	return i.Status == nil || *i.Status == "saved" || *i.Status == "applied"
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
	Page     int
	PageSize int
}

// ListResult carries the page plus honest totals. Counts describes the same
// filters except status, so the saved/applied summary stays truthful when a
// status filter is active: total = counts.Saved + counts.Applied.
type ListResult struct {
	Items      []Application
	Page       int
	PageSize   int
	Total      int
	TotalPages int
	Counts     Counts
}

type Counts struct {
	Saved   int
	Applied int
}

// ParseQuery validates and normalizes raw list query parameters. Unknown
// parameters are rejected so a typo cannot silently widen a result set.
func ParseQuery(values url.Values) (Query, error) {
	for key := range values {
		switch key {
		case QuerySearch, QueryStatus, QueryChannel, QueryPage, QueryPageSize:
		default:
			return Query{}, fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	query := Query{
		Search:  values.Get(QuerySearch),
		Status:  values.Get(QueryStatus),
		Channel: values.Get(QueryChannel),
	}
	if raw := values.Get(QueryPage); raw != "" {
		page, err := strconv.Atoi(raw)
		if err != nil || page < 1 {
			return Query{}, fmt.Errorf("%s must be a positive integer", QueryPage)
		}
		query.Page = page
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
	return query, nil
}

func (q *Query) valid() bool {
	q.Search = strings.TrimSpace(q.Search)
	q.Status = strings.TrimSpace(q.Status)
	q.Channel = strings.TrimSpace(q.Channel)
	if len(q.Search) > maxSearchLength || strings.ContainsRune(q.Search, 0) {
		return false
	}
	if q.Status != "" && q.Status != "saved" && q.Status != "applied" {
		return false
	}
	if !validChannel(q.Channel) {
		return false
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Page < 1 {
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

type Store interface {
	Create(context.Context, string, Input) (Application, error)
	List(context.Context, string, Query) (ListResult, error)
	Get(context.Context, string, string) (Application, error)
	Patch(context.Context, string, string, PatchInput) (Application, error)
	Delete(context.Context, string, string) (bool, error)
}

type PostgresStore struct{ DB *sql.DB }

// applicationColumns is the read projection used by every returning query. New
// columns read with COALESCE(..., '') so a row written before migration 005
// still decodes as "not recorded" instead of failing a scan.
const applicationColumns = `id, company, role, status,
	COALESCE(source, ''), COALESCE(address, ''), COALESCE(url, ''),
	COALESCE(channel, ''), COALESCE(job_id, ''), COALESCE(applied_on::text, ''), COALESCE(notes, '')`

const applicationScan = `&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address,
	&a.URL, &a.Channel, &a.JobID, &a.AppliedOn, &a.Notes`

func (s PostgresStore) Create(ctx context.Context, userID string, in Input) (Application, error) {
	var a Application
	// An empty date must reach Postgres as NULL, not as an empty string cast to
	// date.
	var appliedOn any
	if in.AppliedOn != "" {
		appliedOn = in.AppliedOn
	}
	err := s.DB.QueryRowContext(ctx, `INSERT INTO applications (user_id, company, role, status, source, address, url, channel, job_id, applied_on, notes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+applicationColumns,
		userID, in.Company, in.Role, in.Status, in.Source, in.Address, in.URL, in.Channel, in.JobID, appliedOn, in.Notes).Scan(applicationScan)
	return a, err
}

func (s PostgresStore) List(ctx context.Context, userID string, query Query) (ListResult, error) {
	// Counts ignore the status filter so the saved/applied summary describes
	// the whole (otherwise filtered) set rather than the current page.
	baseWhere, baseArgs := filterClause(query, false, userID)
	totalWhere, totalArgs := filterClause(query, true, userID)

	result := ListResult{Page: query.Page, PageSize: query.PageSize, Items: []Application{}}
	countRow := s.DB.QueryRowContext(ctx, `SELECT count(*),
		count(*) FILTER (WHERE status = 'saved'),
		count(*) FILTER (WHERE status = 'applied')
		FROM applications WHERE `+baseWhere, baseArgs...)
	var saved, applied int
	if err := countRow.Scan(&result.Total, &saved, &applied); err != nil {
		return ListResult{}, err
	}
	result.Counts = Counts{Saved: saved, Applied: applied}
	result.TotalPages = (result.Total + query.PageSize - 1) / query.PageSize

	// Stable ordering: newest first, then id, so pagination never repeats or
	// skips a row when created_at ties.
	offset := (query.Page - 1) * query.PageSize
	rows, err := s.DB.QueryContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE `+totalWhere+
		` ORDER BY created_at DESC, id DESC LIMIT $`+strconv.Itoa(len(totalArgs)+1)+` OFFSET $`+strconv.Itoa(len(totalArgs)+2),
		append(totalArgs, query.PageSize, offset)...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Application
		if err := rows.Scan(applicationScan); err != nil {
			return ListResult{}, err
		}
		result.Items = append(result.Items, a)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	return result, nil
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
	err := s.DB.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE user_id = $1 AND id = $2`, userID, id).Scan(applicationScan)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Patch(ctx context.Context, userID, id string, input PatchInput) (Application, error) {
	var a Application
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
		set("channel", *input.Channel)
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
	err := s.DB.QueryRowContext(ctx, `UPDATE applications SET `+strings.Join(sets, ", ")+
		` WHERE user_id = $1 AND id = $2 RETURNING `+applicationColumns, args...).Scan(applicationScan)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Delete(ctx context.Context, userID, id string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM applications WHERE user_id=$1 AND id=$2`, userID, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected != 0, nil
}
