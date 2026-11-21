package reminders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"hireloop/api/internal/resend"
)

// approximateLabel returns the human label for an approximate date-only
// reminder and whether this timing is approximate.
func approximateLabel(timing ReminderTiming) (string, bool) {
	switch timing {
	case TimingDayBeforeApprox:
		return "day-before", true
	case TimingMorningOfApprox:
		return "morning-of", true
	}
	return "", false
}

// buildReminderSubject labels approximate reminders in the subject line so a
// recipient can tell a nudge from an exact-deadline warning before opening it.
func buildReminderSubject(title string, timing ReminderTiming) string {
	if _, ok := approximateLabel(timing); ok {
		return "Hireloop approximate reminder: " + title
	}
	return "Hireloop reminder: " + title
}

// buildReminderText constructs the email body for one reminder. Exact jobs
// show the stored instant and their advance interval. Approximate jobs show
// only the date the user gave and say plainly that the local-hour nudge is
// approximate and not a guaranteed cutoff; no cutoff time is invented.
func buildReminderText(title string, trueDeadline time.Time, timing ReminderTiming, dueDay, zone string) string {
	if label, ok := approximateLabel(timing); ok {
		hour := ApproxMorningLocalHour
		if timing == TimingDayBeforeApprox {
			hour = ApproxDayBeforeLocalHour
		}
		if zone == "" {
			zone = "your local zone"
		}
		return fmt.Sprintf("Hireloop approximate reminder: %s\nDeadline: %s (date only — cutoff time was not specified)\nThis is an approximate %s reminder around %02d:00 %s. It is not a guaranteed cutoff.\n",
			title, dueDay, label, hour, zone)
	}
	var timingLine string
	switch timing {
	case Timing24hBefore:
		timingLine = "This reminder fires 24 hours before your deadline."
	case Timing2hBefore:
		timingLine = "This reminder fires 2 hours before your deadline."
	case TimingAtDeadline:
		timingLine = "This reminder fires at your deadline."
	default:
		timingLine = "This is a reminder for your upcoming deadline."
	}
	return fmt.Sprintf("Hireloop reminder: %s\nDue: %s\n%s\n", title, trueDeadline.UTC().Format(time.RFC3339), timingLine)
}

// dispatcherStore is the store surface the Resend dispatcher uses. *Store
// implements it; tests inject in-memory fakes.
type dispatcherStore interface {
	Get(ctx context.Context, userID, id string) (Job, error)
	SetProviderEmailID(ctx context.Context, jobID string, version int, emailID string) error
	GetDeliveryPayload(ctx context.Context, jobID string, version int) (Payload, bool, error)
	SaveDeliveryPayload(ctx context.Context, jobID string, version int, p Payload) (Payload, error)
	GetDeliveryPayloadWithTimestamp(ctx context.Context, jobID string, version int) (Payload, time.Time, bool, error)
}

// ResendDispatcher implements Dispatcher over the existing resend adapter
// (apps/api/internal/resend). The verified primary email is resolved on the
// first attempt of a version through an OwnerEmailResolver (Clerk Backend API
// in production), persisted with the full message, and reused byte-for-byte
// on retries. The provider email id Resend returns is persisted through the
// store so provider callbacks and the webhook route can correlate events by
// email id.
type ResendDispatcher struct {
	store   dispatcherStore
	sender  resendSend
	from    string
	resolve OwnerEmailResolver
}

// resendSend is the narrow send boundary over the resend adapter.
type resendSend interface {
	Send(ctx context.Context, idempotencyKey string, msg resend.Message) (resend.Result, error)
}

// NewResendDispatcher builds a dispatcher that sends through sender, sending
// from from, resolving each owner's verified primary email through resolve.
func NewResendDispatcher(store dispatcherStore, sender resendSend, from string, resolve OwnerEmailResolver) *ResendDispatcher {
	return &ResendDispatcher{store: store, sender: sender, from: from, resolve: resolve}
}

// maxIdempotencyAge is Resend's documented idempotency key retention window.
// If a retry occurs after this window, the key has expired and Resend will
// generate a new email id, breaking correlation. We treat this as uncertain
// rather than silently losing correlation or failing.
const maxIdempotencyAge = 24 * time.Hour

func (d *ResendDispatcher) Send(ctx context.Context, n Notification) Outcome {
	if d == nil || d.sender == nil || d.resolve == nil {
		return OutcomePermanent
	}
	job, err := d.store.Get(ctx, n.UserID, n.ID)
	if err != nil || job.DeliveryVersion != n.Version {
		// The job was reactivated since the worker claimed it: sending
		// under the stale version would reuse an old idempotency key.
		return OutcomeFailed
	}
	// The first attempt of a version fixes the complete payload: the verified
	// primary email is resolved once and the message is persisted. Every retry
	// reuses those exact bytes, so a changed task title or a changed Clerk
	// recipient cannot turn a same-key retry into an idempotency conflict.
	p, createdAt, ok, err := d.store.GetDeliveryPayloadWithTimestamp(ctx, job.ID, job.DeliveryVersion)
	if err != nil {
		return OutcomeFailed
	}
	isRetry := ok
	if !ok {
		to, err := d.resolve(ctx, job.UserID)
		if err != nil {
			if errors.Is(err, ErrNoVerifiedEmail) {
				return OutcomePermanent
			}
			return OutcomeFailed
		}
		p = Payload{
			Recipient: to,
			Subject:   buildReminderSubject(n.Title, n.ReminderTiming),
			Text:      buildReminderText(n.Title, n.TrueDeadline, n.ReminderTiming, n.DueDay, n.ReminderZone),
		}
		p, err = d.store.SaveDeliveryPayload(ctx, job.ID, job.DeliveryVersion, p)
		if err != nil {
			return OutcomeFailed
		}
		// First attempt: record the time for idempotency expiry checks on retries.
		createdAt = time.Now()
	}
	// If this is a retry and the first attempt was more than 24h ago, the
	// Resend idempotency key has likely expired. Retrying with the same key
	// would generate a new provider email id, breaking correlation. Be honest:
	// return uncertain so the job stays retryable and a webhook can resolve it.
	if isRetry && time.Since(createdAt) > maxIdempotencyAge {
		return OutcomeUncertain
	}
	// The id is per version: a reschedule bumps the version, so the
	// rescheduled job's dispatches use new keys while retries of this
	// dispatch keep this one.
	key := fmt.Sprintf("job:%s:v%d", job.ID, job.DeliveryVersion)
	msg := resend.Message{
		From:    d.from,
		To:      []string{p.Recipient},
		Subject: p.Subject,
		Text:    p.Text,
	}
	result, _ := d.sender.Send(ctx, key, msg)
	if result.Status == resend.StatusAccepted {
		// The mapping write also reconciles webhooks that arrived early. Do
		// not swallow its error: retry under the same key so Resend returns
		// the same email id and the mapping (and reconciliation) is retried.
		if err := d.store.SetProviderEmailID(ctx, job.ID, job.DeliveryVersion, result.MessageID); err != nil {
			return OutcomeFailed
		}
		return OutcomeAccepted
	}
	switch result.Status {
	case resend.StatusRetryable:
		return OutcomeFailed
	case resend.StatusAmbiguous:
		return OutcomeUncertain
	default: // StatusPermanent
		return OutcomePermanent
	}
}
