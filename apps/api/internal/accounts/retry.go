package accounts

import (
	"context"
	"fmt"
)

// ClerkDeleter is the provider capability the lifecycle needs: ban the exact
// authenticated subject (revoking its sessions and blocking sign-in) and then
// delete that user through Clerk's Backend API.
type ClerkDeleter interface {
	BanAndDelete(ctx context.Context, subject string) error
}

// PendingStore is the durable slice used by the retry loop. MarkAttempt is an
// atomic one-minute claim so multiple worker replicas cannot retry one subject
// at the same time.
type PendingStore interface {
	Pending(context.Context, int) ([]PendingDeletion, error)
	MarkAttempt(context.Context, string) (bool, error)
	Complete(context.Context, string) error
}

// PendingRetryBatchSize bounds time spent on Clerk requests in each worker
// cycle; failed subjects move behind untouched ones by updated_at.
const PendingRetryBatchSize = 2

// LifecycleStore is the full surface used by the HTTP API.
type LifecycleStore interface {
	PendingStore
	Delete(context.Context, string) (DeleteResult, error)
	Status(context.Context, string) (Status, error)
	Export(context.Context, string) (Export, error)
}

// AttemptDeletion erases local data, then tries Clerk once. A Clerk failure is
// not an HTTP error: the tombstone stays pending and the worker retries. Only
// a local persistence failure is returned as an error.
func AttemptDeletion(ctx context.Context, store LifecycleStore, clerk ClerkDeleter, subject string) (Status, error) {
	if _, err := store.Delete(ctx, subject); err != nil {
		return StatusPending, err
	}
	if clerk == nil {
		// Keep the tombstone pending even though local erasure succeeded.
		return StatusPending, nil
	}
	if err := clerk.BanAndDelete(ctx, subject); err != nil {
		return StatusPending, nil
	}
	if err := store.Complete(ctx, subject); err != nil {
		return StatusPending, nil
	}
	return StatusCompleted, nil
}

// RetryPending handles a bounded batch independently of email dispatch. A 404
// at Clerk is treated by the client as already deleted; transient failures
// remain pending and become eligible after the one-minute claim interval.
func RetryPending(ctx context.Context, store PendingStore, clerk ClerkDeleter) (int, error) {
	if clerk == nil {
		return 0, nil
	}
	pending, err := store.Pending(ctx, PendingRetryBatchSize)
	if err != nil {
		return 0, err
	}
	done := 0
	failed := 0
	for _, item := range pending {
		claimed, err := store.MarkAttempt(ctx, item.ClerkSubject)
		if err != nil {
			failed++
			continue
		}
		if !claimed {
			continue
		}
		if err := clerk.BanAndDelete(ctx, item.ClerkSubject); err != nil {
			failed++
			continue
		}
		if err := store.Complete(ctx, item.ClerkSubject); err != nil {
			failed++
			continue
		}
		done++
	}
	if failed > 0 {
		// The count lets the worker report outstanding work without leaking
		// subjects or provider response details into logs.
		return done, fmt.Errorf("%d pending account deletion(s) failed", failed)
	}
	return done, nil
}
