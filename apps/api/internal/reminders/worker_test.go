package reminders

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"
)

func TestBackoffDoublesAndCaps(t *testing.T) {
	w := NewWorker(nil, Config{BackoffBase: 30 * time.Second, BackoffMax: 5 * time.Minute})
	want := []time.Duration{
		30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second,
		5 * time.Minute, 5 * time.Minute,
	}
	for i, d := range want {
		if got := w.backoff(i + 1); got != d {
			t.Fatalf("backoff(%d) = %s, want %s", i+1, got, d)
		}
	}
}

func TestApplyOutcomeMapsDeliveryStates(t *testing.T) {
	w := NewWorker(nil, Config{MaxRetries: 2, BackoffBase: time.Minute})
	job := Job{RetryCount: 0}
	if tr := w.applyOutcome(job, OutcomeDelivered); tr.State != StateDelivered {
		t.Fatalf("delivered transition = %+v", tr)
	}
	if tr := w.applyOutcome(job, OutcomeAccepted); tr.State != StateAccepted {
		t.Fatalf("accepted transition = %+v", tr)
	}
	tr := w.applyOutcome(job, OutcomeUncertain)
	if tr.State != StateUncertain || tr.RetryCount != 1 || tr.RetryAt == nil {
		t.Fatalf("uncertain transition = %+v", tr)
	}
	tr = w.applyOutcome(job, OutcomeFailed)
	if tr.State != StateFailed || tr.RetryCount != 1 || tr.RetryAt == nil {
		t.Fatalf("failed transition = %+v", tr)
	}
	// The failure that follows the retry limit moves the job to exhausted.
	job.RetryCount = 2
	tr = w.applyOutcome(job, OutcomeFailed)
	if tr.State != StateExhausted || tr.RetryAt != nil {
		t.Fatalf("exhausted transition = %+v", tr)
	}
	// Unknown outcomes behave like failures: honest, and retryable.
	if tr := w.applyOutcome(Job{}, Outcome("mystery")); tr.State != StateFailed {
		t.Fatalf("unknown outcome = %+v", tr)
	}
}

func TestWorkerRetriesPendingDeletionsWithoutDispatcher(t *testing.T) {
	// Pending account deletions must be retried by the worker even when email
	// sending is disabled (no Dispatcher), because keeping Hireloop access
	// blocked depends on Clerk deletion completing.
	calls := 0
	w := NewWorker(nil, Config{
		DeletionRetry: func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("clerk unavailable")
			}
			return 1, nil
		},
	})
	w.RetryDeletionsOnce(context.Background())
	if calls != 1 {
		t.Fatalf("retry calls = %d", calls)
	}
	w.RetryDeletionsOnce(context.Background())
	if calls != 2 {
		t.Fatalf("retry calls after recovery = %d", calls)
	}
}

func TestWorkerEmitsRedactedAccountDeletionAlertEvent(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	w := NewWorker(nil, Config{
		DeletionRetry: func(context.Context) (int, error) {
			return 0, errors.New("provider response contains sensitive details")
		},
	})
	w.RetryDeletionsOnce(context.Background())

	if got := output.String(); !strings.Contains(got, "event=account_deletion_retry_failed") {
		t.Fatalf("retry log %q does not contain the stable alert event", got)
	} else if strings.Contains(got, "sensitive details") {
		t.Fatalf("retry log leaked provider error details: %q", got)
	}
}

func TestWorkerDoesNotRetryPendingDeletionsOnEveryPollTick(t *testing.T) {
	calls := 0
	w := NewWorker(nil, Config{
		PollInterval:          time.Millisecond,
		DeletionRetryInterval: time.Second,
		DeletionRetry: func(context.Context) (int, error) {
			calls++
			return 0, errors.New("clerk unavailable")
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("deletion retries = %d, want only the startup attempt during rapid polling", calls)
	}
}

func TestWorkerWithoutDispatcherIsNotReady(t *testing.T) {
	// A missing provider: the worker idles instead of leasing or
	// exhausting jobs.
	if (NewWorker(nil, Config{})).Ready() {
		t.Fatal("worker without a dispatcher must not be ready")
	}
	if !(NewWorker(nil, Config{Dispatcher: &fakeDispatcher{}})).Ready() {
		t.Fatal("worker with a dispatcher must be ready")
	}
}

func TestConfigNormalizesDefaults(t *testing.T) {
	cfg := Config{}
	cfg.normalize()
	if cfg.PollInterval != time.Second || cfg.LeaseTTL != 30*time.Second ||
		cfg.MaxRetries != 5 || cfg.BackoffBase != 30*time.Second ||
		cfg.BackoffMax != 5*time.Minute || cfg.BatchSize != 25 {
		t.Fatalf("normalized config = %+v", cfg)
	}
}
