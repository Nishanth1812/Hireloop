package resend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testAPIKey = "re_test_SUPERSECRET_key_9f8e7d6c"

func TestRetryAfterCannotOverflow(t *testing.T) {
	for _, value := range []string{"3601", "9223372037"} {
		if got := parseRetryAfter(value); got != time.Hour {
			t.Fatalf("parseRetryAfter(%q) = %v, want 1h", value, got)
		}
	}
}

func TestInFlightTransportFailureIsAmbiguous(t *testing.T) {
	result, err := classifyTransportError(context.Background(), errors.New("connection reset"))
	if err == nil || result.Status != StatusAmbiguous {
		t.Fatalf("result = %+v, err = %v; want ambiguous failure", result, err)
	}
}

func newSenderForServer(t *testing.T, endpoint string, timeout time.Duration) *Sender {
	t.Helper()
	s, err := NewSender(testAPIKey, timeout)
	if err != nil {
		t.Fatal(err)
	}
	s.endpoint = strings.TrimSuffix(endpoint, "/") + "/emails"
	return s
}

func testMessage() Message {
	return Message{
		From:    "Hireloop <reminders@hireloop.example>",
		To:      []string{"student@example.com"},
		Subject: "Your reminder",
		Text:    "Assessment due today.",
	}
}

func TestNewSenderValidatesConfig(t *testing.T) {
	if _, err := NewSender("", 0); err == nil {
		t.Fatal("expected error for empty API key")
	}
	if _, err := NewSender(strings.Repeat("x", maxAPIKeyBytes+1), 0); err == nil {
		t.Fatal("expected error for oversized API key")
	}
	if _, err := NewSender("re_ok\r\nX-Evil: 1", 0); err == nil {
		t.Fatal("expected error for API key with control characters")
	}
	if _, err := NewSender("re_ok", -time.Second); err == nil {
		t.Fatal("expected error for negative timeout")
	}
	s, err := NewSender("re_ok", 0)
	if err != nil {
		t.Fatalf("valid config = %v", err)
	}
	if s.endpoint != apiEndpoint {
		t.Fatalf("endpoint = %q, want %q", s.endpoint, apiEndpoint)
	}
	if s.client.Timeout != DefaultSendTimeout {
		t.Fatalf("timeout = %v, want %v", s.client.Timeout, DefaultSendTimeout)
	}
	if s.client.CheckRedirect == nil {
		t.Fatal("redirects must be refused")
	}
}

func TestSendSuccess(t *testing.T) {
	const id = "49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"
	var method, path, auth, ua, contentType, idempotency, accept string
	var body Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		auth = r.Header.Get("Authorization")
		ua = r.Header.Get("User-Agent")
		contentType = r.Header.Get("Content-Type")
		idempotency = r.Header.Get("Idempotency-Key")
		accept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
	}))
	defer server.Close()

	s := newSenderForServer(t, server.URL, 0)
	msg := testMessage()
	msg.HTML = "<p>Assessment due today.</p>"
	res, err := s.Send(context.Background(), "reminder/notification-42", msg)
	if err != nil {
		t.Fatalf("send = %v", err)
	}
	if res.Status != StatusAccepted || res.MessageID != id {
		t.Fatalf("result = %+v, want accepted with id %q", res, id)
	}
	if method != http.MethodPost || path != "/emails" {
		t.Errorf("request = %s %s", method, path)
	}
	if auth != "Bearer "+testAPIKey {
		t.Errorf("authorization header = %q", auth)
	}
	if ua == "" || contentType != "application/json" || !strings.Contains(accept, "application/json") {
		t.Errorf("headers: UA=%q CT=%q Accept=%q", ua, contentType, accept)
	}
	if idempotency != "reminder/notification-42" {
		t.Errorf("idempotency key = %q", idempotency)
	}
	if body.From != msg.From || body.Subject != msg.Subject || len(body.To) != 1 || body.To[0] != msg.To[0] {
		t.Errorf("body = %+v", body)
	}
	if body.Text != msg.Text || body.HTML != msg.HTML {
		t.Errorf("body content = %+v", body)
	}
}

func TestSendRejectsInvalidInputBeforeNetwork(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	s := newSenderForServer(t, server.URL, 0)

	longKey := strings.Repeat("k", maxIdempotencyBytes+1)
	bodies := []struct {
		name string
		key  string
		msg  Message
	}{
		{"empty idempotency key", "", testMessage()},
		{"oversized idempotency key", longKey, testMessage()},
		{"control char in idempotency key", "abc\ndef", testMessage()},
		{"missing from", "key-1", Message{To: []string{"a@b.example"}, Subject: "s", Text: "t"}},
		{"no recipients", "key-2", Message{From: "a@b.example", Subject: "s", Text: "t"}},
		{"no content", "key-3", Message{From: "a@b.example", To: []string{"a@b.example"}, Subject: "s"}},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.Send(context.Background(), tc.key, tc.msg)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if res.Status != StatusPermanent {
				t.Fatalf("status = %q, want permanent", res.Status)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server hits = %d, want 0 (no request should be made)", n)
	}
}

func TestSendStatusClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		headers   map[string]string
		want      Status
		wantRetry time.Duration
	}{
		{"validation error", 400, `{"type":"validation_error","message":"bad field"}`, nil, StatusPermanent, 0},
		{"invalid idempotency key", 400, `{"type":"invalid_idempotency_key"}`, nil, StatusPermanent, 0},
		{"unauthorized", 401, `{"type":"validation_error","message":"invalid api key"}`, nil, StatusPermanent, 0},
		{"forbidden", 403, `{"type":"validation_error","message":"domain not verified"}`, nil, StatusPermanent, 0},
		{"unprocessable", 422, `{"type":"missing_required_field"}`, nil, StatusPermanent, 0},
		{"concurrent idempotent", 409, `{"type":"concurrent_idempotent_requests"}`, nil, StatusRetryable, 0},
		{"invalid idempotent request", 409, `{"type":"invalid_idempotent_request"}`, nil, StatusPermanent, 0},
		{"rate limited", 429, `{"type":"rate_limit_exceeded"}`, map[string]string{"Retry-After": "2"}, StatusRetryable, 2 * time.Second},
		{"daily quota", 429, `{"type":"daily_quota_exceeded"}`, nil, StatusRetryable, 0},
		{"server error", 500, `{"type":"application_error"}`, nil, StatusRetryable, 0},
		{"unavailable", 503, `{"type":"service_unavailable"}`, nil, StatusRetryable, 0},
		{"redirect refused", 302, ``, map[string]string{"Location": "http://127.0.0.1:1/steal"}, StatusPermanent, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := newSenderForServer(t, server.URL, 0)

			res, err := s.Send(context.Background(), "key-1", testMessage())
			if res.Status != tc.want {
				t.Fatalf("status = %q, want %q (err: %v)", res.Status, tc.want, err)
			}
			if tc.want == StatusAccepted {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil {
				t.Fatal("expected an error for a non-accepted result")
			}
			if res.MessageID != "" {
				t.Errorf("message id = %q, want empty", res.MessageID)
			}
			if res.RetryAfter != tc.wantRetry {
				t.Errorf("retry after = %v, want %v", res.RetryAfter, tc.wantRetry)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("server hits = %d, want 1 (redirects must not be followed)", n)
			}
			if strings.Contains(errString(err), testAPIKey) {
				t.Errorf("error leaked the API key: %v", err)
			}
		})
	}
}

func TestSendTimeoutIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":"late"}`))
	}))
	defer server.Close()
	s := newSenderForServer(t, server.URL, 40*time.Millisecond)

	res, err := s.Send(context.Background(), "key-1", testMessage())
	if res.Status != StatusAmbiguous {
		t.Fatalf("status = %q, want ambiguous (err: %v)", res.Status, err)
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout diagnostic", err)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("error leaked the API key: %v", err)
	}
}

func TestSendContextCancellation(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	s := newSenderForServer(t, server.URL, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := s.Send(ctx, "key-1", testMessage())
	if err == nil || res.Status != StatusRetryable {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server hits = %d, want 0", n)
	}
}

func TestSendMalformedOrOversizedSuccessIsAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"not json", `not-json`},
		{"missing id", `{"other":"value"}`},
		{"control char in id", `{"id":"abc\ndef"}`},
		{"oversized body", `{"id":"` + strings.Repeat("x", 2*maxResponseBytes) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := newSenderForServer(t, server.URL, 0)

			res, err := s.Send(context.Background(), "key-1", testMessage())
			if res.Status != StatusAmbiguous || res.MessageID != "" {
				t.Fatalf("result = %+v, err = %v; want ambiguous with no id", res, err)
			}
		})
	}
}

func TestIdempotencyKeyIsStableAcrossRetries(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"id"}`))
	}))
	defer server.Close()
	s := newSenderForServer(t, server.URL, 0)

	for i := 0; i < 3; i++ {
		if _, err := s.Send(context.Background(), "reminder/notification-42", testMessage()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Send(context.Background(), "reminder/notification-43", testMessage()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 4 {
		t.Fatalf("requests = %d, want 4", len(keys))
	}
	for i := 0; i < 3; i++ {
		if keys[i] != "reminder/notification-42" {
			t.Errorf("retry %d key = %q", i, keys[i])
		}
	}
	if keys[3] != "reminder/notification-43" {
		t.Errorf("different logical email key = %q", keys[3])
	}
}

func TestSendOverTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"tls-id"}`))
	}))
	defer server.Close()
	s := newSenderForServer(t, server.URL, 0)
	// Trust only the test server's RootCAs while keeping our TLS minimum and
	// redirect refusal. Production configuration is untouched.
	serverTransport := server.Client().Transport.(*http.Transport)
	s.client.Transport.(*http.Transport).TLSClientConfig = serverTransport.TLSClientConfig

	res, err := s.Send(context.Background(), "key-1", testMessage())
	if err != nil || res.Status != StatusAccepted || res.MessageID != "tls-id" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
