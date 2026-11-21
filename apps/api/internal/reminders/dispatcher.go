package reminders

import (
	"context"
	"time"
)

// Dispatcher is the narrow boundary between the worker and an outbound channel
// (mail, SMS, push, …). The worker records every outcome as an honest
// delivery state: only OutcomeDelivered sets 'delivered', because no outcome
// other than a confirming adapter can prove delivery.
type Dispatcher interface {
	Send(ctx context.Context, n Notification) Outcome
}

// Outcome is what the dispatcher reports for one send.
type Outcome string

const (
	// OutcomeDelivered: the adapter confirmed delivery.
	OutcomeDelivered Outcome = "delivered"
	// OutcomeAccepted: the adapter accepted the send request; delivery is not confirmed.
	OutcomeAccepted Outcome = "accepted"
	// OutcomeFailed: a retryable failure; the worker retries with backoff.
	OutcomeFailed Outcome = "failed"
	// OutcomeUncertain: a timeout or ambiguous result; the send may or may not have happened.
	OutcomeUncertain Outcome = "uncertain"
	// OutcomePermanent: a permanent provider error (for example Resend
	// rejecting the recipient or the request). The worker records the
	// terminal 'permanent_failed' state and does not retry.
	OutcomePermanent Outcome = "permanent"
)

// Notification is one reminder as the dispatcher sees it. ID is the job id:
// the stable notification identity across retries and callbacks. Version is
// the job's delivery version when the dispatch was claimed: the idempotency
// key and payload are built from the job and version, so retries of one
// dispatch are byte-stable and a rescheduled job dispatches under a new
// version.
type Notification struct {
	ID     string
	UserID string
	TaskID string
	DueAt  time.Time
	Title  string
	Version int
}
