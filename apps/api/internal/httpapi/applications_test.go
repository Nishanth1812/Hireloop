package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"hireloop/api/internal/applications"
)

type testStore struct {
	records map[string][]applications.Application
	err     error
}

func (s *testStore) Create(_ context.Context, userID string, input applications.Input) (applications.Application, error) {
	if s.err != nil {
		return applications.Application{}, s.err
	}
	a := applications.Application{ID: "app-1", Company: input.Company, Role: input.Role, Status: input.Status}
	s.records[userID] = append(s.records[userID], a)
	return a, nil
}
func (s *testStore) List(_ context.Context, userID string) ([]applications.Application, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.records[userID], nil
}

func TestApplications(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer user-a":
			_, _ = w.Write([]byte(`{"id":"user-a"}`))
		case "Bearer user-b":
			_, _ = w.Write([]byte(`{"id":"user-b"}`))
		default:
			http.Error(w, "invalid", http.StatusUnauthorized)
		}
	}))
	defer provider.Close()
	store := &testStore{records: make(map[string][]applications.Application)}
	h := NewHandler(provider.URL, "public-key", provider.Client(), store)
	request := func(method, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/applications", bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "", `{"company":"Acme","role":"Engineer","status":"saved"}`); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	for _, body := range []string{`{"company":"","role":"Engineer","status":"saved"}`, `{"company":"Acme","role":"Engineer","status":"applied","user_id":"user-b"}`, `{"company":"Acme","role":"Engineer","status":"unknown"}`, `{} {}`} {
		if w := request("POST", "user-a", body); w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
	}
	if w := request("POST", "user-a", `{"company":"Acme","role":"Engineer","status":"saved"}`); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		token string
		count int
	}{{"user-a", 1}, {"user-b", 0}} {
		w := request("GET", tc.token, "")
		var items []applications.Application
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != tc.count {
			t.Fatalf("list %s: %d %s", tc.token, w.Code, w.Body.String())
		}
	}
	store.err = errors.New("secret database details")
	for _, method := range []string{"GET", "POST"} {
		w := request(method, "user-a", `{"company":"Acme","role":"Engineer","status":"saved"}`)
		if w.Code != http.StatusServiceUnavailable || bytes.Contains(w.Body.Bytes(), []byte("secret")) {
			t.Fatalf("database failure: %d %s", w.Code, w.Body.String())
		}
	}
}
