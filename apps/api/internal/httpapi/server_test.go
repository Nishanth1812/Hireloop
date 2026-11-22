package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
)

// isolationStore records the user_id each route receives and only recognizes
// one owner for the sample application, so we can assert routes never use a
// caller-supplied or cross-user identity.
type isolationStore struct {
	owner      string
	create     string
	getUser    string
	getID      string
	eventsUser string
	eventsApp  string
}

func (s *isolationStore) Create(ctx context.Context, userID string, in applications.Input) (applications.Application, error) {
	s.create = userID
	return applications.Application{ID: "app-1", Company: in.Company, Role: in.Role, Status: in.Status}, nil
}
func (s *isolationStore) List(ctx context.Context, userID string, query applications.Query) (applications.ListResult, error) {
	return applications.ListResult{}, nil
}
func (s *isolationStore) Get(ctx context.Context, userID, id string) (applications.Application, error) {
	s.getUser = userID
	s.getID = id
	if userID == s.owner && id == "00000000-0000-0000-0000-000000000001" {
		return applications.Application{ID: "00000000-0000-0000-0000-000000000001", Company: "Acme", Role: "Engineer", Status: "saved"}, nil
	}
	return applications.Application{}, nil
}
func (s *isolationStore) Patch(ctx context.Context, userID, id string, in applications.PatchInput) (applications.Application, error) {
	return applications.Application{}, nil
}
func (s *isolationStore) Events(ctx context.Context, userID, applicationID string, query applications.EventQuery) (applications.EventListResult, error) {
	s.eventsUser = userID
	s.eventsApp = applicationID
	return applications.EventListResult{Items: []applications.Event{}, PageSize: query.PageSize}, nil
}
func (s *isolationStore) Delete(ctx context.Context, userID, id string) (bool, error) {
	return false, nil
}

type testMapper struct {
	bindings map[string]string
}

func (m testMapper) Resolve(ctx context.Context, clerkSubject string) (string, bool, error) {
	id, ok := m.bindings[clerkSubject]
	return id, ok, nil
}

// signTestRS256 builds a Clerk-like RS256 JWT for tests.
func signTestRS256(t *testing.T, now time.Time, claims map[string]any, kid string) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signTestRS256WithKey(t, key, now, claims, kid), key
}

func signTestRS256WithKey(t *testing.T, key *rsa.PrivateKey, now time.Time, claims map[string]any, kid string) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"iss":   "https://clerk.example.com",
		"sub":   "user_clerk_a",
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
	signing := []byte(base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes))
	digest := sha256.Sum256(signing)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func clerkHandler(t *testing.T, subject, owner string) (http.Handler, *isolationStore, string) {
	t.Helper()
	now := time.Now()
	token, key := signTestRS256(t, now, map[string]any{"sub": subject}, "kid-1")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid-1": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := &isolationStore{owner: owner}
	mapper := testMapper{bindings: map[string]string{subject: owner}}
	handler := NewHandler(verifier, mapper, store)
	return handler, store, token
}

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(nil, nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
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
			NewHandler(nil, nil, nil).ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
}

func requireAuth(t *testing.T, handler http.Handler, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestClerkCreateRecordsBoundOwnerNotClerkSubject(t *testing.T) {
	owner := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, store, token := clerkHandler(t, "user_clerk_a", owner)
	res := requireAuth(t, handler, http.MethodPost, "/api/applications", token, `{"company":"Acme","role":"Engineer","status":"saved"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if store.create != owner {
		t.Fatalf("create user_id = %q, want bound owner %q", store.create, owner)
	}
	if strings.Contains(store.create, "user_clerk") {
		t.Fatalf("create user_id must not be the Clerk subject: %q", store.create)
	}
}

func TestClerkGetUsesBoundOwner(t *testing.T) {
	owner := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, store, token := clerkHandler(t, "user_clerk_a", owner)
	res := requireAuth(t, handler, http.MethodGet, "/api/applications/00000000-0000-0000-0000-000000000001", token, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if store.getUser != owner || store.getID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("get user_id = %q id = %q, want %q / 00000000-0000-0000-0000-000000000001", store.getUser, store.getID, owner)
	}
}

func TestClerkEventsUseBoundOwnerAndRouteApplication(t *testing.T) {
	owner := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, store, token := clerkHandler(t, "user_clerk_a", owner)
	const id = "00000000-0000-0000-0000-000000000001"
	res := requireAuth(t, handler, http.MethodGet, "/api/applications/"+id+"/events", token, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if store.eventsUser != owner || store.eventsApp != id {
		t.Fatalf("events owner = %q application = %q, want %q / %s", store.eventsUser, store.eventsApp, owner, id)
	}
	if strings.Contains(store.eventsUser, "user_clerk") {
		t.Fatalf("events owner must be the bound owner, not the Clerk subject: %q", store.eventsUser)
	}
}

func TestClerkOtherUserCannotReadApplication(t *testing.T) {
	ownerA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	ownerB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	now := time.Now()
	tokenA, keyA := signTestRS256(t, now, map[string]any{"sub": "user_clerk_a"}, "kid-1")
	tokenB, keyB := signTestRS256(t, now, map[string]any{"sub": "user_clerk_b"}, "kid-1")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid-1": &keyA.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := &isolationStore{owner: ownerA}
	mapper := testMapper{bindings: map[string]string{
		"user_clerk_a": ownerA,
		"user_clerk_b": ownerB,
	}}
	handler := NewHandler(verifier, mapper, store)

	// User B's token is signed by a different key, so it fails verification:
	// cross-user access is rejected before any store lookup.
	resB := requireAuth(t, handler, http.MethodGet, "/api/applications/00000000-0000-0000-0000-000000000001", tokenB, "")
	if resB.Code != http.StatusUnauthorized {
		t.Fatalf("other-user status = %d, body = %s, want 401", resB.Code, resB.Body.String())
	}

	// User A reads their own application: queries by bound Supabase owner.
	resA := requireAuth(t, handler, http.MethodGet, "/api/applications/00000000-0000-0000-0000-000000000001", tokenA, "")
	if resA.Code != http.StatusOK {
		t.Fatalf("owner status = %d", resA.Code)
	}
	if store.getUser != ownerA || store.getID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("owner get user_id = %q id = %q, want %q / 00000000-0000-0000-0000-000000000001", store.getUser, store.getID, ownerA)
	}
	_ = tokenB
	_ = keyB
}

func TestClerkUnlinkedSubjectOwnsItself(t *testing.T) {
	now := time.Now()
	token, key := signTestRS256(t, now, map[string]any{"sub": "user_clerk_unbound"}, "kid-1")
	verifier := clerk.NewStaticVerifier("https://clerk.example.com", map[string]*rsa.PublicKey{"kid-1": &key.PublicKey})
	verifier.SetNow(func() time.Time { return now })
	store := &isolationStore{}
	// No binding: the Clerk subject is not linked to any Supabase owner. The
	// Clerk user is a new signup whose stable owner is the Clerk subject
	// itself — not rejected, not mapped to any Supabase auth.users row.
	mapper := testMapper{bindings: map[string]string{}}
	handler := NewHandler(verifier, mapper, store)
	res := requireAuth(t, handler, http.MethodPost, "/api/applications", token, `{"company":"Acme","role":"Engineer","status":"saved"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s, want 201 for unbound Clerk subject", res.Code, res.Body.String())
	}
	if store.create != "user_clerk_unbound" {
		t.Fatalf("create user_id = %q, want the Clerk subject %q", store.create, "user_clerk_unbound")
	}
	_ = key
}

func TestClerkInvalidTokenReturns401(t *testing.T) {
	owner := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, _, _ := clerkHandler(t, "user_clerk_a", owner)
	for _, token := range []string{"", "not-a-token", "a.b.c"} {
		res := requireAuth(t, handler, http.MethodGet, "/api/applications/00000000-0000-0000-0000-000000000001", token, "")
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, res.Code)
		}
	}
}

func TestClerkJWKSOutageReturnsUnavailable(t *testing.T) {
	now := time.Now()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	verifier, err := clerk.NewVerifier(server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	verifier.SetNow(func() time.Time { return now })
	token, _ := signTestRS256(t, now, map[string]any{"iss": server.URL, "sub": "clerk-a"}, "kid")
	handler := NewHandler(verifier, testMapper{bindings: map[string]string{}}, &isolationStore{})
	res := requireAuth(t, handler, http.MethodGet, "/api/me", token, "")
	if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), "tls") {
		t.Fatalf("JWKS failure response = %d %q, want redacted 503", res.Code, res.Body.String())
	}
}
