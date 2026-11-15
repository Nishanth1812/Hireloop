package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler("", "", nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Status != "ok" {
		t.Fatalf("body = %q, decode error = %v", response.Body.String(), err)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodGet, "/api/v1/me", http.StatusNotFound},
		{http.MethodPost, "/health", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewHandler("", "", nil, nil).ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
}

func TestCurrentUser(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/user" || r.Header.Get("apikey") != "public-key" {
			t.Errorf("unexpected provider request: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"user-123","email":"person@example.com","extra":"private"}`))
	}))
	defer provider.Close()
	handler := NewHandler(provider.URL, "public-key", provider.Client(), nil)
	for _, tc := range []struct {
		name, auth string
		want       int
		body       string
	}{
		{"missing", "", http.StatusUnauthorized, ""},
		{"malformed", "Basic abc", http.StatusUnauthorized, ""},
		{"invalid", "Bearer bad-token", http.StatusUnauthorized, ""},
		{"valid", "Bearer valid-token", http.StatusOK, `"id":"user-123"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.want || (tc.body != "" && !strings.Contains(res.Body.String(), tc.body)) || strings.Contains(res.Body.String(), "private") {
				t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestCurrentUserFailsClosedWhenUnconfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	NewHandler("", "", nil, nil).ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.Code)
	}
}

func TestCurrentUserFailsClosedOnProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable", 500, `private provider detail`},
		{"invalid body", 200, `{}`},
		{"redirect", 302, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.com")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer provider.Close()
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.Header.Set("Authorization", "Bearer token")
			res := httptest.NewRecorder()
			NewHandler(provider.URL, "public-key", provider.Client(), nil).ServeHTTP(res, req)
			if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), "private") {
				t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
			}
		})
	}
}
