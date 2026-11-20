package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
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

func (s *testStore) Create(_ context.Context, userID string, input applications.Input) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	a := applications.Application{ID: "00000000-0000-0000-0000-000000000001", Company: input.Company, Role: input.Role, Status: input.Status, Source: input.Source, Address: input.Address}
	s.records[userID] = append(s.records[userID], a)
	return a, nil
}
func (s *testStore) List(_ context.Context, userID string) ([]applications.Application, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.records[userID], nil
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
		var items []applications.Application
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != tc.count {
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
	var created applications.Application
	w := request("GET", "", "/api/applications")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var list []applications.Application
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list decode: %v %s", err, w.Body.String())
	}
	created = list[0]
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
