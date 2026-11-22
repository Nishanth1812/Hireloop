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
	"sort"
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
)

// testRecord pairs a record with the created_at the real store would assign.
// The fake models the same (created_at DESC, id DESC) order and keyset
// predicate as PostgresStore so HTTP-level tests exercise real cursor tokens.
type testRecord struct {
	applications.Application
	createdAt time.Time
}

type testEventRecord struct {
	applications.Event
	occurredAt time.Time
}

type testStore struct {
	records  map[string][]testRecord
	events   map[string][]testEventRecord
	seq      int
	eventSeq int
	base     time.Time
	err      error
}

func newTestStore() *testStore {
	return &testStore{
		records: make(map[string][]testRecord),
		events:  make(map[string][]testEventRecord),
		base:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestAuthenticationFailsClosedWithoutClerk(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	NewHandler(nil, nil, nil).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !bytes.Contains(w.Body.Bytes(), []byte("authentication unavailable")) {
		t.Fatalf("status = %d body = %q, want 503 authentication unavailable", w.Code, w.Body.String())
	}
}

func (s *testStore) nextID() string {
	s.seq++
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", s.seq)
}

// cursorAt returns the continuation token positioned at a stored row so a test
// can request the page after a specific record without reaching into the fake.
func (s *testStore) cursorAt(userID, id string) string {
	for _, record := range s.records[userID] {
		if record.ID == id {
			return applications.EncodeCursor(applications.Query{}, record.createdAt, record.ID)
		}
	}
	return ""
}

func (s *testStore) Create(_ context.Context, userID string, input applications.Input) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	a := applications.Application{
		ID:        s.nextID(),
		Company:   input.Company,
		Role:      input.Role,
		Status:    input.Status,
		Source:    input.Source,
		Address:   input.Address,
		URL:       input.URL,
		Channel:   input.Channel,
		JobID:     input.JobID,
		AppliedOn: input.AppliedOn,
		Notes:     input.Notes,
	}
	s.records[userID] = append(s.records[userID], testRecord{
		Application: a,
		createdAt:   s.base.Add(time.Duration(s.seq) * time.Minute),
	})
	s.appendEvent(userID, a.ID, applications.Event{
		EventType: applications.EventCreated,
		ToStatus:  a.Status,
	})
	return a, nil
}

func (s *testStore) appendEvent(userID, applicationID string, event applications.Event) {
	s.eventSeq++
	event.ID = fmt.Sprintf("33333333-3333-4333-8333-%012d", s.eventSeq)
	event.ApplicationID = applicationID
	event.OccurredAt = s.base.Add(time.Duration(s.eventSeq) * time.Second).UTC().Format(time.RFC3339Nano)
	s.events[applicationID] = append(s.events[applicationID], testEventRecord{
		Event:      event,
		occurredAt: s.base.Add(time.Duration(s.eventSeq) * time.Second),
	})
}

// List models the Postgres store's contract: newest first, filters applied
// before pagination, counts over the same filters except status, and a real
// keyset cursor. The real SQL is not exercised here; no test connects to a
// database.
func (s *testStore) List(_ context.Context, userID string, query applications.Query) (applications.ListResult, error) {
	if s.err != nil {
		return applications.ListResult{}, s.err
	}
	records := append([]testRecord(nil), s.records[userID]...)
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].createdAt.Equal(records[j].createdAt) {
			return records[i].createdAt.After(records[j].createdAt)
		}
		return records[i].ID > records[j].ID
	})

	// Counts ignore the status filter so they describe the whole filtered set.
	matched := make([]testRecord, 0, len(records))
	for _, record := range records {
		if query.Channel != "" && record.Channel != query.Channel {
			continue
		}
		if query.Search != "" && !containsFold(record.Company, query.Search) && !containsFold(record.Role, query.Search) && !containsFold(record.JobID, query.Search) {
			continue
		}
		matched = append(matched, record)
	}
	result := applications.ListResult{PageSize: query.PageSize, Items: []applications.Application{}}
	for _, record := range matched {
		switch record.Status {
		case applications.StatusSaved:
			result.Counts.Saved++
		case applications.StatusApplied:
			result.Counts.Applied++
		case applications.StatusInterviewing:
			result.Counts.Interviewing++
		case applications.StatusOffer:
			result.Counts.Offer++
		case applications.StatusAccepted:
			result.Counts.Accepted++
		case applications.StatusRejected:
			result.Counts.Rejected++
		case applications.StatusWithdrawn:
			result.Counts.Withdrawn++
		}
	}
	if query.Status != "" {
		filtered := make([]testRecord, 0, len(matched))
		for _, record := range matched {
			if record.Status == query.Status {
				filtered = append(filtered, record)
			}
		}
		matched = filtered
	}
	result.Total = len(matched)

	// Keyset predicate: strictly after (created_at, id) in descending order.
	page := make([]testRecord, 0, len(matched))
	for _, record := range matched {
		if query.After != nil {
			if record.createdAt.After(query.After.CreatedAt) {
				continue
			}
			if record.createdAt.Equal(query.After.CreatedAt) && record.ID >= query.After.ID {
				continue
			}
		}
		page = append(page, record)
	}
	if len(page) > query.PageSize {
		last := page[query.PageSize-1]
		result.NextCursor = applications.EncodeCursor(query, last.createdAt, last.ID)
		page = page[:query.PageSize]
	}
	for _, record := range page {
		result.Items = append(result.Items, record.Application)
	}
	return result, nil
}

func containsFold(value, search string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(search))
}

func (s *testStore) Get(_ context.Context, userID, id string) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	for _, record := range s.records[userID] {
		if record.ID == id {
			return record.Application, nil
		}
	}
	return applications.Application{}, nil
}
func (s *testStore) Patch(_ context.Context, userID, id string, input applications.PatchInput) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	for i, record := range s.records[userID] {
		if record.ID == id {
			app := record.Application
			if input.Company != nil {
				app.Company = *input.Company
			}
			if input.Role != nil {
				app.Role = *input.Role
			}
			if input.Status != nil {
				app.Status = *input.Status
			}
			if input.Source != nil {
				app.Source = *input.Source
			}
			if input.Address != nil {
				app.Address = *input.Address
			}
			if input.URL != nil {
				app.URL = *input.URL
			}
			if input.Channel != nil {
				app.Channel = *input.Channel
			}
			if input.JobID != nil {
				app.JobID = *input.JobID
			}
			if input.AppliedOn != nil {
				app.AppliedOn = *input.AppliedOn
			}
			if input.Notes != nil {
				app.Notes = *input.Notes
			}
			s.records[userID][i].Application = app
			if input.Status != nil && *input.Status != record.Status {
				var note string
				if input.StageNote != nil {
					note = *input.StageNote
				}
				s.appendEvent(userID, id, applications.Event{
					EventType:  applications.EventStageChanged,
					FromStatus: record.Status,
					ToStatus:   *input.Status,
					Note:       note,
				})
			}
			return app, nil
		}
	}
	return applications.Application{}, nil
}
func (s *testStore) Events(_ context.Context, userID, id string, query applications.EventQuery) (applications.EventListResult, error) {
	if s.err != nil {
		return applications.EventListResult{}, s.err
	}
	owned := false
	for _, record := range s.records[userID] {
		if record.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		return applications.EventListResult{}, applications.ErrApplicationNotFound
	}
	events := append([]testEventRecord(nil), s.events[id]...)
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].occurredAt.Equal(events[j].occurredAt) {
			return events[i].occurredAt.After(events[j].occurredAt)
		}
		return events[i].ID > events[j].ID
	})
	result := applications.EventListResult{PageSize: query.PageSize, Items: []applications.Event{}}
	page := make([]testEventRecord, 0, len(events))
	for _, event := range events {
		if query.After != nil {
			if event.occurredAt.After(query.After.OccurredAt) {
				continue
			}
			if event.occurredAt.Equal(query.After.OccurredAt) && event.ID >= query.After.ID {
				continue
			}
		}
		page = append(page, event)
	}
	if len(page) > query.PageSize {
		last := page[query.PageSize-1]
		result.NextCursor = applications.EncodeEventCursor(query, last.occurredAt, last.ID)
		page = page[:query.PageSize]
	}
	for _, event := range page {
		result.Items = append(result.Items, event.Event)
	}
	return result, nil
}

func (s *testStore) Delete(_ context.Context, userID, id string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	filtered := make([]testRecord, 0, len(s.records[userID]))
	found := false
	for _, record := range s.records[userID] {
		if record.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, record)
	}
	s.records[userID] = filtered
	return found, nil
}

// clerkStore builds a signed handler around an empty test store.
func clerkStore(t *testing.T, bindings map[string]string) (*testStore, http.Handler, string) {
	t.Helper()
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := newTestStore()
	return store, NewHandler(verifier, testMapper{bindings: bindings}, store), token
}

// clerkStoreTwoOwners builds a signed handler with two distinct owners so the
// history route's isolation can be exercised at the HTTP boundary.
func clerkStoreTwoOwners(t *testing.T) (*testStore, http.Handler, string, string) {
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
	store := newTestStore()
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{
		"clerk-a": "owner-a", "clerk-b": "owner-b",
	}}, store)
	return store, handler, tokenA, tokenB
}

func TestApplicationStageAndEventHistoryHTTP(t *testing.T) {
	_, handler, tokenA, tokenB := clerkStoreTwoOwners(t)
	request := func(token, method, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(tokenA, http.MethodPost, `{"company":"Acme","role":"Engineer","status":"saved"}`, "/api/applications"); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	const id = "00000000-0000-0000-0000-000000000001"
	const path = "/api/applications/" + id + "/events"

	type eventPage struct {
		Items      []applications.Event `json:"items"`
		PageSize   int                  `json:"page_size"`
		NextCursor *string              `json:"next_cursor"`
	}
	events := func(token, path string) eventPage {
		t.Helper()
		w := request(token, http.MethodGet, "", path)
		if w.Code != http.StatusOK {
			t.Fatalf("events %s: %d %s", path, w.Code, w.Body.String())
		}
		var page eventPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode events: %v %s", err, w.Body.String())
		}
		return page
	}

	created := events(tokenA, path)
	if len(created.Items) != 1 || created.Items[0].EventType != applications.EventCreated ||
		created.Items[0].FromStatus != "" || created.Items[0].ToStatus != applications.StatusSaved ||
		created.Items[0].ApplicationID != id || created.Items[0].OccurredAt == "" {
		t.Fatalf("creation event = %+v", created.Items)
	}

	note := "Recruiter call scheduled"
	if w := request(tokenA, http.MethodPatch, `{"status":"interviewing","stage_note":"Recruiter call scheduled"}`, "/api/applications/"+id); w.Code != http.StatusOK {
		t.Fatalf("stage change: %d %s", w.Code, w.Body.String())
	}
	changed := events(tokenA, path)
	if len(changed.Items) != 2 || changed.Items[0].EventType != applications.EventStageChanged ||
		changed.Items[0].FromStatus != applications.StatusSaved || changed.Items[0].ToStatus != applications.StatusInterviewing ||
		changed.Items[0].Note != note {
		t.Fatalf("stage event = %+v", changed.Items)
	}

	// An edit that leaves the stage alone must not add a duplicate event.
	if w := request(tokenA, http.MethodPatch, `{"status":"interviewing"}`, "/api/applications/"+id); w.Code != http.StatusOK {
		t.Fatalf("unchanged stage patch: %d", w.Code)
	}
	if got := events(tokenA, path); len(got.Items) != 2 {
		t.Fatalf("unchanged stage added an event: %+v", got.Items)
	}

	// Terminal outcomes and corrections use the same event path.
	if w := request(tokenA, http.MethodPatch, `{"status":"offer"}`, "/api/applications/"+id); w.Code != http.StatusOK {
		t.Fatalf("offer stage: %d", w.Code)
	}
	if w := request(tokenA, http.MethodPatch, `{"status":"accepted"}`, "/api/applications/"+id); w.Code != http.StatusOK {
		t.Fatalf("accepted stage: %d", w.Code)
	}
	if got := events(tokenA, path); len(got.Items) != 4 || got.Items[0].ToStatus != applications.StatusAccepted {
		t.Fatalf("history after two more stages = %+v", got.Items)
	}

	for name, body := range map[string]string{
		"unknown stage":      `{"status":"in-review"}`,
		"overlong note":      `{"status":"offer","stage_note":"` + strings.Repeat("x", 501) + `"}`,
		"note without stage": `{"stage_note":"hello"}`,
	} {
		if w := request(tokenA, http.MethodPatch, body, "/api/applications/"+id); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}

	// Page through history with a bounded cursor: no repeats, newest first.
	firstPage := events(tokenA, path+"?page_size=1")
	if len(firstPage.Items) != 1 || firstPage.NextCursor == nil {
		t.Fatalf("first history page = %+v", firstPage)
	}
	seen := map[string]bool{firstPage.Items[0].ID: true}
	secondPage := events(tokenA, path+"?page_size=1&cursor="+*firstPage.NextCursor)
	if len(secondPage.Items) != 1 || seen[secondPage.Items[0].ID] {
		t.Fatalf("second history page = %+v", secondPage)
	}

	// Ownership and cursor binding are enforced at the route.
	if w := request(tokenB, http.MethodGet, "", path); w.Code != http.StatusNotFound {
		t.Fatalf("other owner's history = %d, want 404", w.Code)
	}
	if w := request(tokenA, http.MethodGet, "", "/api/applications/not-a-uuid/events"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid application id = %d, want 400", w.Code)
	}
	if w := request(tokenA, http.MethodGet, "", path+"?cursor=abc"); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed cursor = %d, want 400", w.Code)
	}
	if w := request(tokenA, http.MethodGet, "", path+"?cursor="+url.QueryEscape(*firstPage.NextCursor)+"&page_size=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid page size = %d, want 400", w.Code)
	}

	// A cursor minted for one application cannot be reused for another.
	if w := request(tokenA, http.MethodPost, `{"company":"Globex","role":"Designer","status":"saved"}`, "/api/applications"); w.Code != http.StatusCreated {
		t.Fatalf("second create: %d", w.Code)
	}
	otherPath := "/api/applications/00000000-0000-0000-0000-000000000002/events"
	if w := request(tokenA, http.MethodGet, "", otherPath+"?cursor="+url.QueryEscape(*firstPage.NextCursor)); w.Code != http.StatusBadRequest {
		t.Fatalf("cross-application cursor = %d, want 400", w.Code)
	}
}

func TestApplicationCountsIncludeEveryStageAndStatusFilter(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(method, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, status := range []string{"saved", "applied", "interviewing", "offer", "accepted", "rejected", "withdrawn"} {
		body := fmt.Sprintf(`{"company":"Acme","role":"%s","status":"%s"}`, status, status)
		if w := request(http.MethodPost, body, "/api/applications"); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", status, w.Code, w.Body.String())
		}
	}
	type page struct {
		Total  int `json:"total"`
		Counts struct {
			Saved        int `json:"saved"`
			Applied      int `json:"applied"`
			Interviewing int `json:"interviewing"`
			Offer        int `json:"offer"`
			Accepted     int `json:"accepted"`
			Rejected     int `json:"rejected"`
			Withdrawn    int `json:"withdrawn"`
		} `json:"counts"`
	}
	decode := func(path string) page {
		t.Helper()
		w := request(http.MethodGet, "", path)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	all := decode("/api/applications?page_size=50")
	if all.Total != 7 || all.Counts.Saved != 1 || all.Counts.Applied != 1 || all.Counts.Interviewing != 1 ||
		all.Counts.Offer != 1 || all.Counts.Accepted != 1 || all.Counts.Rejected != 1 || all.Counts.Withdrawn != 1 {
		t.Fatalf("per-stage counts = %+v", all)
	}
	filtered := decode("/api/applications?status=interviewing")
	if filtered.Total != 1 || filtered.Counts != all.Counts {
		t.Fatalf("status filter = %+v, want total 1 and unchanged counts", filtered)
	}
}

func TestApplicationsRequireClerkAndEnforceOwnerIsolation(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokenA := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	tokenB := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-b"}, "kid")
	expired := signTestRS256WithKey(t, key, now.Add(-2*time.Hour), map[string]any{"sub": "clerk-a"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := newTestStore()
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a", "clerk-b": "owner-b"}}, store)
	request := func(method, token, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	const path = "/api/applications/00000000-0000-0000-0000-000000000001"
	for _, token := range []string{"", "invalid", expired} {
		if w := request("GET", token, "", path); w.Code != http.StatusUnauthorized {
			t.Fatalf("GET with invalid token status = %d, want 401", w.Code)
		}
	}
	if w := request("POST", tokenA, `{"company":"Acme","role":"Engineer","status":"saved"}`, "/api/applications"); w.Code != http.StatusCreated {
		t.Fatalf("create owner application: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"company":"","role":"Engineer","status":"saved"}`, `{"company":"Acme","role":"Engineer","status":"saved","user_id":"owner-b"}`} {
		if w := request("POST", tokenA, body, "/api/applications"); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid create %s: %d", body, w.Code)
		}
	}
	for _, tc := range []struct {
		token string
		count int
	}{{tokenA, 1}, {tokenB, 0}} {
		w := request("GET", tc.token, "", "/api/applications")
		var page struct {
			Items []applications.Application `json:"items"`
			Total int                        `json:"total"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != tc.count || page.Total != tc.count {
			t.Fatalf("owner list: %d %s, want %d rows", w.Code, w.Body.String(), tc.count)
		}
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		body := ""
		if method == "PATCH" {
			body = `{"status":"applied"}`
		}
		if w := request(method, tokenB, body, path); w.Code != http.StatusNotFound {
			t.Fatalf("%s other user's application: %d %s, want 404", method, w.Code, w.Body.String())
		}
	}
	if w := request("PATCH", tokenA, `{}`, path); w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d, want 400", w.Code)
	}
	if w := request("GET", tokenA, "", path); w.Code != http.StatusOK {
		t.Fatalf("owner reads application: %d %s", w.Code, w.Body.String())
	}
	if w := request("PATCH", tokenA, `{"status":"applied"}`, path); w.Code != http.StatusOK {
		t.Fatalf("owner updates application: %d %s", w.Code, w.Body.String())
	}
	if w := request("DELETE", tokenA, "", path); w.Code != http.StatusNoContent {
		t.Fatalf("owner deletes application: %d %s", w.Code, w.Body.String())
	}
	store.err = errors.New("private database error")
	if w := request("GET", tokenA, "", "/api/applications"); w.Code != http.StatusServiceUnavailable || bytes.Contains(w.Body.Bytes(), []byte("private")) {
		t.Fatalf("database failure: %d %s", w.Code, w.Body.String())
	}
}

func TestApplicationSourceAndAddressRoundTrip(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(method, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if w := request("POST", `{"company":"Acme","role":"Engineer","status":"applied","source":"Referral","address":"me@example.com"}`, "/api/applications"); w.Code != http.StatusCreated {
		t.Fatalf("create with optional fields: %d %s", w.Code, w.Body.String())
	}
	w := request("GET", "", "/api/applications")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var page struct {
		Items []applications.Application `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("list decode: %v %s", err, w.Body.String())
	}
	created := page.Items[0]
	if created.Source != "Referral" || created.Address != "me@example.com" {
		t.Fatalf("stored optional fields = %q/%q", created.Source, created.Address)
	}
	const path = "/api/applications/00000000-0000-0000-0000-000000000001"
	// Optional fields can be cleared with an explicit empty string.
	if w := request("PATCH", `{"source":"","address":""}`, path); w.Code != http.StatusOK {
		t.Fatalf("patch to clear: %d %s", w.Code, w.Body.String())
	}
	var updated applications.Application
	if err := json.Unmarshal(request("GET", "", path).Body.Bytes(), &updated); err != nil || updated.Source != "" || updated.Address != "" {
		t.Fatalf("cleared fields = %q/%q (%v)", updated.Source, updated.Address, err)
	}
	// Overlong optional input is rejected at the API boundary.
	if w := request("PATCH", `{"source":"`+strings.Repeat("x", 201)+`"}`, path); w.Code != http.StatusBadRequest {
		t.Fatalf("overlong source: %d, want 400", w.Code)
	}
}

// TestApplicationManualFieldsRoundTrip covers the Phase 2 capture fields that
// the product plan's manual contract requires beyond company and role.
func TestApplicationManualFieldsRoundTrip(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(method, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	created := `{"company":"Acme","role":"Engineer","status":"applied","url":"https://jobs.example.com/req-42","channel":"campus","job_id":"REQ-42","applied_on":"2026-11-02","notes":"Placement cell notice."}`
	if w := request("POST", created, "/api/applications"); w.Code != http.StatusCreated {
		t.Fatalf("create with manual fields: %d %s", w.Code, w.Body.String())
	}
	var record applications.Application
	if err := json.Unmarshal(request("GET", "", "/api/applications/00000000-0000-0000-0000-000000000001").Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.URL != "https://jobs.example.com/req-42" || record.Channel != "campus" || record.JobID != "REQ-42" || record.AppliedOn != "2026-11-02" || record.Notes != "Placement cell notice." {
		t.Fatalf("manual fields = %+v", record)
	}

	const path = "/api/applications/00000000-0000-0000-0000-000000000001"
	// Clearing every optional field with an explicit empty string is a valid
	// edit and must not require a full record.
	if w := request("PATCH", `{"url":"","channel":"","job_id":"","applied_on":"","notes":""}`, path); w.Code != http.StatusOK {
		t.Fatalf("clear manual fields: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(request("GET", "", path).Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.URL != "" || record.Channel != "" || record.JobID != "" || record.AppliedOn != "" || record.Notes != "" {
		t.Fatalf("cleared manual fields = %+v", record)
	}

	for name, patch := range map[string]string{
		"non-http url":     `{"url":"javascript:alert(1)"}`,
		"relative url":     `{"url":"/jobs/42"}`,
		"unknown channel":  `{"channel":"remote"}`,
		"impossible date":  `{"applied_on":"2026-02-30"}`,
		"date with a time": `{"applied_on":"2026-11-02T09:00:00Z"}`,
		"overlong notes":   `{"notes":"` + strings.Repeat("x", 2001) + `"}`,
		"overlong job id":  `{"job_id":"` + strings.Repeat("x", 201) + `"}`,
	} {
		if w := request("PATCH", patch, path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
}

// TestApplicationListIsPagedFilteredAndBounded covers the bounded keyset
// pagination and filtering contract: honest totals, no repeated rows, an empty
// page past the end, and rejection of malformed query parameters and cursors.
func TestApplicationListIsPagedFilteredAndBounded(t *testing.T) {
	store, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	post := func(body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("seed create: %d %s", w.Code, w.Body.String())
		}
	}
	post(`{"company":"Acme","role":"Backend Engineer","status":"applied","channel":"campus","job_id":"REQ-1"}`)
	post(`{"company":"Acme","role":"Product Designer","status":"saved","channel":"off-campus","job_id":"REQ-2"}`)
	post(`{"company":"Globex","role":"Engineer","status":"saved","channel":"off-campus"}`)

	type page struct {
		Items      []applications.Application `json:"items"`
		PageSize   int                        `json:"page_size"`
		Total      int                        `json:"total"`
		NextCursor *string                    `json:"next_cursor"`
		Counts     struct {
			Saved   int `json:"saved"`
			Applied int `json:"applied"`
		} `json:"counts"`
	}
	decode := func(w *httptest.ResponseRecorder) page {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v %s", err, w.Body.String())
		}
		return p
	}

	first := decode(request("/api/applications?page_size=2"))
	if first.Total != 3 || first.PageSize != 2 || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	if first.Counts.Saved != 2 || first.Counts.Applied != 1 {
		t.Fatalf("counts = %+v", first.Counts)
	}
	second := decode(request("/api/applications?page_size=2&cursor=" + *first.NextCursor))
	if len(second.Items) != 1 || second.Items[0].Company != "Acme" || second.Items[0].Role != "Backend Engineer" {
		t.Fatalf("second page = %+v", second)
	}
	if second.NextCursor != nil {
		t.Fatalf("last page must not offer a next cursor: %q", *second.NextCursor)
	}
	// Page order is stable: no row repeats and none is skipped across pages.
	seen := map[string]bool{}
	for _, app := range append(first.Items, second.Items...) {
		if seen[app.ID] {
			t.Fatalf("row %s repeated across pages", app.ID)
		}
		seen[app.ID] = true
	}

	// A cursor at the last row returns an empty page but still reports the true
	// total, and offers no next cursor.
	atEnd := store.cursorAt("owner-a", "00000000-0000-0000-0000-000000000001")
	past := decode(request("/api/applications?page_size=2&cursor=" + atEnd))
	if len(past.Items) != 0 || past.Total != 3 || past.NextCursor != nil {
		t.Fatalf("cursor at the end = %+v", past)
	}

	// Search and filters narrow the set without lying about the count.
	searched := decode(request("/api/applications?q=acme&page_size=50"))
	if searched.Total != 2 || len(searched.Items) != 2 {
		t.Fatalf("search = %+v", searched)
	}
	campus := decode(request("/api/applications?channel=campus"))
	if campus.Total != 1 || campus.Items[0].Channel != "campus" {
		t.Fatalf("channel filter = %+v", campus)
	}
	applied := decode(request("/api/applications?status=applied"))
	if applied.Total != 1 || applied.Items[0].Status != "applied" {
		t.Fatalf("status filter = %+v", applied)
	}
	// Status counts describe the whole filtered set, not the page.
	if applied.Counts.Saved != 2 || applied.Counts.Applied != 1 {
		t.Fatalf("counts under status filter = %+v", applied.Counts)
	}
	empty := decode(request("/api/applications?q=nothing-matches"))
	if empty.Total != 0 || len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("no results = %+v", empty)
	}

	// A cursor minted for the unfiltered query must not be accepted when the
	// caller changes the filters: that would silently skip or repeat rows.
	if w := request("/api/applications?q=acme&cursor=" + *first.NextCursor); w.Code != http.StatusBadRequest {
		t.Errorf("cursor with different filters: status = %d, want 400", w.Code)
	}
	for name, path := range map[string]string{
		"page is gone":      "/api/applications?page=0",
		"negative page":     "/api/applications?page=-1",
		"non-numeric page":  "/api/applications?page=abc",
		"empty cursor":      "/api/applications?cursor=",
		"malformed cursor":  "/api/applications?cursor=abc",
		"oversized cursor":  "/api/applications?cursor=" + strings.Repeat("a", 1025),
		"zero page size":    "/api/applications?page_size=0",
		"oversized page":    "/api/applications?page_size=51",
		"fractional page":   "/api/applications?page_size=1.5",
		"unknown status":    "/api/applications?status=archived",
		"unknown channel":   "/api/applications?channel=remote",
		"unknown parameter": "/api/applications?offset=1",
		"overlong search":   "/api/applications?q=" + strings.Repeat("x", 101),
	} {
		if w := request(path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

// TestApplicationListCursorIsStableAcrossWrites is the HTTP-level regression
// for Phase 2: rows inserted or deleted between two page requests must not
// repeat or skip a row. A new row ahead of the cursor stays off later pages,
// and deleting the row the cursor points at leaves the walk intact.
func TestApplicationListCursorIsStableAcrossWrites(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	post := func(company, role string) {
		t.Helper()
		body := `{"company":"` + company + `","role":"` + role + `","status":"saved"}`
		if w := request(http.MethodPost, "/api/applications", body); w.Code != http.StatusCreated {
			t.Fatalf("seed create: %d %s", w.Code, w.Body.String())
		}
	}
	post("Acme", "Role 1")
	post("Acme", "Role 2")
	post("Acme", "Role 3")

	type page struct {
		Items      []applications.Application `json:"items"`
		Total      int                        `json:"total"`
		NextCursor *string                    `json:"next_cursor"`
	}
	decode := func(w *httptest.ResponseRecorder) page {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v %s", err, w.Body.String())
		}
		return p
	}

	first := decode(request(http.MethodGet, "/api/applications?page_size=2", ""))
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	cursor := *first.NextCursor
	// The last row on page one is the cursor row; deleting it must not break the
	// next request.
	cursorRole := first.Items[1].Role

	// A newer insert ahead of the cursor must not shift page two.
	post("Globex", "Newest")
	second := decode(request(http.MethodGet, "/api/applications?page_size=2&cursor="+cursor, ""))
	if len(second.Items) != 1 || second.Items[0].Role != "Role 1" {
		t.Fatalf("page two after an insert = %+v, want Role 1", second)
	}

	// Deleting the cursor row (already seen) and a row ahead of the cursor
	// leaves page two positioned the same way.
	var cursorID string
	for _, app := range first.Items {
		if app.Role == cursorRole {
			cursorID = app.ID
		}
	}
	if cursorID == "" {
		t.Fatalf("cursor row %q not on the first page", cursorRole)
	}
	if w := request(http.MethodDelete, "/api/applications/"+cursorID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete cursor row: %d %s", w.Code, w.Body.String())
	}
	afterDelete := decode(request(http.MethodGet, "/api/applications?page_size=2&cursor="+cursor, ""))
	if len(afterDelete.Items) != 1 || afterDelete.Items[0].Role != "Role 1" {
		t.Fatalf("page two after deleting the cursor row = %+v, want Role 1", afterDelete)
	}
}

// TestApplicationsAtSameCompanyStayDistinct pins the product plan rule that
// same-company roles are separate records and can be edited independently.
func TestApplicationsAtSameCompanyStayDistinct(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
	request := func(method, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	for _, role := range []string{"Engineer", "Designer"} {
		if w := request("POST", `{"company":"Acme","role":"`+role+`","status":"saved"}`, "/api/applications"); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", role, w.Code, w.Body.String())
		}
	}
	var page struct {
		Items []applications.Application `json:"items"`
	}
	if err := json.Unmarshal(request("GET", "", "/api/applications").Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("same-company records: %v %+v", err, page.Items)
	}
	const engineerPath = "/api/applications/00000000-0000-0000-0000-000000000001"
	if w := request("PATCH", `{"status":"applied","job_id":"REQ-9"}`, engineerPath); w.Code != http.StatusOK {
		t.Fatalf("edit one role: %d %s", w.Code, w.Body.String())
	}
	var updated applications.Application
	if err := json.Unmarshal(request("GET", "", engineerPath).Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Role != "Engineer" || updated.Status != "applied" || updated.JobID != "REQ-9" {
		t.Fatalf("edited record = %+v", updated)
	}
	// The other record at the same company is untouched.
	var untouched applications.Application
	if err := json.Unmarshal(request("GET", "", "/api/applications/00000000-0000-0000-0000-000000000002").Body.Bytes(), &untouched); err != nil {
		t.Fatal(err)
	}
	if untouched.Role != "Designer" || untouched.Status != "saved" || untouched.JobID != "" {
		t.Fatalf("untouched record = %+v", untouched)
	}
}

// TestApplicationListPageIsOwnerScoped makes sure keyset pagination cannot leak
// rows from another account: a cursor minted for one owner returns only that
// owner's rows, and the same position under another owner returns only theirs.
func TestApplicationListPageIsOwnerScoped(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokenA := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	tokenB := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-b"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := newTestStore()
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a", "clerk-b": "owner-b"}}, store)
	request := func(token, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	post := func(token, role string) {
		t.Helper()
		body := fmt.Sprintf(`{"company":"Acme","role":"%s","status":"saved"}`, role)
		r := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("seed create %s: %d %s", role, w.Code, w.Body.String())
		}
	}

	// B's row is older than A's rows, so a cursor from A lands after it: B
	// still sees exactly one row, its own.
	post(tokenB, "B Only")
	for i := 0; i < 3; i++ {
		post(tokenA, fmt.Sprintf("A Role %d", i))
	}

	type page struct {
		Items      []applications.Application `json:"items"`
		Total      int                        `json:"total"`
		NextCursor *string                    `json:"next_cursor"`
	}
	var first page
	if err := json.Unmarshal(request(tokenA, "/api/applications?page_size=2").Body.Bytes(), &first); err != nil || len(first.Items) != 2 || first.Total != 3 || first.NextCursor == nil {
		t.Fatalf("owner A page: %v %+v", err, first)
	}
	cursor := *first.NextCursor

	var other page
	if err := json.Unmarshal(request(tokenB, "/api/applications?page_size=2&cursor="+cursor).Body.Bytes(), &other); err != nil {
		t.Fatal(err)
	}
	if other.Total != 1 || len(other.Items) != 1 || other.Items[0].Role != "B Only" {
		t.Fatalf("owner B with owner A's cursor = %+v", other)
	}

	var rest page
	if err := json.Unmarshal(request(tokenA, "/api/applications?page_size=2&cursor="+cursor).Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if rest.Total != 3 || len(rest.Items) != 1 || rest.Items[0].Role != "A Role 0" {
		t.Fatalf("owner A with its own cursor = %+v", rest)
	}
}

func TestApplicationHTTPRejectsMalformedAndOversizedRequests(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := signTestRS256WithKey(t, key, now, map[string]any{"sub": "clerk-a"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := newTestStore()
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a"}}, store)
	request := func(auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	for name, auth := range map[string]string{"missing": "", "wrong scheme": "Basic " + token, "missing token": "Bearer", "extra token": "Bearer " + token + " extra", "oversized token": "Bearer " + strings.Repeat("x", 8193)} {
		if w := request(auth, `{"company":"Acme","role":"Engineer","status":"saved"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s Authorization: status = %d, want 401", name, w.Code)
		}
	}
	for name, body := range map[string]string{
		"malformed JSON":    `{"company":`,
		"trailing JSON":     `{"company":"Acme","role":"Engineer","status":"saved"}{}`,
		"unknown field":     `{"company":"Acme","role":"Engineer","status":"saved","owner":"other"}`,
		"NUL in company":    `{"company":"Acme\u0000","role":"Engineer","status":"saved"}`,
		"NUL in role":       `{"company":"Acme","role":"Eng\u0000ineer","status":"saved"}`,
		"NUL in notes":      `{"company":"Acme","role":"Engineer","status":"saved","notes":"n\u0000"}`,
		"oversized payload": `{"company":"` + strings.Repeat("x", 16<<10) + `","role":"Engineer","status":"saved"}`,
	} {
		if w := request("Bearer "+token, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
	if len(store.records["owner-a"]) != 0 {
		t.Fatalf("invalid requests created records: %+v", store.records["owner-a"])
	}
}
