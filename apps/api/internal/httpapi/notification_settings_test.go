package httpapi

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hireloop/api/internal/clerk"
	"hireloop/api/internal/reminders"
)

// fakeSettingsStore models the notification-settings contract used by the
// HTTP layer without a database.
type fakeSettingsStore struct {
	mu      sync.Mutex
	enabled map[string]bool
	err     error
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{enabled: map[string]bool{}}
}

func (f *fakeSettingsStore) RemindersEnabled(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	enabled, ok := f.enabled[userID]
	if !ok {
		return true, nil // default: enabled
	}
	return enabled, nil
}

func (f *fakeSettingsStore) SetRemindersEnabled(_ context.Context, userID string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.enabled[userID] = enabled
	return nil
}

func (f *fakeSettingsStore) RecordWebhookEvent(_ context.Context, _, _, _ string, _ reminders.Transition) (bool, error) {
	return false, nil
}

func newSettingsHarness(t *testing.T, store *fakeSettingsStore) (http.Handler, string, string) {
	t.Helper()
	now := time.Now()
	tokenA, keyA := signTestRS256(t, now, map[string]any{"sub": "clerk-a"}, "kid-a")
	tokenB, keyB := signTestRS256(t, now, map[string]any{"sub": "clerk-b"}, "kid-b")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{
		"kid-a": &keyA.PublicKey, "kid-b": &keyB.PublicKey,
	})
	verifier.SetNow(func() time.Time { return now })
	handler := NewHandlerWithTasks(verifier, testMapper{bindings: map[string]string{
		"clerk-a": "owner-a", "clerk-b": "owner-b",
	}}, nil, nil, store, nil)
	return handler, tokenA, tokenB
}

// settingsEnabled decodes the {"reminders_enabled": bool} envelope.
func settingsEnabled(t *testing.T, res *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		RemindersEnabled bool `json:"reminders_enabled"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode settings body: %v", err)
	}
	return body.RemindersEnabled
}

func TestNotificationSettingsRoutesRequireAuth(t *testing.T) {
	handler, _, _ := newSettingsHarness(t, newFakeSettingsStore())
	for _, tc := range []struct{ method, body, path string }{
		{http.MethodGet, "", "/api/notification-settings"},
		{http.MethodPatch, `{"reminders_enabled":false}`, "/api/notification-settings"},
	} {
		res := requireAuth(t, handler, tc.method, tc.path, "", tc.body)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s without auth = %d, want 401", tc.method, res.Code)
		}
	}
}

func TestNotificationSettingsDefaultAndUpdate(t *testing.T) {
	handler, tokenA, _ := newSettingsHarness(t, newFakeSettingsStore())
	read := func() bool {
		res := requireAuth(t, handler, http.MethodGet, "/api/notification-settings", tokenA, "")
		if res.Code != http.StatusOK {
			t.Fatalf("GET = %d %s, want 200", res.Code, res.Body.String())
		}
		return settingsEnabled(t, res)
	}
	if !read() {
		t.Fatal("default reminders_enabled = false, want true")
	}
	res := requireAuth(t, handler, http.MethodPatch, "/api/notification-settings", tokenA, `{"reminders_enabled":false}`)
	if res.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s, want 200", res.Code, res.Body.String())
	}
	if settingsEnabled(t, res) {
		t.Fatalf("PATCH response = %s, want reminders_enabled false", res.Body.String())
	}
	if read() {
		t.Fatal("GET after PATCH still reports enabled")
	}
}

func TestNotificationSettingsRejectsInvalidInput(t *testing.T) {
	handler, tokenA, _ := newSettingsHarness(t, newFakeSettingsStore())
	for name, body := range map[string]string{
		"empty body":     "",
		"unknown field":  `{"reminders_enabled":true,"extra":1}`,
		"missing field":  `{}`,
		"non-boolean":    `{"reminders_enabled":"yes"}`,
		"null value":     `{"reminders_enabled":null}`,
		"trailing json":  `{"reminders_enabled":true}{}`,
		"malformed json": `{"reminders_enabled":`,
	} {
		res := requireAuth(t, handler, http.MethodPatch, "/api/notification-settings", tokenA, body)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, res.Code, res.Body.String())
		}
	}
}

func TestNotificationSettingsOwnerIsolation(t *testing.T) {
	handler, tokenA, tokenB := newSettingsHarness(t, newFakeSettingsStore())
	res := requireAuth(t, handler, http.MethodPatch, "/api/notification-settings", tokenB, `{"reminders_enabled":false}`)
	if res.Code != http.StatusOK {
		t.Fatalf("owner B PATCH = %d %s, want 200", res.Code, res.Body.String())
	}
	res = requireAuth(t, handler, http.MethodGet, "/api/notification-settings", tokenA, "")
	if res.Code != http.StatusOK {
		t.Fatalf("owner A GET = %d %s", res.Code, res.Body.String())
	}
	if settingsEnabled(t, res) != true {
		t.Fatalf("owner A settings = %s, want enabled (owner B's change leaked)", res.Body.String())
	}
}

func TestNotificationSettingsUnavailableWithoutStore(t *testing.T) {
	now := time.Now()
	tokenA, keyA := signTestRS256(t, now, map[string]any{"sub": "clerk-a"}, "kid")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid": &keyA.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	handler := NewHandlerWithTasks(verifier, testMapper{bindings: map[string]string{"clerk-a": "owner-a"}}, nil, nil, nil, nil)
	for _, tc := range []struct{ method, body, path string }{
		{http.MethodGet, "", "/api/notification-settings"},
		{http.MethodPatch, `{"reminders_enabled":false}`, "/api/notification-settings"},
	} {
		res := requireAuth(t, handler, tc.method, tc.path, tokenA, tc.body)
		if res.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s without store = %d, want 503", tc.method, res.Code)
		}
	}
}

func TestNotificationSettingsStoreFailureIsRedacted(t *testing.T) {
	store := newFakeSettingsStore()
	store.err = errors.New("private database detail")
	handler, tokenA, _ := newSettingsHarness(t, store)
	res := requireAuth(t, handler, http.MethodGet, "/api/notification-settings", tokenA, "")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with store error = %d, want 503", res.Code)
	}
	if strings.Contains(res.Body.String(), "private") {
		t.Fatalf("store error leaked into response: %q", res.Body.String())
	}
}
