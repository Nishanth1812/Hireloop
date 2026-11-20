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
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
)

type testStore struct {
	records map[string][]applications.Application
	seq     int
	err     error
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
	s.records[userID] = append(s.records[userID], a)
	return a, nil
}

// List models the Postgres store's contract: newest first, filters applied
// before pagination, counts over the same filters except status. The real SQL
// is not exercised here; no test connects to a database.
func (s *testStore) List(_ context.Context, userID string, query applications.Query) (applications.ListResult, error) {
	if s.err != nil {
		return applications.ListResult{}, s.err
	}
	// Counts ignore the status filter so they describe the whole filtered set.
	base := []applications.Application{}
	for i := len(s.records[userID]) - 1; i >= 0; i-- {
		app := s.records[userID][i]
		if query.Channel != "" && app.Channel != query.Channel {
			continue
		}
		if query.Search != "" && !containsFold(app.Company, query.Search) && !containsFold(app.Role, query.Search) && !containsFold(app.JobID, query.Search) {
			continue
		}
		base = append(base, app)
	}
	result := applications.ListResult{Page: query.Page, PageSize: query.PageSize, Items: []applications.Application{}}
	for _, app := range base {
		if app.Status == "saved" {
			result.Counts.Saved++
		} else {
			result.Counts.Applied++
		}
	}
	matches := []applications.Application{}
	if query.Status != "" {
		for _, app := range base {
			if app.Status == query.Status {
				matches = append(matches, app)
			}
		}
	} else {
		matches = base
	}
	result.Total = len(matches)
	result.TotalPages = (result.Total + query.PageSize - 1) / query.PageSize
	start := (query.Page - 1) * query.PageSize
	if start < len(matches) {
		end := start + query.PageSize
		if end > len(matches) {
			end = len(matches)
		}
		result.Items = append(result.Items, matches[start:end]...)
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
	for _, app := range s.records[userID] {
		if app.ID == id {
			return app, nil
		}
	}
	return applications.Application{}, nil
}
func (s *testStore) Patch(_ context.Context, userID, id string, input applications.PatchInput) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	for i, app := range s.records[userID] {
		if app.ID == id {
			if input.Company != nil {
				s.records[userID][i].Company = *input.Company
			}
			if input.Role != nil {
				s.records[userID][i].Role = *input.Role
			}
			if input.Status != nil {
				s.records[userID][i].Status = *input.Status
			}
			if input.Source != nil {
				s.records[userID][i].Source = *input.Source
			}
			if input.Address != nil {
				s.records[userID][i].Address = *input.Address
			}
			if input.URL != nil {
				s.records[userID][i].URL = *input.URL
			}
			if input.Channel != nil {
				s.records[userID][i].Channel = *input.Channel
			}
			if input.JobID != nil {
				s.records[userID][i].JobID = *input.JobID
			}
			if input.AppliedOn != nil {
				s.records[userID][i].AppliedOn = *input.AppliedOn
			}
			if input.Notes != nil {
				s.records[userID][i].Notes = *input.Notes
			}
			return s.records[userID][i], nil
		}
	}
	return applications.Application{}, nil
}
func (s *testStore) Delete(_ context.Context, userID, id string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	var filtered []applications.Application
	found := false
	for _, app := range s.records[userID] {
		if app.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, app)
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
	store := &testStore{records: make(map[string][]applications.Application)}
	return store, NewHandler(verifier, testMapper{bindings: bindings}, store), token
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
	store := &testStore{records: make(map[string][]applications.Application)}
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

// TestApplicationListIsPagedFilteredAndBounded covers the bounded, stable
// pagination and filtering contract, including honest totals on out-of-range
// pages and rejection of malformed query parameters.
func TestApplicationListIsPagedFilteredAndBounded(t *testing.T) {
	_, handler, token := clerkStore(t, map[string]string{"clerk-a": "owner-a"})
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
		Page       int                        `json:"page"`
		PageSize   int                        `json:"page_size"`
		Total      int                        `json:"total"`
		TotalPages int                        `json:"total_pages"`
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

	first := decode(request("/api/applications?page=1&page_size=2"))
	if first.Total != 3 || first.Page != 1 || first.PageSize != 2 || first.TotalPages != 2 || len(first.Items) != 2 {
		t.Fatalf("first page = %+v", first)
	}
	if first.Counts.Saved != 2 || first.Counts.Applied != 1 {
		t.Fatalf("counts = %+v", first.Counts)
	}
	second := decode(request("/api/applications?page=2&page_size=2"))
	if len(second.Items) != 1 || second.Items[0].Company != "Acme" || second.Items[0].Role != "Backend Engineer" {
		t.Fatalf("second page = %+v", second)
	}
	// Page order is stable: no row repeats and none is skipped across pages.
	seen := map[string]bool{}
	for _, app := range append(first.Items, second.Items...) {
		if seen[app.ID] {
			t.Fatalf("row %s repeated across pages", app.ID)
		}
		seen[app.ID] = true
	}

	// An out-of-range page is empty but still reports the true total.
	past := decode(request("/api/applications?page=9&page_size=2"))
	if len(past.Items) != 0 || past.Total != 3 || past.TotalPages != 2 {
		t.Fatalf("page past the end = %+v", past)
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
	if empty.Total != 0 || len(empty.Items) != 0 || empty.TotalPages != 0 {
		t.Fatalf("no results = %+v", empty)
	}

	for _, path := range []string{
		"/api/applications?page=0",
		"/api/applications?page=-1",
		"/api/applications?page=abc",
		"/api/applications?page_size=0",
		"/api/applications?page_size=51",
		"/api/applications?page_size=1.5",
		"/api/applications?status=archived",
		"/api/applications?channel=remote",
		"/api/applications?unknown=1",
		"/api/applications?q=" + strings.Repeat("x", 101),
	} {
		if w := request(path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
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

// TestApplicationListPageIsOwnerScoped makes sure pagination cannot leak rows
// from another account, even when the other account has many records.
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
	store := &testStore{records: make(map[string][]applications.Application)}
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a", "clerk-b": "owner-b"}}, store)
	request := func(token, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"company":"Acme","role":"Engineer %d","status":"saved"}`, i)
		r := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("seed create: %d %s", w.Code, w.Body.String())
		}
	}
	var page struct {
		Items []applications.Application `json:"items"`
		Total int                        `json:"total"`
	}
	if err := json.Unmarshal(request(tokenB, "/api/applications?page=1&page_size=2").Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("other account sees rows: %+v", page)
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
	store := &testStore{records: make(map[string][]applications.Application)}
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
