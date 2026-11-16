package clerk

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func encodeSegment(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// signToken builds an RS256 Clerk-like JWT signed by key.
func signToken(t *testing.T, key *rsa.PrivateKey, now time.Time, claims map[string]any, headerKid string) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	if headerKid != "" {
		header["kid"] = headerKid
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"iss":   "https://clerk.example.com",
		"sub":   "user_clerk_1",
		"email": "person@example.com",
		"exp":   now.Add(time.Hour).Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
	}
	for k, v := range claims {
		payload[k] = v
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	signing := []byte(encodeSegment(headerBytes) + "." + encodeSegment(payloadBytes))
	digest := sha256.Sum256(signing)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return encodeSegment(headerBytes) + "." + encodeSegment(payloadBytes) + "." + encodeSegment(sig)
}

func newTestVerifier(t *testing.T, key *rsa.PrivateKey, kid string) *Verifier {
	t.Helper()
	return NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{kid: &key.PublicKey})
}

func TestVerifyValidToken(t *testing.T) {
	key := mustKey(t)
	now := time.Now()
	token := signToken(t, key, now, nil, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	v.now = func() time.Time { return now }
	u, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("verify = %v", err)
	}
	if u.ID != "user_clerk_1" || u.Email != "person@example.com" {
		t.Fatalf("user = %+v", u)
	}
}

func TestVerifyUnknownKid(t *testing.T) {
	key := mustKey(t)
	token := signToken(t, key, time.Now(), nil, "kid-1")
	v := NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid-2": &key.PublicKey})
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected error for unknown kid")
	}
}

func TestVerifyTamperedSignature(t *testing.T) {
	key := mustKey(t)
	token := signToken(t, key, time.Now(), nil, "kid-1")
	parts := strings.Split(token, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2] + "===")
	sig[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	v := newTestVerifier(t, key, "kid-1")
	if _, err := v.Verify(context.Background(), strings.Join(parts, ".")); err == nil {
		t.Fatal("expected error for tampered signature")
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	key := mustKey(t)
	past := time.Now().Add(-2 * time.Hour)
	token := signToken(t, key, past, map[string]any{"exp": past.Add(time.Minute).Unix()}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	v.now = func() time.Time { return time.Now() }
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerifyWrongIssuer(t *testing.T) {
	key := mustKey(t)
	token := signToken(t, key, time.Now(), map[string]any{"iss": "https://evil.example.com"}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected error for wrong issuer")
	}
}

func TestVerifyMalformed(t *testing.T) {
	v := newTestVerifier(t, mustKey(t), "kid-1")
	for _, bad := range []string{"", "not.a.jwt", "a.b", "a.b.c.d"} {
		if _, err := v.Verify(context.Background(), bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

// TestVerifyAudienceIgnored confirms Clerk session tokens carry no audience by
// default: a token issued for any audience (or none) is accepted so long as
// the issuer, signature, and expiry are valid. The app ID is never the
// audience and is never required.
func TestVerifyAudienceIgnored(t *testing.T) {
	key := mustKey(t)
	token := signToken(t, key, time.Now(), map[string]any{"aud": "some-other-app"}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("verify = %v, want success: audience must not be validated", err)
	}
}

// TestVerifyRotatedSigningKey verifies key rotation: when the cached signing
// key no longer validates a token, the verifier refreshes Clerk's JWKS once
// and retries before rejecting the token.
func TestVerifyRotatedSigningKey(t *testing.T) {
	jwks := func(keys map[string]*rsa.PublicKey) []byte {
		items := make([]jwk, 0, len(keys))
		for kid, key := range keys {
			nBytes := key.N.Bytes()
			eBytes := bigEndianUint32(uint32(key.E))
			items = append(items, jwk{
				Kty: "RSA",
				Kid: kid,
				N:   base64.RawURLEncoding.EncodeToString(nBytes),
				E:   base64.RawURLEncoding.EncodeToString(eBytes),
			})
		}
		b, _ := json.Marshal(map[string]any{"keys": items})
		return b
	}

	served := &atomicValue{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(served.Get().([]byte))
	}))
	defer server.Close()

	key1 := mustKey(t)
	key2 := mustKey(t)
	served.Set(jwks(map[string]*rsa.PublicKey{"kid-1": &key1.PublicKey}))

	v, err := NewVerifier(server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	// Token signed by the original key verifies (JWKS fetched and cached).
	token1 := signToken(t, key1, time.Now(), map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token1); err != nil {
		t.Fatalf("initial verify = %v, want success", err)
	}

	// Rotate the signing key under the same kid. The cached key1 now fails.
	served.Set(jwks(map[string]*rsa.PublicKey{"kid-1": &key2.PublicKey}))
	token2 := signToken(t, key2, time.Now(), map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token2); err != nil {
		t.Fatalf("rotated-key verify = %v, want success after JWKS refresh", err)
	}
}

// TestVerifyTokenWithoutExpiry confirms a session token with no expiry (exp ==
// 0) is rejected: a session token must always carry a lifetime bound.
func TestVerifyTokenWithoutExpiry(t *testing.T) {
	key := mustKey(t)
	now := time.Now()
	token := signToken(t, key, now, map[string]any{"exp": 0}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	v.now = func() time.Time { return now }
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected error for token without expiry")
	}
}

// jwksPayload marshals a JWKS response with a single RSA key for kid.
func jwksPayload(kid string, key *rsa.PublicKey) []byte {
	nBytes := key.N.Bytes()
	eBytes := bigEndianUint32(uint32(key.E))
	b, err := json.Marshal(map[string]any{"keys": []jwk{{
		Kty: "RSA",
		Kid: kid,
		N:   base64.RawURLEncoding.EncodeToString(nBytes),
		E:   base64.RawURLEncoding.EncodeToString(eBytes),
	}}})
	if err != nil {
		panic(err)
	}
	return b
}

// TestVerifyKeyRefreshAfterTTL confirms cached signing keys are re-fetched and
// replaced after keyTTL elapses, so a rotated key is picked up and a revoked
// key is evicted.
func TestVerifyKeyRefreshAfterTTL(t *testing.T) {
	key1 := mustKey(t)
	key2 := mustKey(t)

	var mu sync.Mutex
	var fetchCount int
	nextKey := &key1.PublicKey
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksPayload("kid-1", nextKey))
	}))
	defer server.Close()

	v, err := NewVerifier(server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	v.keyTTL = time.Second       // keys age out after one second
	v.refreshInterval = 0        // allow immediate refetch in the test
	fake := time.Now()
	v.now = func() time.Time { return fake }

	// First verification fetches and caches key1.
	token1 := signToken(t, key1, fake, map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token1); err != nil {
		t.Fatalf("initial verify = %v, want success", err)
	}
	mu.Lock()
	if fetchCount != 1 {
		t.Fatalf("fetchCount = %d, want 1", fetchCount)
	}
	mu.Unlock()

	// Rotate the served key to key2 and advance the clock past keyTTL. The
	// cached key1 is now stale, so verification refreshes JWKS and picks up
	// key2 (evicting key1).
	fake = fake.Add(2 * time.Second)
	mu.Lock()
	nextKey = &key2.PublicKey
	mu.Unlock()
	token2 := signToken(t, key2, fake, map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token2); err != nil {
		t.Fatalf("rotated verify after TTL = %v, want success after refresh", err)
	}
	mu.Lock()
	if fetchCount != 2 {
		t.Fatalf("fetchCount = %d, want 2", fetchCount)
	}
	mu.Unlock()
}

// TestVerifyDoesNotUnboundedlyFetchInvalidTokens confirms invalid tokens with
// an unknown kid do not trigger repeated JWKS fetches: the refresh interval
// bounds the fetch rate so a single invalid token storm costs one fetch.
func TestVerifyDoesNotUnboundedlyFetchInvalidTokens(t *testing.T) {
	key := mustKey(t)
	var mu sync.Mutex
	var fetchCount int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksPayload("kid-1", &key.PublicKey))
	}))
	defer server.Close()

	v, err := NewVerifier(server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	v.refreshInterval = time.Minute // bound fetches to once per minute
	v.now = func() time.Time { return time.Now() }

	// Many invalid tokens with an unknown kid must not each fetch JWKS.
	for i := 0; i < 50; i++ {
		token := signToken(t, key, time.Now(), map[string]any{"iss": server.URL}, "kid-99")
		_, _ = v.Verify(context.Background(), token)
	}
	mu.Lock()
	count := fetchCount
	mu.Unlock()
	if count > 1 {
		t.Fatalf("fetchCount = %d for 50 invalid tokens, want <= 1", count)
	}

	// A valid kid-1 token verifies from the cached key without another fetch.
	token := signToken(t, key, time.Now(), map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("valid kid-1 token = %v, want success using cached key", err)
	}
	mu.Lock()
	count = fetchCount
	mu.Unlock()
	if count > 1 {
		t.Fatalf("fetchCount = %d after valid token, want <= 1 (served from cache)", count)
	}
}

// TestVerifyStaleKeyFailsClosedOnRefreshFailure confirms that when a cached
// signing key is past its TTL and Clerk's JWKS cannot be refreshed, the
// verifier fails closed instead of accepting the stale (possibly revoked) key.
func TestVerifyStaleKeyFailsClosedOnRefreshFailure(t *testing.T) {
	key1 := mustKey(t)

	var mu sync.Mutex
	serveErr := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		err := serveErr
		mu.Unlock()
		if err {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksPayload("kid-1", &key1.PublicKey))
	}))
	defer server.Close()

	v, err := NewVerifier(server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	v.keyTTL = time.Second       // keys age out after one second
	v.refreshInterval = 0        // always attempt a refresh when stale
	fake := time.Now()
	v.now = func() time.Time { return fake }

	// Cache key1 with a successful fetch.
	token1 := signToken(t, key1, fake, map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token1); err != nil {
		t.Fatalf("initial verify = %v, want success", err)
	}

	// Make JWKS fail and advance the clock past keyTTL so key1 is stale.
	mu.Lock()
	serveErr = true
	mu.Unlock()
	fake = fake.Add(2 * time.Second)

	// The cached key1 is stale and the refresh fails: verification must fail
	// closed rather than accept the stale key.
	token2 := signToken(t, key1, fake, map[string]any{"iss": server.URL}, "kid-1")
	if _, err := v.Verify(context.Background(), token2); err == nil {
		t.Fatal("expected error for stale key when JWKS refresh fails")
	}
}

// TestVerifyClockSkewExpiresEarly confirms a positive clock skew treats exp as
// earlier (conservative): a token is rejected before its nominal expiry once
// now reaches exp - skew.
func TestVerifyClockSkewExpiresEarly(t *testing.T) {
	key := mustKey(t)
	base := time.Now()
	token := signToken(t, key, base, map[string]any{"exp": base.Add(time.Minute).Unix()}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	v.SetClockSkew(30 * time.Second)
	v.SetNow(func() time.Time { return base.Add(45 * time.Second) })
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected error: positive skew should treat exp as earlier")
	}
}

// TestVerifyClockSkewAcceptsNbfEarly confirms a positive clock skew treats nbf
// as earlier (lenient): a token is accepted before its nominal not-before once
// now + skew reaches nbf.
func TestVerifyClockSkewAcceptsNbfEarly(t *testing.T) {
	key := mustKey(t)
	base := time.Now()
	token := signToken(t, key, base, map[string]any{"nbf": base.Add(time.Minute).Unix()}, "kid-1")
	v := newTestVerifier(t, key, "kid-1")
	v.SetClockSkew(30 * time.Second)
	v.SetNow(func() time.Time { return base.Add(45 * time.Second) })
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("expected success: positive skew should treat nbf as earlier, got %v", err)
	}
}

// TestNewVerifierAcceptsValidConfig confirms a matching HTTPS issuer and JWKS
// URL are accepted.
func TestNewVerifierAcceptsValidConfig(t *testing.T) {
	v, err := NewVerifier("https://clerk.example.com", "https://clerk.example.com/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("valid config = %v, want success", err)
	}
	if v.issuer != "https://clerk.example.com" || v.jwksURL != "https://clerk.example.com/.well-known/jwks.json" {
		t.Fatalf("verifier fields not stored: issuer=%q jwksURL=%q", v.issuer, v.jwksURL)
	}
}

// TestNewVerifierRejectsNonHTTPJWKSURL confirms a plain-HTTP JWKS URL is
// rejected to prevent SSRF to internal services.
func TestNewVerifierRejectsNonHTTPJWKSURL(t *testing.T) {
	if _, err := NewVerifier("https://clerk.example.com", "http://127.0.0.1/.well-known/jwks.json"); err == nil {
		t.Fatal("expected error for plain-HTTP JWKS URL")
	}
}

// TestNewVerifierRejectsMismatchedOrigin confirms the JWKS URL host must match
// the issuer host, so signing keys are only fetched from the token's origin.
func TestNewVerifierRejectsMismatchedOrigin(t *testing.T) {
	if _, err := NewVerifier("https://clerk.example.com", "https://attacker.example.com/.well-known/jwks.json"); err == nil {
		t.Fatal("expected error for JWKS URL origin that does not match issuer")
	}
}

// TestNewVerifierRejectsNonHTTPIssuer confirms the issuer must be HTTPS.
func TestNewVerifierRejectsNonHTTPIssuer(t *testing.T) {
	if _, err := NewVerifier("http://clerk.example.com", "http://clerk.example.com/.well-known/jwks.json"); err == nil {
		t.Fatal("expected error for plain-HTTP issuer")
	}
}

// TestNewVerifierRejectsEmptyIssuer confirms the issuer is required.
func TestNewVerifierRejectsEmptyIssuer(t *testing.T) {
	if _, err := NewVerifier("", "https://clerk.example.com/.well-known/jwks.json"); err == nil {
		t.Fatal("expected error for empty issuer")
	}
}

type atomicValue struct {
	mu  sync.Mutex
	val interface{}
}

func (a *atomicValue) Set(v interface{}) {
	a.mu.Lock()
	a.val = v
	a.mu.Unlock()
}

func (a *atomicValue) Get() interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.val
}

// bigEndianUint32 encodes a uint32 as the 4-byte big-endian representation
// used by the JWK "e" parameter.
func bigEndianUint32(x uint32) []byte {
	b := make([]byte, 4)
	b[0] = byte(x >> 24)
	b[1] = byte(x >> 16)
	b[2] = byte(x >> 8)
	b[3] = byte(x)
	return b
}
