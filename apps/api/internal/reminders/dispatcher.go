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
)

// Notification is one reminder as the dispatcher sees it. ID is the job id:
// the stable notification identity across retries and callbacks.
type Notification struct {
	ID     string
	UserID string
	TaskID string
	DueAt  time.Time
	Title  string
}

// NullDispatcher is the placeholder used until a mail provider is integrated.
// It reports failure so honest delivery states stay at 'failed'/'exhausted';
// it never implies delivery.
type NullDispatcher struct{}

func (NullDispatcher) Send(context.Context, Notification) Outcome { return OutcomeFailed }
