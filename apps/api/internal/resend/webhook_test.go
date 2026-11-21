package resend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testSecret  = "whsec_AAAAAAAAAAAAAAAAAAAAAA=="
	testSecret2 = "whsec_AQEBAQEBAQEBAQEBAQEBAQ=="
	testTS      = "1771803672"
	testMsgID   = "msg_loFOjxBNrRLzqYUf"
)

const deliveredPayload = `{"type":"email.delivered","created_at":"2026-02-22T23:41:12.126Z","data":{"email_id":"56761188-7520-42d8-8898-ff6fc54ce618","to":["student@example.com"],"subject":"PRIVATE SUBJECT SHOULD NOT BE RETAINED","tags":{"notification":"42"}}}`

func newVerifier(t *testing.T, secrets ...string) *WebhookVerifier {
	t.Helper()
	v, err := NewWebhookVerifier(secrets...)
	if err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return time.Unix(1771803672, 0) }
	return v
}

func signBody(t *testing.T, secret, id, timestamp string, body []byte) string {
	t.Helper()
	key, err := decodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, timestamp)
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func webhookRequest(id, timestamp, signature string, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/resend", bytes.NewReader(body))
	r.Header.Set("svix-id", id)
	r.Header.Set("svix-timestamp", timestamp)
	r.Header.Set("svix-signature", signature)
	return r
}

func signedRequest(t *testing.T, secret string, body []byte) *http.Request {
	t.Helper()
	return webhookRequest(testMsgID, testTS, signBody(t, secret, testMsgID, testTS, body), body)
}

func TestNewWebhookVerifierValidation(t *testing.T) {
	if _, err := NewWebhookVerifier(); err == nil {
		t.Fatal("expected error with no secrets")
	}
	if _, err := NewWebhookVerifier(""); err == nil {
		t.Fatal("expected error with empty secret")
	}
	if _, err := NewWebhookVerifier("whsec_!!!not-base64!!!"); err == nil {
		t.Fatal("expected error with invalid base64")
	}
	if _, err := NewWebhookVerifier("whsec_c2hvcnQ="); err == nil {
		t.Fatal("expected error with short secret")
	}

	v, err := NewWebhookVerifier(testSecret, testSecret2, testSecret)
	if err != nil {
		t.Fatalf("multiple secrets = %v", err)
	}
	if len(v.secrets) != 2 {
		t.Fatalf("secrets = %d, want 2 (duplicate removed)", len(v.secrets))
	}
	if v.tolerance != DefaultWebhookTolerance {
		t.Fatalf("tolerance = %v, want %v", v.tolerance, DefaultWebhookTolerance)
	}

	bare := strings.TrimPrefix(testSecret, "whsec_")
	if _, err := NewWebhookVerifier(bare); err != nil {
		t.Fatalf("bare base64 secret = %v", err)
	}
}

func TestVerifyValidWebhook(t *testing.T) {
	v := newVerifier(t, testSecret)
	event, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, []byte(deliveredPayload)))
	if err != nil {
		t.Fatalf("verify = %v", err)
	}
	if event.ID != testMsgID || event.Type != EventDelivered {
		t.Fatalf("event = %+v", event)
	}
	if event.EmailID != "56761188-7520-42d8-8898-ff6fc54ce618" {
		t.Fatalf("email id = %q", event.EmailID)
	}
	if event.CreatedAt.IsZero() {
		t.Fatal("created_at not parsed")
	}
}

func TestVerifyTamperedBodyAndSignature(t *testing.T) {
	v := newVerifier(t, testSecret)
	body := []byte(deliveredPayload)

	tampered := append([]byte(nil), body...)
	tampered[10] ^= 0x01
	if _, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, tampered)); err == nil {
		t.Fatal("expected rejection for tampered body")
	}

	// Same length as a valid signature but different bytes: must still fail.
	sig := signBody(t, testSecret, testMsgID, testTS, body)
	rawSig, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig, "v1,"))
	rawSig[0] ^= 0x01
	badSig := "v1," + base64.StdEncoding.EncodeToString(rawSig)
	if _, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, testTS, badSig, body)); err == nil {
		t.Fatal("expected rejection for near-match signature")
	}

	// A valid signature for a different message id must not verify.
	otherID := signBody(t, testSecret, "msg_other", testTS, body)
	if _, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, testTS, otherID, body)); err == nil {
		t.Fatal("expected rejection for signature bound to another id")
	}
}

func TestVerifyTimestampTolerance(t *testing.T) {
	v := newVerifier(t, testSecret)
	body := []byte(deliveredPayload)
	now := time.Unix(1771803672, 0)

	for _, tc := range []struct {
		name string
		ts   string
		ok   bool
	}{
		{"current", testTS, true},
		{"four minutes old", fmt.Sprint(now.Add(-4 * time.Minute).Unix()), true},
		{"four minutes ahead", fmt.Sprint(now.Add(4 * time.Minute).Unix()), true},
		{"six minutes old (expired)", fmt.Sprint(now.Add(-6 * time.Minute).Unix()), false},
		{"six minutes ahead", fmt.Sprint(now.Add(6 * time.Minute).Unix()), false},
		{"non numeric", "not-a-number", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig := signBody(t, testSecret, testMsgID, tc.ts, body)
			_, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, tc.ts, sig, body))
			if tc.ok && err != nil {
				t.Fatalf("verify = %v, want success", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected rejection")
			}
		})
	}

	v.SetTolerance(time.Hour)
	sig := signBody(t, testSecret, testMsgID, fmt.Sprint(now.Add(-10*time.Minute).Unix()), body)
	if _, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, fmt.Sprint(now.Add(-10*time.Minute).Unix()), sig, body)); err != nil {
		t.Fatalf("verify with wider tolerance = %v", err)
	}
}

func TestVerifyMissingAndMalformedHeaders(t *testing.T) {
	v := newVerifier(t, testSecret)
	body := []byte(deliveredPayload)
	sig := signBody(t, testSecret, testMsgID, testTS, body)

	for _, tc := range []struct {
		name string
		id   string
		ts   string
		sig  string
	}{
		{"missing id", "", testTS, sig},
		{"missing timestamp", testMsgID, "", sig},
		{"missing signature", testMsgID, testTS, ""},
		{"oversized id", strings.Repeat("x", maxSvixHeaderBytes+1), testTS, sig},
		{"control char in id", "msg\ninjected", testTS, sig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.VerifyRequest(context.Background(), webhookRequest(tc.id, tc.ts, tc.sig, body)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestVerifyRotationAndMultipleSignatures(t *testing.T) {
	body := []byte(deliveredPayload)
	v := newVerifier(t, testSecret, testSecret2)

	// Signed with the newly rotated-in secret.
	if _, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret2, body)); err != nil {
		t.Fatalf("new secret = %v", err)
	}
	// Old secret still verifies during the overlap window.
	if _, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, body)); err != nil {
		t.Fatalf("old secret = %v", err)
	}

	// Multiple v1 signatures: a bad one, a good one, and an unknown version.
	good := signBody(t, testSecret2, testMsgID, testTS, body)
	bad := "v1," + base64.StdEncoding.EncodeToString(make([]byte, 32))
	header := bad + " " + good + " v2,MzJsNDk4MzI0K2VvdSMjMTEjQEBAQDEyMzMzMzEyMwo="
	if _, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, testTS, header, body)); err != nil {
		t.Fatalf("multiple v1 signatures = %v", err)
	}
}

func TestVerifyRequiresV1Signature(t *testing.T) {
	v := newVerifier(t, testSecret)
	body := []byte(deliveredPayload)
	header := "v2,MzJsNDk4MzI0K2VvdSMjMTEjQEBAQDEyMzMzMzEyMwo="
	if _, err := v.VerifyRequest(context.Background(), webhookRequest(testMsgID, testTS, header, body)); err == nil {
		t.Fatal("expected rejection when only a v2 signature is present")
	}
}

// TestVerifyReplayIdentityStable documents that verification is stateless: the
// same signed message verifies repeatedly and always exposes the same svix-id.
// Deduplication is the caller's responsibility at the DB level.
func TestVerifyReplayIdentityStable(t *testing.T) {
	v := newVerifier(t, testSecret)
	body := []byte(deliveredPayload)
	for i := 0; i < 3; i++ {
		event, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, body))
		if err != nil {
			t.Fatalf("replay %d = %v", i, err)
		}
		if event.ID != testMsgID {
			t.Fatalf("replay %d id = %q, want %q", i, event.ID, testMsgID)
		}
	}
}

func TestVerifyBodyBound(t *testing.T) {
	v := newVerifier(t, testSecret)
	v.maxBody = 32
	body := []byte(deliveredPayload)
	if _, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, body)); err == nil {
		t.Fatal("expected rejection for oversized body")
	}

	// Declared Content-Length is rejected before the body is read.
	v.maxBody = maxWebhookBodyBytes
	r := signedRequest(t, testSecret, body)
	r.ContentLength = v.maxBody + 1
	if _, err := v.VerifyRequest(context.Background(), r); err == nil {
		t.Fatal("expected rejection for oversized Content-Length")
	}
}

func TestVerifyTypedEventParsing(t *testing.T) {
	v := newVerifier(t, testSecret)
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"delivered", deliveredPayload, true},
		{"unknown type", strings.Replace(deliveredPayload, "email.delivered", "email.teleported", 1), false},
		{"missing email id", strings.Replace(deliveredPayload, "56761188-7520-42d8-8898-ff6fc54ce618", "", 1), false},
		{"invalid created_at", strings.Replace(deliveredPayload, "2026-02-22T23:41:12.126Z", "yesterday", 1), false},
		{"not json", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := v.VerifyRequest(context.Background(), signedRequest(t, testSecret, []byte(tc.body)))
			if tc.ok && err != nil {
				t.Fatalf("verify = %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected rejection, got %+v", event)
			}
		})
	}
}

func TestVerifyContextCancellation(t *testing.T) {
	v := newVerifier(t, testSecret)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.VerifyRequest(ctx, signedRequest(t, testSecret, []byte(deliveredPayload))); err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// TestVerifyUsesRawBody confirms that reformatting the JSON invalidates the
// signature: callers must pass the untouched bytes they received.
func TestVerifyUsesRawBody(t *testing.T) {
	v := newVerifier(t, testSecret)
	minified := []byte(deliveredPayload)
	pretty := []byte(strings.ReplaceAll(deliveredPayload, ",", ",\n"))
	r := webhookRequest(testMsgID, testTS, signBody(t, testSecret, testMsgID, testTS, minified), pretty)
	if _, err := v.VerifyRequest(context.Background(), r); err == nil {
		t.Fatal("expected rejection for a reformatted body")
	}
}
