package clerk

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const backendTestKey = "sk_test_clerk_backend_secret"

func newTestBackendClient(t *testing.T, handler http.HandlerFunc) *BackendClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewBackendClient(backendTestKey)
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	return client
}

func backendUserJSON(emails ...backendEmailAddress) []byte {
	body, err := json.Marshal(map[string]any{
		"id":                          "user_testuser",
		"primary_email_address_id":    "em_primary",
		"email_addresses":             emails,
	})
	if err != nil {
		panic(err)
	}
	return body
}

func verifiedEmail(id, email string) backendEmailAddress {
	var address backendEmailAddress
	address.ID = id
	address.EmailAddress = email
	address.IsPrimary = true
	address.Verification.Status = "verified"
	return address
}

func TestBackendClientValidatesConfig(t *testing.T) {
	if _, err := NewBackendClient(""); err == nil {
		t.Fatal("expected error for empty API key")
	}
	if _, err := NewBackendClient("  "); err == nil {
		t.Fatal("expected error for whitespace API key")
	}
	if _, err := NewBackendClient("sk_ok\r\nX-Evil: 1"); err == nil {
		t.Fatal("expected error for control characters in API key")
	}
	if _, err := NewBackendClient(strings.Repeat("k", 1025)); err == nil {
		t.Fatal("expected error for oversized API key")
	}
	client, err := NewBackendClient(backendTestKey)
	if err != nil {
		t.Fatal(err)
	}
	if client.baseURL != defaultBackendBaseURL {
		t.Fatalf("base URL = %q, want %q", client.baseURL, defaultBackendBaseURL)
	}
}

func TestVerifiedPrimaryEmailReturnsVerifiedPrimary(t *testing.T) {
	var path, auth string
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(backendUserJSON(verifiedEmail("em_primary", "student@example.com")))
	})
	email, err := client.VerifiedPrimaryEmail(context.Background(), "user_testuser")
	if err != nil || email != "student@example.com" {
		t.Fatalf("email = %q, %v", email, err)
	}
	if path != "/v1/users/user_testuser" {
		t.Fatalf("path = %q", path)
	}
	if auth != "Bearer "+backendTestKey {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestVerifiedPrimaryEmailRequiresSubject(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("client must reject an empty subject before calling Clerk")
	})
	if _, err := client.VerifiedPrimaryEmail(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty subject")
	}
}

func TestVerifiedPrimaryEmailRejectsNotFoundAndDeleted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		deleted    bool
		body       string
	}{
		{"not found", http.StatusNotFound, false, `{"errors":[]}`},
		{"deleted user", http.StatusOK, true, `{"id":"user_gone","deleted":true,"email_addresses":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := client.VerifiedPrimaryEmail(context.Background(), "user_testuser"); err != ErrUserNotFound {
				t.Fatalf("err = %v, want ErrUserNotFound", err)
			}
		})
	}
}

func TestVerifiedPrimaryEmailRejectsUnverifiedPrimary(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		var address backendEmailAddress
		address.ID = "em_primary"
		address.EmailAddress = "student@example.com"
		address.IsPrimary = true
		address.Verification.Status = "pending"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(backendUserJSON(address))
	})
	if _, err := client.VerifiedPrimaryEmail(context.Background(), "user_testuser"); err != ErrNoVerifiedEmail {
		t.Fatalf("err = %v, want ErrNoVerifiedEmail", err)
	}
}

func TestVerifiedPrimaryEmailRequiresVerifiedPrimary(t *testing.T) {
	// A verified non-primary address is not the verified primary email; a
	// missing primary email id must not widen the choice.
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		var other backendEmailAddress
		other.ID = "em_other"
		other.EmailAddress = "other@example.com"
		other.Verification.Status = "verified"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(backendUserJSON(other))
	})
	if _, err := client.VerifiedPrimaryEmail(context.Background(), "user_testuser"); err != ErrNoVerifiedEmail {
		t.Fatalf("err = %v, want ErrNoVerifiedEmail", err)
	}
}

func TestVerifiedPrimaryEmailRejectsBackendFailures(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := client.VerifiedPrimaryEmail(context.Background(), "user_testuser"); err == nil {
		t.Fatal("expected error for backend failure")
	}
}

func TestBackendClientBanUser(t *testing.T) {
	var method, path, auth string
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	if err := client.BanUser(context.Background(), "user/needs+escape"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if method != http.MethodPost || path != "/v1/users/user%2Fneeds+escape/ban" {
		t.Fatalf("request = %s %s", method, path)
	}
	if auth != "Bearer "+backendTestKey {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestBackendClientDeleteUser(t *testing.T) {
	var method, path string
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user_testuser","deleted":true}`))
	})
	if err := client.DeleteUser(context.Background(), "user_testuser"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if method != http.MethodDelete || path != "/v1/users/user_testuser" {
		t.Fatalf("request = %s %s", method, path)
	}
}

func TestBackendClientDeleteUserNotFoundIsIdempotent(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"message":"not found"}]}`))
	})
	if err := client.DeleteUser(context.Background(), "user_testuser"); err != nil {
		t.Fatalf("delete 404 must be idempotent: %v", err)
	}
}

func TestBackendClientBanAndDeleteOrderAndNoCredentialLogging(t *testing.T) {
	var logBuffer strings.Builder
	previous := log.Writer()
	log.SetOutput(&logBuffer)
	t.Cleanup(func() { log.SetOutput(previous) })

	calls := []string{}
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer "+backendTestKey {
			t.Error("backend request is missing bearer auth")
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := client.BanAndDelete(context.Background(), "user_lifecycle"); err != nil {
		t.Fatalf("ban and delete: %v", err)
	}
	want := []string{"POST /v1/users/user_lifecycle/ban", "DELETE /v1/users/user_lifecycle"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if strings.Contains(logBuffer.String(), backendTestKey) || strings.Contains(logBuffer.String(), "user_lifecycle") {
		t.Fatalf("backend client logged credentials or user payload: %q", logBuffer.String())
	}
}

func TestBackendClientLifecycleRejectsInvalidSubject(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("client must reject an invalid subject before calling Clerk")
	})
	if err := client.BanAndDelete(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty subject")
	}
	if err := client.BanAndDelete(context.Background(), "user\r\nX-Evil: 1"); err == nil {
		t.Fatal("expected error for control characters in subject")
	}
}

func TestBackendClientLifecycleReportsTransientFailures(t *testing.T) {
	client := newTestBackendClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := client.BanUser(context.Background(), "user_testuser"); err == nil {
		t.Fatal("expected error for transient ban failure")
	}
	if err := client.DeleteUser(context.Background(), "user_testuser"); err == nil {
		t.Fatal("expected error for transient delete failure")
	}
}
