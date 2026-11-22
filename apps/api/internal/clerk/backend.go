package clerk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBackendBaseURL    = "https://api.clerk.com"
	backendTimeout           = 5 * time.Second
	maxBackendResponseBytes  = 1 << 20
	backendUserAgent         = "hireloop-api/1.0 (clerk-backend)"
	maxBackendSubjectBytes   = 256
	maxBackendEmailAddressBytes = 320
)

var (
	// ErrNoVerifiedEmail is returned when the subject has no verified
	// primary email address: the dispatcher records a permanent failure.
	ErrNoVerifiedEmail = errors.New("clerk: no verified primary email address")
	// ErrUserNotFound is returned when Clerk has no such user.
	ErrUserNotFound = errors.New("clerk: user not found")
)

// BackendClient calls Clerk's Backend API for verified primary emails. It is
// the server-only source of a user's verified primary email address at send
// time: session JWT email claims are never used for delivery, and no
// user-entered application address is used.
//
// Official reference:
//
//	https://clerk.com/docs/references/backend-api/get-user
type BackendClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewBackendClient returns a BackendClient for the given secret key
// (CLERK_SECRET_KEY), validated as a server-side secret value. The client
// never returns the key to callers and logs nothing.
func NewBackendClient(apiKey string) (*BackendClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("clerk: backend API key is required")
	}
	if hasControl(apiKey) || len(apiKey) > 1024 {
		return nil, errors.New("clerk: backend API key is invalid")
	}
	return &BackendClient{
		apiKey:  apiKey,
		baseURL: defaultBackendBaseURL,
		client:  &http.Client{Timeout: backendTimeout},
	}, nil
}

// backendUser is the Clerk Backend API v1 User object view needed to find the
// verified primary email address.
type backendUser struct {
	ID                    string                `json:"id"`
	Deleted               bool                  `json:"deleted"`
	PrimaryEmailAddressID string                `json:"primary_email_address_id"`
	EmailAddresses        []backendEmailAddress `json:"email_addresses"`
}

type backendEmailAddress struct {
	ID           string `json:"id"`
	EmailAddress string `json:"email_address"`
	IsPrimary    bool   `json:"is_primary"`
	Verification struct {
		Status string `json:"status"`
	} `json:"verification"`
}

// VerifiedPrimaryEmail returns the subject's verified primary email address
// from Clerk's Backend API, at call time. Errors distinguish permanent
// outcomes (user not found, or no verified primary email) from transient
// ones (Clerk API failures), so the dispatcher can decide retry versus
// terminal.
func (c *BackendClient) VerifiedPrimaryEmail(ctx context.Context, subject string) (string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", errors.New("clerk: subject required")
	}
	if len(subject) > maxBackendSubjectBytes || hasControl(subject) {
		return "", errors.New("clerk: subject is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/users/"+url.PathEscape(subject), nil)
	if err != nil {
		return "", fmt.Errorf("clerk: backend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", backendUserAgent)

	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("clerk: backend request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBackendResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("clerk: backend response: %w", err)
	}
	if res.StatusCode == http.StatusNotFound {
		return "", ErrUserNotFound
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clerk: backend request failed (HTTP %d)", res.StatusCode)
	}
	var user backendUser
	if err := json.Unmarshal(raw, &user); err != nil {
		return "", errors.New("clerk: backend response is not valid JSON")
	}
	if user.Deleted {
		return "", ErrUserNotFound
	}
	for _, address := range user.EmailAddresses {
		if address.Verification.Status != "verified" {
			continue
		}
		// A primary address is the one the API marks primary for this user;
		// anything else is not the verified primary email.
		if !address.IsPrimary && address.ID != user.PrimaryEmailAddressID {
			continue
		}
		if len(address.EmailAddress) == 0 || len(address.EmailAddress) > maxBackendEmailAddressBytes || hasControl(address.EmailAddress) {
			continue
		}
		return address.EmailAddress, nil
	}
	return "", ErrNoVerifiedEmail
}

// BanUser revokes all of the subject's sessions and blocks that exact Clerk
// identity from signing in again. A 404 means the user is already gone and is
// treated as success, so a retry after a completed deletion is harmless.
// No key, header or user payload is ever logged.
func (c *BackendClient) BanUser(ctx context.Context, subject string) error {
	return c.doUserAction(ctx, subject, http.MethodPost, "/ban")
}

// DeleteUser deletes the exact authenticated Clerk subject through the
// Backend API. A 404 means the user was already deleted, which is idempotent.
func (c *BackendClient) DeleteUser(ctx context.Context, subject string) error {
	return c.doUserAction(ctx, subject, http.MethodDelete, "")
}

// BanAndDelete performs both provider steps for account deletion. The verified
// JWT subject is the only target id; neither method accepts an email or any
// other person-level identifier.
func (c *BackendClient) BanAndDelete(ctx context.Context, subject string) error {
	if err := c.BanUser(ctx, subject); err != nil {
		return err
	}
	return c.DeleteUser(ctx, subject)
}

func (c *BackendClient) doUserAction(ctx context.Context, subject, method, suffix string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return errors.New("clerk: subject required")
	}
	if len(subject) > maxBackendSubjectBytes || hasControl(subject) {
		return errors.New("clerk: subject is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, method,
		c.baseURL+"/v1/users/"+url.PathEscape(subject)+suffix, nil)
	if err != nil {
		return fmt.Errorf("clerk: backend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", backendUserAgent)

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("clerk: backend request failed: %w", err)
	}
	defer res.Body.Close()
	// Drain only to allow connection reuse. The body is never parsed or logged:
	// it can contain user data, and only the HTTP outcome matters here.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 8<<10))
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusOK || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return fmt.Errorf("clerk: backend request failed (HTTP %d)", res.StatusCode)
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
