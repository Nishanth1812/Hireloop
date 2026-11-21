// Package resend implements the two building blocks Hireloop needs for Resend:
// an outbound email sender and an inbound Svix-signed webhook verifier.
//
// Both are dependency-free (standard library only) so they can be reviewed and
// tested offline. Nothing in this package sends email on its own: a send occurs
// only when Sender.Send is called explicitly by the caller.
//
// Official references:
//
//	https://resend.com/docs/api-reference/emails/send-email
//	https://resend.com/docs/dashboard/emails/idempotency-keys
//	https://resend.com/docs/api-reference/errors
//	https://resend.com/docs/webhooks/verify-webhooks-requests
//	https://docs.svix.com/receiving/verifying-payloads/how-manual
package resend

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// apiEndpoint is fixed. The sender never accepts a destination URL from a
	// caller, so it cannot be used to reach internal services (SSRF).
	apiEndpoint = "https://api.resend.com/emails"
	userAgent   = "hireloop-api/1.0 (resend-sender)"

	defaultSendTimeout   = 10 * time.Second
	maxAPIKeyBytes       = 1024
	maxIdempotencyBytes  = 256 // Resend's documented limit.
	maxResponseBytes     = 1 << 14
	maxMessageIDBytes    = 256
	maxFromBytes         = 500
	maxToBytes           = 320
	maxSubjectBytes      = 1000
	maxMessageBodyBytes  = 1 << 20
	maxRecipients        = 50 // Resend's documented limit.
	maxRetryAfterSeconds = 3600
)

// Status classifies the outcome of one send attempt. It is explicit so callers
// never have to guess from an error string.
//
// Mapping to reminders.Outcome (parent integrates this):
//
//	StatusAccepted  -> OutcomeAccepted (request accepted; delivery not confirmed)
//	StatusRetryable -> OutcomeFailed (retry with backoff)
//	StatusPermanent -> handle separately: do not retry until config/body is fixed
//	StatusAmbiguous -> OutcomeUncertain (retry, but delivery may already have happened)
//
// StatusPermanent has no faithful reminders.Outcome value because that worker
// retries every failure; the caller must suppress or alert on it instead.
type Status string

const (
	// StatusAccepted means Resend returned 2xx with a usable email id. This is
	// acceptance of the request, not proof of delivery.
	StatusAccepted Status = "accepted"
	// StatusRetryable means the request can be retried as-is. Always retry with
	// the same idempotency key: Resend deduplicates it for 24 hours.
	StatusRetryable Status = "retryable"
	// StatusPermanent means retrying unchanged cannot succeed (bad key, bad
	// payload, idempotency key reused with a different body, ...).
	StatusPermanent Status = "permanent"
	// StatusAmbiguous means the outcome is unknown. A timeout or a 2xx whose
	// body could not be parsed lands here: the email may or may not have been
	// sent. Retrying with the same key is still safe, but delivery must not be
	// claimed.
	StatusAmbiguous Status = "ambiguous"
)

// Result is the outcome of one Send call. MessageID is populated only for
// StatusAccepted. RetryAfter is populated only when the provider supplied a
// usable Retry-After header (currently only on 429).
type Result struct {
	Status     Status
	MessageID  string
	RetryAfter time.Duration
}

// Message is the minimal outbound email. Fields map directly to Resend's
// POST /emails body. At least one of Text or HTML must be set.
type Message struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
}

// Sender posts to Resend's fixed /emails endpoint. It is safe for concurrent
// use; all configuration is fixed at construction.
type Sender struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// NewSender validates configuration and returns a sender. A zero timeout
// selects DefaultSendTimeout; a negative timeout is rejected. TLS verification is always on and
// redirects are refused, so no call can be redirected to another host.
func NewSender(apiKey string, timeout time.Duration) (*Sender, error) {
	if err := validateAPIKey(apiKey); err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("resend: timeout must not be negative")
	}
	if timeout == 0 {
		timeout = defaultSendTimeout
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	return &Sender{
		apiKey:   apiKey,
		endpoint: apiEndpoint,
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			// A redirect could move the request (and the API key) to another
			// host, so the fixed endpoint is never followed off-origin.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// DefaultSendTimeout is the timeout used when NewSender is given a
// zero timeout.
const DefaultSendTimeout = defaultSendTimeout

// Send sends one email. idempotencyKey is caller-provided and must be stable
// across retries of the same logical email (1-256 characters, Resend's limit).
// The same key with a different body is rejected by Resend, so callers must
// keep the Message identical when retrying.
//
// A non-nil error is returned for every status other than StatusAccepted; the
// authoritative classification is Result.Status. Errors contain only status
// codes and fixed text: the API key and response bodies are never included.
func (s *Sender) Send(ctx context.Context, idempotencyKey string, msg Message) (Result, error) {
	if s == nil {
		return Result{Status: StatusPermanent}, errors.New("resend: nil sender")
	}
	if err := ctx.Err(); err != nil {
		// No request was built, so nothing can have been sent.
		return Result{Status: StatusRetryable}, fmt.Errorf("resend: context ended before send: %w", err)
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Result{Status: StatusPermanent}, err
	}
	if err := validateMessage(msg); err != nil {
		return Result{Status: StatusPermanent}, err
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return Result{Status: StatusPermanent}, errors.New("resend: could not encode message")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Status: StatusRetryable}, errors.New("resend: could not build request")
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Idempotency-Key", idempotencyKey)

	res, err := s.client.Do(req)
	if err != nil {
		return classifyTransportError(ctx, err)
	}
	defer res.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if readErr != nil || len(raw) > maxResponseBytes {
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return Result{Status: StatusAmbiguous}, errors.New("resend: response unreadable after acceptance; outcome unknown")
		}
		return Result{Status: StatusRetryable}, errors.New("resend: response unreadable")
	}

	return classifyResponse(res, raw)
}

func classifyTransportError(ctx context.Context, err error) (Result, error) {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// A timeout may mean the request reached Resend and was processed.
		// Never downgrade this to a clean "accepted" or plain retryable.
		return Result{Status: StatusAmbiguous}, fmt.Errorf("resend: request timed out; delivery outcome is unknown: %w", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return Result{Status: StatusAmbiguous}, fmt.Errorf("resend: request ended before a response was received: %w", err)
	}
	// A connection can fail after the request was processed, even without a timeout.
	return Result{Status: StatusAmbiguous}, errors.New("resend: transport failure; delivery outcome is unknown")
}

func classifyResponse(res *http.Response, raw []byte) (Result, error) {
	code := res.StatusCode
	if code >= 200 && code < 300 {
		var accepted struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &accepted); err != nil ||
			accepted.ID == "" || len(accepted.ID) > maxMessageIDBytes || hasControl(accepted.ID) {
			return Result{Status: StatusAmbiguous}, errors.New("resend: request accepted but response had no usable email id")
		}
		return Result{Status: StatusAccepted, MessageID: accepted.ID}, nil
	}

	// Error bodies are read only to refine a 409. Nothing from the body is
	// echoed into an error, so a hostile or broken response cannot leak data.
	var detail struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &detail)

	switch {
	case code == 408:
		return Result{Status: StatusRetryable}, errors.New("resend: provider timed out the request (HTTP 408)")
	case code == 409:
		if detail.Type == "invalid_idempotent_request" {
			return Result{Status: StatusPermanent}, errors.New("resend: idempotency key reused with a different payload (HTTP 409)")
		}
		// concurrent_idempotent_requests and unknown 409s are transient and
		// safe to retry with the same key.
		return Result{Status: StatusRetryable}, errors.New("resend: idempotency conflict in progress (HTTP 409)")
	case code == 429:
		return Result{Status: StatusRetryable, RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"))},
			errors.New("resend: rate limited or quota exceeded (HTTP 429)")
	case code >= 500:
		return Result{Status: StatusRetryable}, fmt.Errorf("resend: provider error (HTTP %d)", code)
	case code >= 400 && code < 500:
		return Result{Status: StatusPermanent}, fmt.Errorf("resend: request rejected (HTTP %d)", code)
	default:
		// Includes 3xx (redirects are refused by the transport).
		return Result{Status: StatusPermanent}, fmt.Errorf("resend: unexpected response (HTTP %d)", code)
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		if secs > maxRetryAfterSeconds {
			return maxRetryAfterSeconds * time.Second
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		if d > maxRetryAfterSeconds*time.Second {
			return maxRetryAfterSeconds * time.Second
		}
		return d
	}
	return 0
}

func validateAPIKey(key string) error {
	if key == "" {
		return errors.New("resend: API key is required")
	}
	if len(key) > maxAPIKeyBytes {
		return errors.New("resend: API key is too long")
	}
	if hasControl(key) {
		return errors.New("resend: API key contains invalid characters")
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > maxIdempotencyBytes {
		return errors.New("resend: idempotency key must be 1-256 characters")
	}
	if hasControl(key) {
		return errors.New("resend: idempotency key contains invalid characters")
	}
	return nil
}

func validateMessage(msg Message) error {
	if msg.From == "" || len(msg.From) > maxFromBytes || hasControl(msg.From) {
		return errors.New("resend: invalid from address")
	}
	if len(msg.To) == 0 || len(msg.To) > maxRecipients {
		return errors.New("resend: to must contain 1-50 recipients")
	}
	for _, to := range msg.To {
		if to == "" || len(to) > maxToBytes || hasControl(to) {
			return errors.New("resend: invalid recipient address")
		}
	}
	if msg.Subject == "" || len(msg.Subject) > maxSubjectBytes || hasControl(msg.Subject) {
		return errors.New("resend: invalid subject")
	}
	if msg.Text == "" && msg.HTML == "" {
		return errors.New("resend: text or html is required")
	}
	if len(msg.Text)+len(msg.HTML) > maxMessageBodyBytes {
		return errors.New("resend: message body is too large")
	}
	return nil
}

// hasControl rejects CR/LF and DEL, which would allow header injection either
// here or downstream when Resend builds the MIME message.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
