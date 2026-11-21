package reminders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"hireloop/api/internal/resend"
)

// ResendDispatcher implements Dispatcher over the existing resend adapter
// (apps/api/internal/resend). The verified primary email is resolved at send
// time through an OwnerEmailResolver (Clerk Backend API in production); the
// provider email id Resend returns is persisted through the store so
// provider callbacks and the webhook route can correlate events by email id.
type ResendDispatcher struct {
	store   *Store
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
func NewResendDispatcher(store *Store, sender resendSend, from string, resolve OwnerEmailResolver) *ResendDispatcher {
	return &ResendDispatcher{store: store, sender: sender, from: from, resolve: resolve}
}

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
	to, err := d.resolve(ctx, job.UserID)
	if err != nil {
		if errors.Is(err, ErrNoVerifiedEmail) {
			return OutcomePermanent
		}
		return OutcomeFailed
	}
	// The id is per version: a reschedule bumps the version, so the
	// rescheduled job's dispatches use new keys while retries of this
	// dispatch keep this one.
	key := fmt.Sprintf("job:%s:v%d", job.ID, job.DeliveryVersion)
	// The payload is built from the versioned job fields so it is
	// byte-stable across retries of the same version.
	msg := resend.Message{
		From:    d.from,
		To:      []string{to},
		Subject: "Hireloop reminder: " + n.Title,
		Text:    "Hireloop reminder: " + n.Title + "\nDue: " + n.DueAt.UTC().Format(time.RFC3339) + "\n",
	}
	result, _ := d.sender.Send(ctx, key, msg)
	if result.Status == resend.StatusAccepted {
		// The provider accepted the send; persist the email id so provider
		// events correlate to this job and version. The write is guarded by
		// the lease and version, so the acceptance of a stale dispatch can
		// never overwrite the mapping of a newer version.
		_ = d.store.SetProviderEmailID(ctx, job.ID, job.DeliveryVersion, result.MessageID)
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
