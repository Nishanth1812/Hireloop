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
	a := applications.Application{ID: "00000000-0000-0000-0000-000000000001", Company: input.Company, Role: input.Role, Status: input.Status}
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
	request := func(method, token, body, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	requestList := func(method, token, body string) *httptest.ResponseRecorder {
		return request(method, token, body, "/api/applications")
	}
	requestByID := func(method, token, body, id string) *httptest.ResponseRecorder {
		path := "/api/applications/"
		if id != "" {
			path += id
		}
		return request(method, token, body, path)
	}
	if w := requestList("POST", "", `{"company":"Acme","role":"Engineer","status":"saved"}`); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	for _, body := range []string{`{"company":"","role":"Engineer","status":"saved"}`, `{"company":"Acme","role":"Engineer","status":"applied","user_id":"user-b"}`, `{"company":"Acme","role":"Engineer","status":"unknown"}`, `{}`} {
		if w := requestList("POST", "user-a", body); w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
	}
	if w := requestList("POST", "user-a", `{"company":"Acme","role":"Engineer","status":"saved"}`); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		token string
		count int
	}{{"user-a", 1}, {"user-b", 0}} {
		w := requestList("GET", tc.token, "")
		var items []applications.Application
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != tc.count {
			t.Fatalf("list %s: %d %s", tc.token, w.Code, w.Body.String())
		}
	}
	w := requestList("GET", "user-a", "")
	var items []applications.Application
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 1 {
		t.Fatalf("list owner: %d %s", w.Code, w.Body.String())
	}
	id := items[0].ID

	if w := requestByID("GET", "", "", id); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated get: %d", w.Code)
	}
	w = requestByID("GET", "user-a", "", id)
	var item applications.Application
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.ID != id {
		t.Fatalf("get owner: %d %s", w.Code, w.Body.String())
	}
	if w := requestByID("GET", "user-b", "", id); w.Code != http.StatusNotFound {
		t.Fatalf("get other user: %d", w.Code)
	}
	for _, invalidID := range []string{"not-a-uuid", "gggggggg-gggg-gggg-gggg-gggggggggggg"} {
		if w := requestByID("GET", "user-a", "", invalidID); w.Code != http.StatusBadRequest {
			t.Fatalf("get malformed ID %q: %d", invalidID, w.Code)
		}
	}

	for _, body := range []string{`{}`, `{"company":" "}`, `{"company":"Acme","role":"Engineer","status":"unknown"}`, `{"company":"Acme","role":"Engineer","status":"applied","user_id":"user-b"}`} {
		if w := requestByID("PATCH", "user-a", body, id); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch %s: %d", body, w.Code)
		}
	}
	if w := requestByID("PATCH", "user-b", `{"company":"Other","role":"Role","status":"applied"}`, id); w.Code != http.StatusNotFound {
		t.Fatalf("patch other user: %d", w.Code)
	}
	if w := requestByID("PATCH", "user-a", `{"status":"applied"}`, "not-a-uuid"); w.Code != http.StatusBadRequest {
		t.Fatalf("patch malformed ID: %d", w.Code)
	}
	w = requestByID("PATCH", "user-a", `{"company":"Acme Ltd","role":"Senior Engineer","status":"applied"}`, id)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Company != "Acme Ltd" || item.Status != "applied" {
		t.Fatalf("patch owner: %d %s", w.Code, w.Body.String())
	}
	w = requestByID("PATCH", "user-a", `{"status":"saved"}`, id)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Company != "Acme Ltd" || item.Role != "Senior Engineer" || item.Status != "saved" {
		t.Fatalf("partial patch: %d %s", w.Code, w.Body.String())
	}

	if w := requestByID("DELETE", "user-b", "", id); w.Code != http.StatusNotFound {
		t.Fatalf("delete other user: %d", w.Code)
	}
	if w := requestByID("DELETE", "user-a", "", "not-a-uuid"); w.Code != http.StatusBadRequest {
		t.Fatalf("delete malformed ID: %d", w.Code)
	}
	if w := requestByID("DELETE", "user-a", "", id); w.Code != http.StatusNoContent {
		t.Fatalf("delete owner: %d %s", w.Code, w.Body.String())
	}
	if w := requestByID("GET", "user-a", "", id); w.Code != http.StatusNotFound {
		t.Fatalf("get deleted application: %d", w.Code)
	}
	if w := requestByID("DELETE", "user-a", "", id); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing application: %d", w.Code)
	}
	store.err = errors.New("secret database details")
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		w := requestList(method, "user-a", `{"company":"Acme","role":"Engineer","status":"saved"}`)
		if method == "PATCH" || method == "DELETE" {
			w = requestByID(method, "user-a", `{"company":"Acme","role":"Engineer","status":"saved"}`, id)
		}
		if w.Code != http.StatusServiceUnavailable || bytes.Contains(w.Body.Bytes(), []byte("secret")) {
			t.Fatalf("database failure: %d %s", w.Code, w.Body.String())
		}
	}
	store.err = nil
}
