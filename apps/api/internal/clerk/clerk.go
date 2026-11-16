// Package clerk verifies Clerk session JWTs server-side.
// It is intentionally dependency-free (standard library only) so token
// verification can be tested and run without network access to Clerk.
//
// Verification follows Clerk's official session-token contract:
//
//	https://clerk.com/docs/guides/sessions/session-tokens
//	https://clerk.com/docs/guides/sessions/manual-jwt-verification
//
// Clerk session tokens are RS256 JWTs issued by the instance's Frontend API.
// Their default claims do not include an audience: the app ID is not the
// audience and is never required here. The `azp` claim names the origin the
// token was minted for and is checked against the configured authorized
// parties (origins) when present. The issuer is the instance's Frontend API
// origin. Signature, expiry (exp) and not-before (nbf) are always verified.
//
// The official Clerk Go SDK (github.com/clerk/clerk-sdk-go) can replace this
// package when the module registry is reachable; this implementation keeps the
// API dependency-free and fully testable offline.
package clerk

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable is returned when Clerk's JWKS cannot be reached or parsed.
// Callers distinguish it from an invalid token: unavailable maps to 503, an
// invalid token maps to 401 (fail closed).
var ErrUnavailable = errors.New("clerk: verification unavailable")

// User is a verified Clerk identity.
//
// ID is the Clerk subject (sub, e.g. "user_2..."). For new Clerk signups the
// subject itself is the stable owner; existing ownership is preserved through
// the explicit Clerk -> Supabase mapping in the identities package.
type User struct {
	ID    string
	Email string
}

// Verifier checks a Clerk session JWT's signature, issuer, expiry and
// authorized party.
//
// AuthorizedParties is an allowlist of origins matched against the token's
// `azp` claim. When empty, a present `azp` is still not enforced unless
// RequireAuthorizedParty is set, matching Clerk's default behavior (the check
// applies when the claim is present; an absent `azp` can be skipped).
type Verifier struct {
	issuer  string
	jwksURL string
	client  *http.Client

	// AuthorizedParties are the origins permitted to mint tokens for this
	// service, checked against the token's `azp` claim.
	AuthorizedParties []string
	// RequireAuthorizedParty rejects tokens carrying no `azp` claim. Defaults
	// to false.
	RequireAuthorizedParty bool

	// staticKeys, when set, resolves signing keys without network access.
	// Used by tests; production uses fetchJWKS.
	staticKeys map[string]*rsa.PublicKey

	keys      map[string]*rsa.PublicKey
	keyLoaded map[string]time.Time
	keysMu    sync.RWMutex

	now       func() time.Time
	clockSkew time.Duration

	// refreshInterval is the minimum time between JWKS fetches. It bounds the
	// fetch rate so invalid or rotated-key tokens cannot trigger unbounded
	// network requests. Zero disables the interval.
	refreshInterval time.Duration
	// keyTTL is the maximum age of a cached signing key before it is refreshed.
	// Keys older than keyTTL are re-fetched and replaced, so revoked keys are
	// not retained indefinitely. Zero disables automatic aging.
	keyTTL time.Duration

	// lastFetch and lastFetchErr track the most recent ordinary refresh attempt.
	lastFetch    time.Time
	lastFetchErr error
	// lastSigFailFetch is the time of the last forced JWKS refresh triggered
	// by a signature failure. It bounds forced refreshes.
	lastSigFailFetch time.Time
}

// NewVerifier returns a verifier that fetches Clerk's JWKS from jwksURL
// (typically the instance Frontend API followed by /.well-known/jwks.json).
// issuer is the Frontend API origin and is required.
//
// NewVerifier fails closed on a misconfigured instance. issuer and jwksURL
// must both be HTTPS URLs sharing the same origin (scheme + host). This
// prevents SSRF through an operator- or attacker-controlled CLERK_JWKS_URL
// (for example a loopback or cloud metadata address) and enforces that
// signing keys are only fetched from the origin that minted the tokens.
func NewVerifier(issuer, jwksURL string) (*Verifier, error) {
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("clerk: invalid issuer %q: %w", issuer, err)
	}
	if issuerURL.Scheme != "https" || issuerURL.Host == "" {
		return nil, fmt.Errorf("clerk: issuer must be an HTTPS origin, got %q", issuer)
	}

	jwksURLParsed, err := url.Parse(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("clerk: invalid JWKS URL %q: %w", jwksURL, err)
	}
	if jwksURLParsed.Scheme != "https" {
		return nil, fmt.Errorf("clerk: JWKS URL must use HTTPS, got %q", jwksURL)
	}
	if jwksURLParsed.Host != issuerURL.Host {
		return nil, fmt.Errorf("clerk: JWKS URL origin %q does not match issuer origin %q", jwksURL, issuer)
	}

	return &Verifier{
		issuer:          issuer,
		jwksURL:         jwksURL,
		client:          &http.Client{Timeout: 5 * time.Second},
		now:             time.Now,
		clockSkew:       0,
		refreshInterval: time.Minute,
		keyTTL:          time.Hour,
	}, nil
}

// SetKeyRefresh configures the JWKS refresh interval and cached-key retention.
// interval bounds how often the JWKS endpoint is polled; ttl bounds how long a
// cached signing key is trusted before being re-fetched and replaced. Zero
// disables the corresponding bound. Intended to be called before verification
// begins; it is not safe to call concurrently with Verify.
func (v *Verifier) SetKeyRefresh(interval, ttl time.Duration) {
	v.refreshInterval = interval
	v.keyTTL = ttl
}

// NewStaticVerifier returns a verifier that resolves signing keys from the
// given kid->public key map without any network access. Intended for tests.
func NewStaticVerifier(issuer string, keys map[string]*rsa.PublicKey) *Verifier {
	return &Verifier{
		issuer:     issuer,
		staticKeys: keys,
		now:        time.Now,
		clockSkew:  0,
	}
}

// SetNow replaces the clock used for exp/nbf checks. Intended for tests.
func (v *Verifier) SetNow(f func() time.Time) {
	v.now = f
}

// SetClockSkew sets the tolerance applied to exp/nbf to absorb small clock
// differences between hosts. Zero means strict validation.
func (v *Verifier) SetClockSkew(d time.Duration) {
	v.clockSkew = d
}

// Verify parses and validates token, returning the Clerk identity on success.
// It fails closed: any verification problem returns an error and no user.
//
// ErrUnavailable is returned when the JWKS cannot be obtained, so callers can
// respond 503 instead of 401 for a transient verification failure.
func (v *Verifier) Verify(ctx context.Context, token string) (User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return User{}, errors.New("clerk: malformed token")
	}

	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return User{}, err
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return User{}, errors.New("clerk: invalid token header")
	}
	if header.Alg != "RS256" || header.Typ != "JWT" || header.Kid == "" {
		return User{}, errors.New("clerk: unsupported token header")
	}

	payloadBytes, err := decodeSegment(parts[1])
	if err != nil {
		return User{}, err
	}
	var payload struct {
		Iss   string `json:"iss"`
		Exp   int64  `json:"exp"`
		Nbf   int64  `json:"nbf"`
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Azp   string `json:"azp"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return User{}, errors.New("clerk: invalid token payload")
	}

	if payload.Iss != v.issuer {
		return User{}, errors.New("clerk: issuer mismatch")
	}

	now := v.now()
	skew := int64(v.clockSkew / time.Second)
	// Clerk session tokens always carry an expiry. A missing or zero exp means
	// the token never expires, which is not acceptable for a session token:
	// reject it rather than accepting a token with no lifetime bound.
	if payload.Exp == 0 || now.Unix() >= payload.Exp-skew {
		return User{}, errors.New("clerk: token expired")
	}
	if payload.Nbf != 0 && now.Unix()+skew < payload.Nbf {
		return User{}, errors.New("clerk: token not yet valid")
	}
	if payload.Sub == "" {
		return User{}, errors.New("clerk: missing subject")
	}

	if err := v.checkAuthorizedParty(payload.Azp); err != nil {
		return User{}, err
	}

	signingBytes := []byte(parts[0] + "." + parts[1])
	sigBytes, err := decodeSegment(parts[2])
	if err != nil {
		return User{}, err
	}
	digest := sha256.Sum256(signingBytes)

	key, err := v.publicKey(ctx, header.Kid)
	if err != nil {
		return User{}, err
	}
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sigBytes); err != nil {
		// The signing key may have rotated since it was cached. Refresh Clerk's
		// JWKS once and retry before rejecting the token. Forced refreshes are
		// gated by lastSigFailFetch so repeated bad tokens cannot trigger
		// unbounded fetches.
		if v.staticKeys == nil {
			if refreshed, ferr := v.forceFetchJWKS(ctx); ferr == nil {
				if key, ok := refreshed[header.Kid]; ok &&
					rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sigBytes) == nil {
					return User{ID: payload.Sub, Email: payload.Email}, nil
				}
			}
		}
		return User{}, errors.New("clerk: signature verification failed")
	}

	return User{ID: payload.Sub, Email: payload.Email}, nil
}

// checkAuthorizedParty validates the token's azp claim against the configured
// authorized parties (origins). An absent azp is allowed unless
// RequireAuthorizedParty is set; a present azp must be listed.
func (v *Verifier) checkAuthorizedParty(azp string) error {
	if azp == "" {
		if v.RequireAuthorizedParty {
			return errors.New("clerk: missing authorized party")
		}
		return nil
	}
	if len(v.AuthorizedParties) == 0 {
		return nil
	}
	for _, party := range v.AuthorizedParties {
		if party == azp {
			return nil
		}
	}
	return errors.New("clerk: unauthorized party")
}

// publicKey returns the RSA public key for kid, using staticKeys when
// provided and otherwise fetching Clerk's JWKS. A missing key after a
// successful JWKS fetch is an invalid token, not an unavailable error.
//
// A cached key is served while it is within its retention window (keyTTL).
// Stale or missing keys trigger a JWKS refresh that is bounded by
// refreshInterval, so invalid tokens cannot cause unbounded fetches. A
// successful fetch replaces the cache entirely, evicting revoked or rotated
// keys that are no longer present in JWKS.
func (v *Verifier) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if v.staticKeys != nil {
		if key, ok := v.staticKeys[kid]; ok {
			return key, nil
		}
		return nil, errors.New("clerk: unknown signing key")
	}

	now := v.now()
	// Serve a cached key still within its retention window.
	v.keysMu.RLock()
	key, cached := v.keys[kid]
	loaded, loadedOk := v.keyLoaded[kid]
	v.keysMu.RUnlock()
	if cached && loadedOk && (v.keyTTL <= 0 || now.Sub(loaded) < v.keyTTL) {
		return key, nil
	}

	// Stale or missing: refresh the JWKS cache (bounded by the refresh
	// interval) and use the replacement key. A stale cached key is not used
	// when the refresh fails: accepting a key past its TTL could allow a
	// revoked or rotated key to be trusted indefinitely. Fail closed instead.
	fetched, _, err := v.refreshJWKS(ctx)
	if err != nil {
		return nil, err
	}
	if key, ok := fetched[kid]; ok {
		return key, nil
	}
	return nil, errors.New("clerk: unknown signing key")
}

// refreshJWKS fetches Clerk's JWKS and replaces the cached keys, honoring the
// minimum refresh interval. If a fetch is not yet due and keys are cached, the
// cached keys are returned without a network request. A successful fetch
// replaces the cache entirely, so keys removed from JWKS (revoked or rotated)
// are evicted. The returned boolean reports whether a network fetch occurred.
func (v *Verifier) refreshJWKS(ctx context.Context) (map[string]*rsa.PublicKey, bool, error) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	now := v.now()
	if !v.lastFetch.IsZero() && now.Sub(v.lastFetch) < v.refreshInterval {
		if v.lastFetchErr != nil {
			return nil, false, v.lastFetchErr
		}
		if v.keyTTL <= 0 || now.Sub(v.lastFetch) < v.keyTTL {
			return v.keys, false, nil
		}
	}
	v.lastFetch = now
	fetched, err := v.fetchJWKS(ctx)
	if err != nil {
		v.lastFetchErr = err
		return nil, false, err
	}
	v.lastFetchErr = nil
	now = v.now()
	v.keys = fetched
	if v.keyLoaded == nil {
		v.keyLoaded = make(map[string]time.Time, len(fetched))
	}
	for kid := range fetched {
		v.keyLoaded[kid] = now
	}
	v.lastFetch = now
	return fetched, true, nil
}

// forceFetchJWKS refreshes the JWKS cache regardless of the refresh interval.
// It is used once after a signature failure to pick up a rotated signing key.
// The lock gates refresh attempts, including failures, across all callers.
func (v *Verifier) forceFetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	now := v.now()
	if !v.lastSigFailFetch.IsZero() && now.Sub(v.lastSigFailFetch) < v.refreshInterval {
		return v.keys, nil
	}
	v.lastSigFailFetch = now
	fetched, err := v.fetchJWKS(ctx)
	if err != nil {
		return nil, err
	}
	now = v.now()
	v.keys = fetched
	if v.keyLoaded == nil {
		v.keyLoaded = make(map[string]time.Time, len(fetched))
	}
	for kid := range fetched {
		v.keyLoaded[kid] = now
	}
	v.lastFetch = now
	v.lastFetchErr = nil
	return fetched, nil
}

func (v *Verifier) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	res, err := v.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, ErrUnavailable
	}
	var jwks struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, ErrUnavailable
	}
	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, jw := range jwks.Keys {
		key, err := jw.publicKey()
		if err != nil || key == nil {
			continue
		}
		if jw.Kid != "" {
			keys[jw.Kid] = key
		}
	}
	if len(keys) == 0 {
		return nil, ErrUnavailable
	}
	return keys, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (j jwk) publicKey() (*rsa.PublicKey, error) {
	if j.Kty != "RSA" || j.N == "" || j.E == "" {
		return nil, errors.New("clerk: jwk is not an RSA public key")
	}
	nBytes, err := decodeSegment(j.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := decodeSegment(j.E)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if n.Sign() <= 0 || e <= 0 {
		return nil, errors.New("clerk: invalid RSA parameters")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

// decodeSegment base64url-decodes a JWT segment, repairing padding.
func decodeSegment(s string) ([]byte, error) {
	if len(s)%4 != 0 {
		s += strings.Repeat("=", 4-len(s)%4)
	}
	return base64.URLEncoding.DecodeString(s)
}
