package accounts

import "context"

// ClerkDeleter is the provider capability the lifecycle needs: ban the exact
// authenticated subject (revoking its sessions and blocking sign-in) and then
// delete that user through Clerk's Backend API.
type ClerkDeleter interface {
	BanAndDelete(ctx context.Context, subject string) error
}

// PendingStore is the durable slice used by the retry loop.
type PendingStore interface {
	Pending(context.Context) ([]PendingDeletion, error)
	Complete(context.Context, string) error
}

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
		// Fail closed: without the provider capability we must never report
		// completion, even though local erasure succeeded.
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

// RetryPending is called by the worker on every tick, independently of email
// dispatch. A 404 at Clerk is treated by the client as already deleted; a
// transient failure simply leaves the record pending for the next tick.
func RetryPending(ctx context.Context, store PendingStore, clerk ClerkDeleter) (int, error) {
	if clerk == nil {
		return 0, nil
	}
	pending, err := store.Pending(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, item := range pending {
		if err := clerk.BanAndDelete(ctx, item.ClerkSubject); err != nil {
			continue
		}
		if err := store.Complete(ctx, item.ClerkSubject); err != nil {
			continue
		}
		done++
	}
	return done, nil
}
