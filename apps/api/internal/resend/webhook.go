package resend

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWebhookTolerance = 5 * time.Minute // Svix/Svix's documented default.
	maxWebhookBodyBytes     = 1 << 20         // 1 MiB of raw body.
	maxSvixHeaderBytes      = 1024
	maxEmailIDBytes         = 256
	minSecretBytes          = 16
)

// EventType is the small set of Resend email events this building block
// understands. Unknown types are rejected so a new or forged event cannot be
// silently treated as a known delivery state.
type EventType string

const (
	EventScheduled    EventType = "email.scheduled"
	EventSent         EventType = "email.sent"
	EventDelivered    EventType = "email.delivered"
	EventDeliverySlow EventType = "email.delivery_delayed"
	EventBounced      EventType = "email.bounced"
	EventComplained   EventType = "email.complained"
	EventFailed       EventType = "email.failed"
	EventSuppressed   EventType = "email.suppressed"
	EventOpened       EventType = "email.opened"
	EventClicked      EventType = "email.clicked"
	EventReceived     EventType = "email.received"
)

var knownEventTypes = map[EventType]struct{}{
	EventScheduled: {}, EventSent: {}, EventDelivered: {}, EventDeliverySlow: {},
	EventBounced: {}, EventComplained: {}, EventFailed: {}, EventSuppressed: {},
	EventOpened: {}, EventClicked: {}, EventReceived: {},
}

// Event is the minimal, non-private view of a verified webhook. It deliberately
// excludes subject, sender, recipients, bounce diagnostics and the raw body:
// those are private payload and are not needed for delivery bookkeeping.
//
// ID is the svix-id header and is the replay identity. Svix reuses it for
// retries of the same message, so the caller MUST persist it with a unique
// constraint and ignore duplicates. Verification is stateless by design; it
// performs no deduplication of its own.
type Event struct {
	ID        string
	Type      EventType
	CreatedAt time.Time
	EmailID   string // data.email_id; matches Result.MessageID from Send.
}

// WebhookVerifier verifies Svix signatures over the raw request body. It makes
// no network calls, keeps no replay cache and retains no raw payload.
type WebhookVerifier struct {
	secrets   [][]byte
	tolerance time.Duration
	maxBody   int64
	now       func() time.Time
}

// NewWebhookVerifier accepts one or more Resend/Svix signing secrets to support
// rotation: a payload signed by any configured secret verifies. Secrets may be
// "whsec_<base64>" or bare base64 and must decode to at least 16 bytes.
func NewWebhookVerifier(secrets ...string) (*WebhookVerifier, error) {
	if len(secrets) == 0 {
		return nil, errors.New("resend: at least one webhook signing secret is required")
	}
	decoded := make([][]byte, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		key, err := decodeSecret(secret)
		if err != nil {
			return nil, err
		}
		if len(key) < minSecretBytes {
			return nil, errors.New("resend: webhook signing secret is too short")
		}
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		decoded = append(decoded, key)
	}
	return &WebhookVerifier{
		secrets:   decoded,
		tolerance: defaultWebhookTolerance,
		maxBody:   maxWebhookBodyBytes,
		now:       time.Now,
	}, nil
}

// SetTolerance overrides the timestamp tolerance (default 5 minutes). Call it
// before verifying; it is not safe to call concurrently with VerifyRequest.
func (v *WebhookVerifier) SetTolerance(d time.Duration) {
	if d > 0 {
		v.tolerance = d
	}
}

// DefaultWebhookTolerance is the timestamp tolerance used by default.
const DefaultWebhookTolerance = defaultWebhookTolerance

// VerifyRequest validates a webhook request and returns its minimal typed
// event. It reads r.Body directly so verification always uses the raw bytes;
// callers must not parse the body before calling this method. The context is
// checked before and after the bounded read; net/http also unblocks body reads
// when the request context is canceled.
func (v *WebhookVerifier) VerifyRequest(ctx context.Context, r *http.Request) (Event, error) {
	if v == nil || len(v.secrets) == 0 {
		return Event{}, errors.New("resend: webhook verifier is not configured")
	}
	if r == nil {
		return Event{}, errors.New("resend: nil webhook request")
	}
	if err := ctx.Err(); err != nil {
		return Event{}, fmt.Errorf("resend: webhook verification canceled: %w", err)
	}

	id := r.Header.Get("svix-id")
	timestamp := r.Header.Get("svix-timestamp")
	signature := r.Header.Get("svix-signature")
	if err := validateSvixHeader("svix-id", id); err != nil {
		return Event{}, err
	}
	if err := validateSvixHeader("svix-timestamp", timestamp); err != nil {
		return Event{}, err
	}
	if err := validateSvixHeader("svix-signature", signature); err != nil {
		return Event{}, err
	}

	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return Event{}, errors.New("resend: invalid svix timestamp")
	}
	delta := v.now().Sub(time.Unix(unix, 0))
	if delta < -v.tolerance || delta > v.tolerance {
		return Event{}, errors.New("resend: webhook timestamp outside tolerance")
	}

	if r.Body == nil {
		return Event{}, errors.New("resend: missing webhook body")
	}
	if r.ContentLength > v.maxBody {
		return Event{}, errors.New("resend: webhook body too large")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, v.maxBody+1))
	if err != nil {
		return Event{}, errors.New("resend: could not read webhook body")
	}
	if err := ctx.Err(); err != nil {
		return Event{}, fmt.Errorf("resend: webhook verification canceled: %w", err)
	}
	if int64(len(raw)) > v.maxBody {
		return Event{}, errors.New("resend: webhook body too large")
	}

	if err := v.checkSignature(id, timestamp, raw, signature); err != nil {
		return Event{}, err
	}
	return parseEvent(id, raw)
}

// checkSignature computes HMAC-SHA256 over "<id>.<timestamp>.<raw body>" with
// every configured secret and compares it in constant time with every v1
// signature in the space-delimited header. Any secret/signature match wins,
// which supports both signing-secret rotation and multiple simultaneous v1
// signatures. Other versions (e.g. v2) are ignored.
func (v *WebhookVerifier) checkSignature(id, timestamp string, body []byte, header string) error {
	signed := make([]byte, 0, len(id)+len(timestamp)+len(body)+2)
	signed = append(signed, id...)
	signed = append(signed, '.')
	signed = append(signed, timestamp...)
	signed = append(signed, '.')
	signed = append(signed, body...)

	expected := make([][]byte, len(v.secrets))
	for i, secret := range v.secrets {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(signed)
		expected[i] = []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}

	sawV1 := false
	for _, part := range strings.Fields(header) {
		version, provided, ok := strings.Cut(part, ",")
		if !ok || version != "v1" || provided == "" {
			continue
		}
		sawV1 = true
		for _, want := range expected {
			if subtle.ConstantTimeCompare(want, []byte(provided)) == 1 {
				return nil
			}
		}
	}
	if !sawV1 {
		return errors.New("resend: webhook has no v1 signature")
	}
	return errors.New("resend: webhook signature does not match")
}

func parseEvent(id string, raw []byte) (Event, error) {
	var wire struct {
		Type      string `json:"type"`
		CreatedAt string `json:"created_at"`
		Data      struct {
			EmailID string `json:"email_id"`
		} `json:"data"`
	}
	// Private fields present in the payload (subject, from, to, tags, bounce)
	// are ignored: json.Unmarshal does not retain them beyond this function.
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Event{}, errors.New("resend: webhook payload is not valid JSON")
	}
	eventType := EventType(wire.Type)
	if _, ok := knownEventTypes[eventType]; !ok {
		return Event{}, errors.New("resend: unsupported webhook event type")
	}
	created, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil {
		return Event{}, errors.New("resend: webhook created_at is invalid")
	}
	if wire.Data.EmailID == "" || len(wire.Data.EmailID) > maxEmailIDBytes || hasControl(wire.Data.EmailID) {
		return Event{}, errors.New("resend: webhook email_id is missing or invalid")
	}
	return Event{ID: id, Type: eventType, CreatedAt: created, EmailID: wire.Data.EmailID}, nil
}

func decodeSecret(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("resend: empty webhook signing secret")
	}
	encoded := strings.TrimPrefix(secret, "whsec_")
	if encoded == "" {
		return nil, errors.New("resend: webhook signing secret has no base64 payload")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, errors.New("resend: webhook signing secret is not valid base64")
	}
	return raw, nil
}

func validateSvixHeader(name, value string) error {
	if value == "" || len(value) > maxSvixHeaderBytes || hasControl(value) {
		return fmt.Errorf("resend: invalid or missing %s header", name)
	}
	return nil
}
