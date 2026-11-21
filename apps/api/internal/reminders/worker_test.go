package reminders

import (
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

func TestNullDispatcherReportsFailure(t *testing.T) {
	if got := (NullDispatcher{}).Send(nil, Notification{}); got != OutcomeFailed {
		t.Fatalf("NullDispatcher = %q, want %q", got, OutcomeFailed)
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
