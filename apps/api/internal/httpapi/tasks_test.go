package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/clerk"
	"hireloop/api/internal/tasks"
)

// fakeTaskStore models the store contract without a database. It applies the
// same owner scoping and not-found behavior as PostgresStore so the HTTP tests
// exercise routing, validation, status transitions and isolation.
type fakeTaskStore struct {
	records   map[string][]tasks.Task
	seq       int
	lastInput tasks.Input
	lastPatch tasks.PatchInput
	err       error
}

func newFakeTaskStore() *fakeTaskStore {
	return &fakeTaskStore{records: make(map[string][]tasks.Task)}
}

func (s *fakeTaskStore) nextID() string {
	s.seq++
	return fmt.Sprintf("22222222-2222-4222-8222-%012d", s.seq)
}

func (s *fakeTaskStore) Create(_ context.Context, owner string, input tasks.Input) (tasks.Task, error) {
	if s.err != nil {
		return tasks.Task{}, s.err
	}
	s.lastInput = input
	task := tasks.Task{
		ID:            s.nextID(),
		ApplicationID: input.ApplicationID,
		Title:         input.Title,
		Status:        tasks.StatusOpen,
		DuePrecision:  input.DuePrecision,
		DueDate:       input.DueDate,
		DueLocal:      input.DueLocal,
		TimeZone:      input.TimeZone,
		CreatedAt:     "2026-11-01T09:00:00Z",
	}
	if task.DuePrecision == "" {
		task.DuePrecision = tasks.PrecisionNone
	}
	// Mirror the real store: a stored instant task also carries the resolved
	// UTC instant, so reads and the calendar export see the same complete
	// shape the database CHECK enforces.
	if task.DuePrecision == tasks.PrecisionInstant && task.DueLocal != "" && task.TimeZone != "" {
		wall, err := time.Parse("2006-01-02T15:04", task.DueLocal)
		if err == nil {
			if loc, err := time.LoadLocation(task.TimeZone); err == nil {
				if candidate := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc); candidate.In(loc).Format("2006-01-02T15:04") == wall.Format("2006-01-02T15:04") {
					task.DueAt = candidate.UTC().Format(time.RFC3339)
				}
			}
		}
	}
	s.records[owner] = append(s.records[owner], task)
	return task, nil
}

func (s *fakeTaskStore) List(_ context.Context, owner string, filter tasks.Filter) (tasks.ListResult, error) {
	if s.err != nil {
		return tasks.ListResult{}, s.err
	}
	filter.Normalize()
	matching := []tasks.Task{}
	for _, task := range s.records[owner] {
		if filter.ShowAll || task.Status == tasks.StatusOpen {
			matching = append(matching, task)
		}
	}
	result := tasks.ListResult{PageSize: filter.PageSize, Total: len(matching), Items: []tasks.Task{}}
	start := 0
	if filter.After != nil {
		// IDs are sequential in this fake, so the cursor's ID identifies the
		// position exactly; real tie-breaking is covered by the SQL tests.
		for i, task := range matching {
			if task.ID == filter.After.ID {
				start = i + 1
				break
			}
			start = i + 1
		}
	}
	for i := start; i < len(matching) && i < start+filter.PageSize; i++ {
		result.Items = append(result.Items, matching[i])
	}
	if start+filter.PageSize < len(matching) {
		last := result.Items[len(result.Items)-1]
		created, _ := time.Parse(time.RFC3339, last.CreatedAt)
		result.NextCursor = tasks.EncodeCursor(filter, deadlineDay(last), deadlineAt(last), created, last.ID)
	}
	return result, nil
}

func deadlineDay(task tasks.Task) string {
	if task.DuePrecision == tasks.PrecisionDate {
		return task.DueDate
	}
	return "infinity"
}

func deadlineAt(task tasks.Task) string {
	if task.DuePrecision == tasks.PrecisionInstant && task.DueAt != "" {
		return task.DueAt
	}
	return "-infinity"
}

func (s *fakeTaskStore) Get(_ context.Context, owner, id string) (tasks.Task, error) {
	if s.err != nil {
		return tasks.Task{}, s.err
	}
	for _, task := range s.records[owner] {
		if task.ID == id {
			return task, nil
		}
	}
	return tasks.Task{}, tasks.ErrNotFound
}

func (s *fakeTaskStore) Patch(_ context.Context, owner, id string, input tasks.PatchInput) (tasks.Task, error) {
	if s.err != nil {
		return tasks.Task{}, s.err
	}
	s.lastPatch = input
	for i, task := range s.records[owner] {
		if task.ID != id {
			continue
		}
		if input.Title != nil {
			task.Title = *input.Title
		}
		if input.Status != nil {
			task.Status = *input.Status
		}
		if input.DuePrecision != nil {
			task.DuePrecision = *input.DuePrecision
			task.DueDate, task.DueLocal, task.DueAt, task.TimeZone = "", "", "", ""
			if input.DueDate != nil {
				task.DueDate = *input.DueDate
			}
			if input.DueLocal != nil {
				task.DueLocal = *input.DueLocal
			}
			if input.TimeZone != nil {
				task.TimeZone = *input.TimeZone
			}
		}
		s.records[owner][i] = task
		return task, nil
	}
	return tasks.Task{}, tasks.ErrNotFound
}

const taskAppID = "11111111-1111-4111-8111-111111111111"

type taskHarness struct {
	store   *fakeTaskStore
	handler http.Handler
	token   string
	tokenB  string
}

func newTaskHarness(t *testing.T) taskHarness {
	t.Helper()
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokenA := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	tokenB := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-b"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := newFakeTaskStore()
	handler := NewHandlerWithTasks(verifier, testMapper{bindings: map[string]string{
		"clerk-a": "owner-a", "clerk-b": "owner-b",
	}}, nil, store)
	return taskHarness{store: store, handler: handler, token: tokenA, tokenB: tokenB}
}

func (h taskHarness) request(token, method, body, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func taskPath(id string) string { return "/api/tasks/" + id }

func TestTaskInvalidMergedStateReturnsBadRequest(t *testing.T) {
	h := newTaskHarness(t)
	h.store.err = tasks.ErrInvalid
	w := h.request(h.token, http.MethodPatch, `{"due_precision":"date","due_date":""}`, taskPath(taskAppID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid merged task: got %d, want 400", w.Code)
	}
}

func TestTaskRoutesRequireAuthAndEnforceOwnerIsolation(t *testing.T) {
	h := newTaskHarness(t)
	for _, token := range []string{"", "invalid"} {
		if w := h.request(token, http.MethodGet, "", "/api/tasks"); w.Code != http.StatusUnauthorized {
			t.Fatalf("list with token %q: %d", token, w.Code)
		}
	}
	create := fmt.Sprintf(`{"application_id":%q,"title":"Apply","due_precision":"date","due_date":"2026-11-12"}`, taskAppID)
	if w := h.request(h.token, http.MethodPost, create, "/api/tasks"); w.Code != http.StatusCreated {
		t.Fatalf("owner create: %d %s", w.Code, w.Body.String())
	}
	id := h.store.records["owner-a"][0].ID
	for _, token := range []string{"", "invalid"} {
		if w := h.request(token, http.MethodGet, "", taskPath(id)); w.Code != http.StatusUnauthorized {
			t.Fatalf("get with token %q: %d", token, w.Code)
		}
	}
	for _, method := range []string{"GET", "PATCH"} {
		body := ""
		if method == "PATCH" {
			body = `{"status":"done"}`
		}
		if w := h.request(h.tokenB, method, body, taskPath(id)); w.Code != http.StatusNotFound {
			t.Fatalf("%s other owner's task: %d %s, want 404", method, w.Code, w.Body.String())
		}
	}
	var mine, theirs struct {
		Items []tasks.Task `json:"items"`
	}
	if err := json.Unmarshal(h.request(h.token, http.MethodGet, "", "/api/tasks").Body.Bytes(), &mine); err != nil || len(mine.Items) != 1 {
		t.Fatalf("owner A list: %v %+v", err, mine)
	}
	if err := json.Unmarshal(h.request(h.tokenB, http.MethodGet, "", "/api/tasks").Body.Bytes(), &theirs); err != nil || len(theirs.Items) != 0 {
		t.Fatalf("owner B list: %v %+v", err, theirs)
	}
	// The body-supplied owner cannot widen a write.
	spoofed := fmt.Sprintf(`{"application_id":%q,"title":"Spoof","user_id":"owner-b"}`, taskAppID)
	if w := h.request(h.token, http.MethodPost, spoofed, "/api/tasks"); w.Code != http.StatusBadRequest {
		t.Fatalf("spoofed owner: %d, want 400", w.Code)
	}
	got, err := h.store.Get(context.Background(), "owner-a", id)
	if err != nil || got.Status != tasks.StatusOpen || got.Title != "Apply" {
		t.Fatalf("A's task after B's attempts = %+v, err = %v", got, err)
	}
}

func TestTaskCreateEditCompleteCancelAndPrecision(t *testing.T) {
	h := newTaskHarness(t)
	create := fmt.Sprintf(`{"application_id":%q,"title":"Submit assessment","due_precision":"date","due_date":"2026-11-12"}`, taskAppID)
	w := h.request(h.token, http.MethodPost, create, "/api/tasks")
	if w.Code != http.StatusCreated {
		t.Fatalf("create date-only task: %d %s", w.Code, w.Body.String())
	}
	var created tasks.Task
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DuePrecision != tasks.PrecisionDate || created.DueDate != "2026-11-12" ||
		created.DueAt != "" || created.TimeZone != "" || created.Status != tasks.StatusOpen {
		t.Fatalf("created task = %+v", created)
	}
	id := created.ID

	instant := fmt.Sprintf(`{"application_id":%q,"title":"Interview","due_precision":"instant","due_local":"2026-11-12T18:00","time_zone":"Asia/Kolkata"}`, taskAppID)
	w = h.request(h.token, http.MethodPost, instant, "/api/tasks")
	if w.Code != http.StatusCreated {
		t.Fatalf("create instant task: %d %s", w.Code, w.Body.String())
	}
	var interview tasks.Task
	if err := json.Unmarshal(w.Body.Bytes(), &interview); err != nil ||
		interview.DueLocal != "2026-11-12T18:00" || interview.TimeZone != "Asia/Kolkata" || interview.DueDate != "" {
		t.Fatalf("instant task = %+v, err = %v", interview, err)
	}

	// Edit the title, then complete and cancel. The HTTP layer sends exactly
	// the patch the client asked for; the store owns the shape re-validation.
	if w := h.request(h.token, http.MethodPatch, `{"title":"Submit the assessment"}`, taskPath(id)); w.Code != http.StatusOK {
		t.Fatalf("edit title: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(h.request(h.token, http.MethodGet, "", taskPath(id)).Body.String(), "Submit the assessment") {
		t.Fatal("edited title was not persisted")
	}
	if w := h.request(h.token, http.MethodPatch, `{"status":"done"}`, taskPath(id)); w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}
	if w := h.request(h.token, http.MethodPatch, `{"status":"cancelled"}`, taskPath(interview.ID)); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}

	var open, all struct {
		Items []tasks.Task `json:"items"`
	}
	if err := json.Unmarshal(h.request(h.token, http.MethodGet, "", "/api/tasks").Body.Bytes(), &open); err != nil || len(open.Items) != 0 {
		t.Fatalf("open actions after completion = %+v, err = %v", open, err)
	}
	if err := json.Unmarshal(h.request(h.token, http.MethodGet, "", "/api/tasks?filter=all").Body.Bytes(), &all); err != nil || len(all.Items) != 2 {
		t.Fatalf("all tasks = %+v, err = %v", all, err)
	}
	for _, task := range all.Items {
		want := tasks.StatusDone
		if task.ID == interview.ID {
			want = tasks.StatusCancelled
		}
		if task.Status != want {
			t.Fatalf("task %s status = %q, want %q (reload persistence)", task.ID, task.Status, want)
		}
	}
}

func TestTaskIcsExportIsOwnerScopedAndWellFormed(t *testing.T) {
	h := newTaskHarness(t)
	instant := fmt.Sprintf(`{"application_id":%q,"title":"Interview","due_precision":"instant","due_local":"2026-11-12T18:00","time_zone":"Asia/Kolkata"}`, taskAppID)
	w := h.request(h.token, http.MethodPost, instant, "/api/tasks")
	if w.Code != http.StatusCreated {
		t.Fatalf("create instant task: %d %s", w.Code, w.Body.String())
	}
	var interview tasks.Task
	if err := json.Unmarshal(w.Body.Bytes(), &interview); err != nil {
		t.Fatal(err)
	}
	if interview.DueAt == "" {
		t.Fatalf("fake store should carry the resolved instant: %+v", interview)
	}

	icsPath := taskPath(interview.ID) + "/ics"
	if w := h.request(h.tokenB, http.MethodGet, "", icsPath); w.Code != http.StatusNotFound {
		t.Fatalf("other owner's export = %d, want 404", w.Code)
	}
	w = h.request(h.token, http.MethodGet, "", icsPath)
	if w.Code != http.StatusOK {
		t.Fatalf("owner export = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Fatalf("export is not a calendar: %q", body)
	}
	if !strings.Contains(body, "DUE:20261112T123000Z") {
		t.Fatalf("export = %q, want the stored instant in UTC", body)
	}
	if !strings.Contains(body, "SUMMARY:Interview") || !strings.Contains(body, "STATUS:NEEDS-ACTION") {
		t.Fatalf("export = %q", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/calendar") {
		t.Fatalf("content type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "task-"+interview.ID+".ics") {
		t.Fatalf("content disposition = %q", cd)
	}
}

func TestTaskIcsExportRejectsMissingAuthInvalidAndUnknownTasks(t *testing.T) {
	h := newTaskHarness(t)
	const id = "22222222-2222-4222-8222-000000000001"
	if w := h.request("", http.MethodGet, "", taskPath(id)+"/ics"); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", w.Code)
	}
	h.store.err = tasks.ErrInvalid
	if w := h.request(h.token, http.MethodGet, "", taskPath(id)+"/ics"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid task = %d, want 400", w.Code)
	}
	h.store.err = nil
	for _, p := range []string{taskPath("not-a-uuid") + "/ics", taskPath("22222222-2222-4222-8222-short") + "/ics"} {
		if w := h.request(h.token, http.MethodGet, "", p); w.Code != http.StatusBadRequest {
			t.Fatalf("malformed id %s = %d, want 400", p, w.Code)
		}
	}
	if w := h.request(h.token, http.MethodGet, "", taskPath(id)+"/ics"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown task = %d, want 404", w.Code)
	}
}

func TestTaskValidationAndQueryAreBounded(t *testing.T) {
	h := newTaskHarness(t)
	badBodies := map[string]string{
		"empty":             `{}`,
		"unknown field":     fmt.Sprintf(`{"application_id":%q,"title":"x","owner":"b"}`, taskAppID),
		"missing title":     fmt.Sprintf(`{"application_id":%q}`, taskAppID),
		"bad application":   `{"application_id":"nope","title":"x"}`,
		"date with a time":  fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"date","due_date":"2026-11-12T09:00"}`, taskAppID),
		"date with a zone":  fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"date","due_date":"2026-11-12","time_zone":"Asia/Kolkata"}`, taskAppID),
		"instant no zone":   fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"instant","due_local":"2026-11-12T18:00"}`, taskAppID),
		"nonexistent local": fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"instant","due_local":"2026-03-08T02:30","time_zone":"America/New_York"}`, taskAppID),
		"ambiguous local":   fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"instant","due_local":"2026-11-01T01:30","time_zone":"America/New_York"}`, taskAppID),
		"half-hour gap":     fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"instant","due_local":"2026-10-04T02:15","time_zone":"Australia/Lord_Howe"}`, taskAppID),
		"half-hour overlap": fmt.Sprintf(`{"application_id":%q,"title":"x","due_precision":"instant","due_local":"2026-04-05T01:45","time_zone":"Australia/Lord_Howe"}`, taskAppID),
		"oversized title":   fmt.Sprintf(`{"application_id":%q,"title":"%s"}`, taskAppID, strings.Repeat("x", 201)),
		"nul in title":      fmt.Sprintf(`{"application_id":%q,"title":"x\u0000y"}`, taskAppID),
	}
	for name, body := range badBodies {
		if w := h.request(h.token, http.MethodPost, body, "/api/tasks"); w.Code != http.StatusBadRequest {
			t.Errorf("create %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	const path = "/api/tasks/22222222-2222-4222-8222-000000000001"
	for name, body := range map[string]string{
		"empty patch":    `{}`,
		"bad status":     `{"status":"paused"}`,
		"bad precision":  `{"due_precision":"sometime"}`,
		"trailing json":  `{"status":"done"}{}`,
		"malformed json": `{"status":`,
	} {
		if w := h.request(h.token, http.MethodPatch, body, path); w.Code != http.StatusBadRequest {
			t.Errorf("patch %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/tasks/not-a-uuid", "/api/tasks/22222222-2222-4222-8222-short"} {
		if w := h.request(h.token, http.MethodGet, "", path); w.Code != http.StatusBadRequest {
			t.Errorf("malformed id %s: %d", path, w.Code)
		}
	}
	for name, query := range map[string]string{
		"unknown filter":  "?filter=today",
		"unknown param":   "?status=open",
		"empty filter":    "?filter=",
		"empty cursor":    "?cursor=",
		"broken cursor":   "?cursor=not-a-token",
		"zero page size":  "?page_size=0",
		"oversized page":  "?page_size=51",
		"nonnumeric page": "?page_size=many",
	} {
		if w := h.request(h.token, http.MethodGet, "", "/api/tasks"+query); w.Code != http.StatusBadRequest {
			t.Errorf("query %s: %d", name, w.Code)
		}
	}
	if w := h.request(h.token, http.MethodGet, "", "/api/tasks?filter=all"); w.Code != http.StatusOK {
		t.Fatalf("filter=all: %d %s", w.Code, w.Body.String())
	}

	// Store failures are redacted, and a not-found result is a 404.
	h.store.err = errors.New("private database detail")
	if w := h.request(h.token, http.MethodGet, "", "/api/tasks"); w.Code != http.StatusServiceUnavailable || bytes.Contains(w.Body.Bytes(), []byte("private")) {
		t.Fatalf("list failure: %d %s", w.Code, w.Body.String())
	}
	h.store.err = nil
	if w := h.request(h.token, http.MethodGet, "", path); w.Code != http.StatusNotFound {
		t.Fatalf("missing task: %d, want 404", w.Code)
	}
}

func TestTaskListIsBoundedAndContinuesWithOwnerIsolation(t *testing.T) {
	h := newTaskHarness(t)
	for i := 0; i < 5; i++ {
		body := fmt.Sprintf(`{"application_id":%q,"title":"Task %d","due_precision":"date","due_date":"2026-12-%02d"}`, taskAppID, i, i+1)
		if w := h.request(h.token, http.MethodPost, body, "/api/tasks"); w.Code != http.StatusCreated {
			t.Fatalf("create task %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	other := fmt.Sprintf(`{"application_id":%q,"title":"B task","due_precision":"date","due_date":"2026-12-01"}`, taskAppID)
	if w := h.request(h.tokenB, http.MethodPost, other, "/api/tasks"); w.Code != http.StatusCreated {
		t.Fatalf("create B task: %d %s", w.Code, w.Body.String())
	}

	type envelope struct {
		Items      []tasks.Task `json:"items"`
		PageSize   int          `json:"page_size"`
		Total      int          `json:"total"`
		NextCursor *string      `json:"next_cursor"`
	}
	read := func(token, path string) envelope {
		t.Helper()
		w := h.request(token, http.MethodGet, "", path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var result envelope
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	seen := map[string]bool{}
	cursor := ""
	for page, wantCount := range []int{2, 2, 1} {
		path := "/api/tasks?page_size=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		result := read(h.token, path)
		if result.PageSize != 2 || result.Total != 5 || len(result.Items) != wantCount {
			t.Fatalf("page %d = %+v", page, result)
		}
		for _, task := range result.Items {
			if seen[task.ID] {
				t.Fatalf("page %d repeated task %s", page, task.ID)
			}
			seen[task.ID] = true
		}
		if page == 2 {
			if result.NextCursor != nil {
				t.Fatal("last page still has a continuation cursor")
			}
		} else if result.NextCursor == nil || *result.NextCursor == "" {
			t.Fatalf("page %d has no continuation cursor", page)
		} else {
			cursor = *result.NextCursor
		}
	}
	if len(seen) != 5 {
		t.Fatalf("walked %d distinct tasks, want 5", len(seen))
	}

	theirs := read(h.tokenB, "/api/tasks?page_size=2")
	if theirs.Total != 1 || len(theirs.Items) != 1 || theirs.NextCursor != nil || seen[theirs.Items[0].ID] {
		t.Fatalf("owner B page = %+v", theirs)
	}

	// Page size is capped and a cursor is bound to the view that minted it.
	if w := h.request(h.token, http.MethodGet, "", "/api/tasks?page_size=51"); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized page: %d", w.Code)
	}
	if w := h.request(h.token, http.MethodGet, "", "/api/tasks?filter=all&cursor="+url.QueryEscape(cursor)); w.Code != http.StatusBadRequest {
		t.Fatalf("cursor from another view: %d", w.Code)
	}
}

func TestTaskRoutesUnavailableWithoutStore(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	handler := NewHandlerWithTasks(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a"}}, nil, nil)
	for _, tc := range []struct{ method, body, path string }{
		{http.MethodGet, "", "/api/tasks"},
		{http.MethodPost, fmt.Sprintf(`{"application_id":%q,"title":"x"}`, taskAppID), "/api/tasks"},
		{http.MethodGet, "", taskPath("22222222-2222-4222-8222-000000000001")},
		{http.MethodGet, "", taskPath("22222222-2222-4222-8222-000000000001") + "/ics"},
		{http.MethodPatch, `{"status":"done"}`, taskPath("22222222-2222-4222-8222-000000000001")},
	} {
		r := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d, want 503", tc.method, tc.path, w.Code)
		}
	}
}
