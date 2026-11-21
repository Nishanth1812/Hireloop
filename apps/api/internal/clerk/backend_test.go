package clerk

import (
	"context"
	"encoding/json"
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
