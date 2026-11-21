package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hireloop/api/internal/reminders"
	"hireloop/api/internal/resend"
)

// fakeWebhookStore implements the notificationSettingsStore surface the
// webhook route uses.
type fakeWebhookStore struct {
	mu     sync.Mutex
	seen   []string
	applied map[string]bool
}

func (f *fakeWebhookStore) RemindersEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func (f *fakeWebhookStore) SetRemindersEnabled(context.Context, string, bool) error { return nil }

func (f *fakeWebhookStore) RecordWebhookEvent(_ context.Context, eventID, _, _ string, _ reminders.Transition) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, eventID)
	if f.applied == nil {
		f.applied = map[string]bool{}
	}
	f.applied[eventID] = true
	return true, nil
}

func (f *fakeWebhookStore) seenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// svixSign computes the Svix v1 signature over "<id>.<timestamp>.<body>".
func svixSign(secret, id, timestamp string, body []byte) string {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		panic(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, timestamp)
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// resendTestSecret is a valid Svix secret (whsec_ + base64, >= 16 bytes).
const resendTestSecret = "whsec_AAAAAAAAAAAAAAAAAAAAAA=="

func postWebhook(t *testing.T, handler http.Handler, eventID, timestamp, secret string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/resend", strings.NewReader(string(body)))
	req.Header.Set("svix-id", eventID)
	req.Header.Set("svix-timestamp", timestamp)
	req.Header.Set("svix-signature", svixSign(secret, eventID, timestamp, body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRouteFailsClosedWhenNotConfigured(t *testing.T) {
	handler := NewHandlerWithTasks(nil, nil, nil, nil, &fakeWebhookStore{}, nil)
	rec := postWebhook(t, handler, "msg_1", fmt.Sprint(time.Now().Unix()), resendTestSecret, []byte(`{"type":"email.delivered","created_at":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","data":{"email_id":"email-1"}}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestWebhookRouteRejectsUnsignedAndInvalidEvents(t *testing.T) {
	verifier, err := resend.NewWebhookVerifier(resendTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithTasks(nil, nil, nil, nil, &fakeWebhookStore{}, verifier)
	body := []byte(`{"type":"email.delivered","created_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","data":{"email_id":"email-1"}}`)

	// Signed with a different secret: the signature does not verify.
	rec := postWebhook(t, handler, "msg_2", fmt.Sprint(time.Now().Unix()), "whsec_"+base64.StdEncoding.EncodeToString([]byte("another-secret-that-verifies")), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-secret code = %d, want 400", rec.Code)
	}
	// No svix headers at all.
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/resend", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsigned code = %d, want 400", rec.Code)
	}
	// A stale timestamp is rejected (tolerance window).
	stale := time.Now().Add(-time.Hour).Unix()
	rec = postWebhook(t, handler, "msg_3", fmt.Sprint(stale), resendTestSecret, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stale-timestamp code = %d, want 400", rec.Code)
	}
}

func TestWebhookRouteAppliesVerifiedEventsAndSkipsNoOps(t *testing.T) {
	verifier, err := resend.NewWebhookVerifier(resendTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeWebhookStore{}
	handler := NewHandlerWithTasks(nil, nil, nil, nil, store, verifier)

	delivered := []byte(`{"type":"email.delivered","created_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","data":{"email_id":"email-1"}}`)
	rec := postWebhook(t, handler, "msg_4", fmt.Sprint(time.Now().Unix()), resendTestSecret, delivered)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delivered code = %d, want 204", rec.Code)
	}
	if store.seenCount() != 1 {
		t.Fatalf("store saw %d events, want 1", store.seenCount())
	}

	// Events that never change delivery state are acknowledged without
	// reaching the store.
	opened := []byte(`{"type":"email.opened","created_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","data":{"email_id":"email-1"}}`)
	rec = postWebhook(t, handler, "msg_5", fmt.Sprint(time.Now().Unix()), resendTestSecret, opened)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("opened code = %d, want 204", rec.Code)
	}
	if store.seenCount() != 1 {
		t.Fatalf("store saw %d events, want 1 (no-op event must not reach the store)", store.seenCount())
	}
}
