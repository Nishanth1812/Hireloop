package reminders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"hireloop/api/internal/resend"
)

// dispatchStore implements the two store methods the Resend dispatcher uses
// (Get and SetProviderEmailID), in memory, without a database.
type dispatchStore struct {
	mu       sync.Mutex
	jobs     map[string]Job
	payloads map[string]Payload
}

func payloadKey(jobID string, version int) string {
	return fmt.Sprintf("%s:v%d", jobID, version)
}

func (s *dispatchStore) GetDeliveryPayload(_ context.Context, jobID string, version int) (Payload, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payloads[payloadKey(jobID, version)]
	return p, ok, nil
}

func (s *dispatchStore) SaveDeliveryPayload(_ context.Context, jobID string, version int, p Payload) (Payload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := payloadKey(jobID, version)
	if existing, ok := s.payloads[k]; ok {
		return existing, nil
	}
	s.payloads[k] = p
	return p, nil
}

func (s *dispatchStore) Get(_ context.Context, userID, id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok || job.UserID != userID {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (s *dispatchStore) SetProviderEmailID(_ context.Context, jobID string, version int, emailID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[jobID]; ok {
		job.ProviderEmailID = emailID
		job.ProviderVersion = version
		s.jobs[jobID] = job
	}
	return nil
}

// captureSender returns a queue of (Result, error) pairs and records every
// call's key and marshaled message.
type captureSender struct {
	mu     sync.Mutex
	queue  []captureSenderEntry
	keys   []string
	msgs   []resend.Message
	bodies [][]byte
}

type captureSenderEntry struct {
	result resend.Result
	err    error
}

func (s *captureSender) Send(_ context.Context, key string, msg resend.Message) (resend.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	s.msgs = append(s.msgs, msg)
	body, _ := json.Marshal(msg)
	s.bodies = append(s.bodies, body)
	if len(s.queue) == 0 {
		return resend.Result{Status: resend.StatusPermanent}, errors.New("captureSender: empty queue")
	}
	entry := s.queue[0]
	s.queue = s.queue[1:]
	return entry.result, entry.err
}

func (s *captureSender) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

func newDispatchStore(owner string) *dispatchStore {
	return &dispatchStore{
		payloads: map[string]Payload{},
		jobs: map[string]Job{
			"job-1": {ID: "job-1", UserID: owner, TaskID: "task-1", DueAt: time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC), DeliveryVersion: 1},
		},
	}
}

func TestResendDispatcherAcceptsAndPersistsProviderEmailID(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{queue: []captureSenderEntry{{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-abc"}}}}
	resolverCalls := 0
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com", func(_ context.Context, ownerID string) (string, error) {
		resolverCalls++
		if ownerID != "owner-1" {
			t.Errorf("resolver owner = %q, want owner-1", ownerID)
		}
		return "student@example.com", nil
	})

	outcome := dispatcher.Send(context.Background(), Notification{
		ID: "job-1", UserID: "owner-1", TaskID: "task-1",
		DueAt: store.jobs["job-1"].DueAt, Title: "Interview", Version: 1,
	})
	if outcome != OutcomeAccepted {
		t.Fatalf("outcome = %q, want accepted", outcome)
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
	// The id is per version: "job:<id>:v<version>".
	if got, want := sender.keys[0], "job:job-1:v1"; got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
	job := store.jobs["job-1"]
	if job.ProviderEmailID != "email-abc" || job.ProviderVersion != 1 {
		t.Fatalf("provider mapping = %q@%d, want email-abc@1", job.ProviderEmailID, job.ProviderVersion)
	}
	// The payload is byte-stable from the versioned job fields: deterministic
	// from job id, version, owner, task, due instant and title.
	msg := sender.msgs[0]
	if msg.From != "hireloop@example.com" || len(msg.To) != 1 || msg.To[0] != "student@example.com" {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Subject != "Hireloop reminder: Interview" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	wantText := "Hireloop reminder: Interview\nDue: 2026-11-21T09:30:00Z\n"
	if msg.Text != wantText {
		t.Fatalf("text = %q, want %q", msg.Text, wantText)
	}
}

func TestResendDispatcherPayloadIsByteStableAcrossRetries(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{queue: []captureSenderEntry{
		{result: resend.Result{Status: resend.StatusRetryable}},
		{result: resend.Result{Status: resend.StatusRetryable}},
		{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-abc"}},
	}}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "student@example.com", nil })
	n := Notification{ID: "job-1", UserID: "owner-1", TaskID: "task-1", DueAt: store.jobs["job-1"].DueAt, Title: "Interview", Version: 1}

	for i, want := range []Outcome{OutcomeFailed, OutcomeFailed, OutcomeAccepted} {
		if outcome := dispatcher.Send(context.Background(), n); outcome != want {
			t.Fatalf("attempt %d outcome = %q, want %q", i, outcome, want)
		}
	}
	for i := 1; i < len(sender.keys); i++ {
		if sender.keys[i] != sender.keys[0] {
			t.Fatalf("idempotency key %d = %q, want %q", i, sender.keys[i], sender.keys[0])
		}
		if string(sender.bodies[i]) != string(sender.bodies[0]) {
			t.Fatalf("payload %d = %q, want byte-identical %q", i, sender.bodies[i], sender.bodies[0])
		}
	}
}

func TestResendDispatcherMapsProviderStatuses(t *testing.T) {
	for _, tc := range []struct {
		status resend.Status
		want   Outcome
	}{
		{resend.StatusAccepted, OutcomeAccepted},
		{resend.StatusRetryable, OutcomeFailed},
		{resend.StatusAmbiguous, OutcomeUncertain},
		{resend.StatusPermanent, OutcomePermanent},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			store := newDispatchStore("owner-1")
			sender := &captureSender{queue: []captureSenderEntry{{result: resend.Result{Status: tc.status}}}}
			dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
				func(_ context.Context, _ string) (string, error) { return "student@example.com", nil })
			outcome := dispatcher.Send(context.Background(), Notification{ID: "job-1", UserID: "owner-1", Version: 1})
			if outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", outcome, tc.want)
			}
		})
	}
}

func TestResendDispatcherResolverFailures(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "", ErrNoVerifiedEmail })
	if outcome := dispatcher.Send(context.Background(), Notification{ID: "job-1", UserID: "owner-1", Version: 1}); outcome != OutcomePermanent {
		t.Fatalf("no verified email: outcome = %q, want %q", outcome, OutcomePermanent)
	}

	dispatcher = NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "", errors.New("clerk: backend request failed") })
	if outcome := dispatcher.Send(context.Background(), Notification{ID: "job-1", UserID: "owner-1", Version: 1}); outcome != OutcomeFailed {
		t.Fatalf("backend failure: outcome = %q, want %q", outcome, OutcomeFailed)
	}
	if sender.calls() != 0 {
		t.Fatalf("sender called despite resolver failure")
	}
}

func TestResendDispatcherRejectsStaleVersion(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "student@example.com", nil })
	// The job's current version is 1; a dispatch claimed at version 0 must
	// not reuse the old version's key.
	outcome := dispatcher.Send(context.Background(), Notification{ID: "job-1", UserID: "owner-1", Version: 0})
	if outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeFailed)
	}
	if sender.calls() != 0 {
		t.Fatalf("sender called with a stale version")
	}
}

func TestResendDispatcherNilIsPermanent(t *testing.T) {
	var dispatcher *ResendDispatcher
	if outcome := dispatcher.Send(context.Background(), Notification{}); outcome != OutcomePermanent {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomePermanent)
	}
}

// TestProviderMappingUniquePerJobVersion documents that the in-memory
// dispatchStore does not enforce the unique (job_id, delivery_version)
// constraint - that is enforced by the database via migration 011. The
// database will reject a second provider_email_id for the same job/version.
func TestProviderMappingUniquePerJobVersion(t *testing.T) {
	store := newDispatchStore("owner-1")
	sender := &captureSender{queue: []captureSenderEntry{{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-1"}}}}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, _ string) (string, error) { return "student@example.com", nil })

	// First send for version 1
	if out := dispatcher.Send(context.Background(), securityNotification(1, "Title")); out != OutcomeAccepted {
		t.Fatalf("first send = %q, want accepted", out)
	}
	if store.jobs["job-1"].ProviderEmailID != "email-1" || store.jobs["job-1"].ProviderVersion != 1 {
		t.Fatalf("first mapping = %q@%d, want email-1@1", store.jobs["job-1"].ProviderEmailID, store.jobs["job-1"].ProviderVersion)
	}

	// Simulate a second send for the same version with a different email id
	// (this would happen if Resend returned a different email id for the same
	// idempotency key, which shouldn't happen in practice). The in-memory
	// store allows this (no unique constraint on job_id+version), but the
	// real database enforces it via migration 011's unique index.
	sender.queue = []captureSenderEntry{{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-2"}}}
	if out := dispatcher.Send(context.Background(), securityNotification(1, "Title")); out != OutcomeAccepted {
		t.Fatalf("second send = %q, want accepted", out)
	}
	// In-memory store updates to the new email id; database would reject.
	if store.jobs["job-1"].ProviderEmailID != "email-2" {
		t.Fatalf("in-memory store email id = %q, want email-2 (database would differ)", store.jobs["job-1"].ProviderEmailID)
	}
	// The history table in the in-memory store is not checked here; the
	// integration test TestProviderMappingUniqueness verifies the database
	// constraint.
}
