package reminders

import (
	"context"
	"errors"
	"testing"
	"time"

	"hireloop/api/internal/resend"
)

// These tests exercise the first-attempt payload freeze and the mapping-write
// failure path without a database. The database-backed counterparts live in
// security_store_regression_test.go.

func securityNotification(version int, title string) Notification {
	dueAt := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)
	return Notification{
		ID: "job-1", UserID: "owner-1", TaskID: "task-1",
		DueAt: dueAt, TrueDeadline: dueAt, ReminderTiming: TimingAtDeadline,
		Title: title, Version: version,
	}
}

// TestSecurityPayloadFrozenWhenTitleAndRecipientChange proves that a retry
// under the same idempotency key reuses the first attempt's exact bytes. A
// changed task title and a changed Clerk-verified address must not alter the
// body, which Resend would reject as an idempotency conflict (HTTP 409).
func TestSecurityPayloadFrozenWhenTitleAndRecipientChange(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{queue: []captureSenderEntry{
		{result: resend.Result{Status: resend.StatusRetryable}},
		{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-stable"}},
	}}
	emails := []string{"old-verified@example.com", "new-verified@example.com"}
	resolver := func(_ context.Context, _ string) (string, error) {
		email := emails[0]
		emails = emails[1:]
		return email, nil
	}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com", resolver)

	if out := dispatcher.Send(context.Background(), securityNotification(1, "Original title")); out != OutcomeFailed {
		t.Fatalf("first attempt = %q, want failed (retryable)", out)
	}
	changed := securityNotification(1, "A different title edited by the user")
	if out := dispatcher.Send(context.Background(), changed); out != OutcomeAccepted {
		t.Fatalf("retry = %q, want accepted", out)
	}
	if sender.calls() != 2 {
		t.Fatalf("send calls = %d, want 2", sender.calls())
	}
	if sender.keys[0] != sender.keys[1] {
		t.Fatalf("idempotency keys differ: %q vs %q", sender.keys[0], sender.keys[1])
	}
	if string(sender.bodies[0]) != string(sender.bodies[1]) {
		t.Fatalf("payload changed across retry:\nfirst  %s\nsecond %s", sender.bodies[0], sender.bodies[1])
	}
	if got, want := sender.msgs[1].To[0], "old-verified@example.com"; got != want {
		t.Fatalf("recipient = %q, want first-attempt %q", got, want)
	}
	if got, want := sender.msgs[1].Subject, "Hireloop reminder: Original title"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
}

// mappingFailStore injects a failure into the provider-mapping write while
// reusing the in-memory store for everything else.
type mappingFailStore struct {
	*dispatchStore
	fail bool
}

func (s *mappingFailStore) SetProviderEmailID(ctx context.Context, jobID string, version int, emailID string) error {
	if s.fail {
		return errors.New("injected provider mapping failure")
	}
	return s.dispatchStore.SetProviderEmailID(ctx, jobID, version, emailID)
}

// TestSecurityMappingFailureForcesRetry proves the mapping error is not
// swallowed: the dispatcher reports a retryable outcome so the worker tries
// again under the same key and Resend returns the same email id.
func TestSecurityMappingFailureForcesRetry(t *testing.T) {
	base := newDispatchStore("owner-1")
	store := &mappingFailStore{dispatchStore: base, fail: true}
	accepted := resend.Result{Status: resend.StatusAccepted, MessageID: "email-retried"}
	sender := &captureSender{queue: []captureSenderEntry{{result: accepted}, {result: accepted}}}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "verified@example.com", nil })
	n := securityNotification(1, "Interview")

	if out := dispatcher.Send(context.Background(), n); out != OutcomeFailed {
		t.Fatalf("outcome with failing mapping = %q, want failed (retry)", out)
	}
	if base.jobs["job-1"].ProviderEmailID != "" {
		t.Fatal("mapping was persisted despite injected failure")
	}
	store.fail = false
	if out := dispatcher.Send(context.Background(), n); out != OutcomeAccepted {
		t.Fatalf("outcome after retry = %q, want accepted", out)
	}
	if sender.keys[0] != sender.keys[1] {
		t.Fatalf("retry used a different idempotency key: %q vs %q", sender.keys[0], sender.keys[1])
	}
	if got := base.jobs["job-1"].ProviderEmailID; got != "email-retried" {
		t.Fatalf("provider mapping = %q, want email-retried", got)
	}
}
