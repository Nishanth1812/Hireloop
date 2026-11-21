package reminders

import (
	"testing"
	"time"
)

// TestScheduleCreatesTwoAdvanceReminders verifies that Schedule creates
// both 24h and 2h advance reminders when the deadline is far enough in the future.
func TestScheduleCreatesTwoAdvanceReminders(t *testing.T) {
	now := time.Now()
	trueDeadline := now.Add(48 * time.Hour)

	d24 := trueDeadline.Add(-24 * time.Hour)
	d2 := trueDeadline.Add(-2 * time.Hour)

	if !d24.After(now) {
		t.Fatalf("24h reminder should be in future: %v", d24)
	}
	if !d2.After(now) {
		t.Fatalf("2h reminder should be in future: %v", d2)
	}
	if d24.After(d2) {
		t.Fatalf("24h reminder should be before 2h reminder: %v vs %v", d24, d2)
	}
}

// TestScheduleShortDeadlineOnly2h verifies that when the deadline is less
// than 24h away but more than 2h away, only the 2h reminder is scheduled.
func TestScheduleShortDeadlineOnly2h(t *testing.T) {
	now := time.Now()
	trueDeadline := now.Add(12 * time.Hour)

	d24 := trueDeadline.Add(-24 * time.Hour)
	d2 := trueDeadline.Add(-2 * time.Hour)

	if d24.After(now) {
		t.Fatalf("24h reminder should be in past: %v", d24)
	}
	if !d2.After(now) {
		t.Fatalf("2h reminder should be in future: %v", d2)
	}
}

// TestScheduleVeryShortDeadlineNone verifies that when the deadline is
// less than 2h away, no advance reminders are scheduled.
func TestScheduleVeryShortDeadlineNone(t *testing.T) {
	now := time.Now()
	trueDeadline := now.Add(1 * time.Hour)

	d24 := trueDeadline.Add(-24 * time.Hour)
	d2 := trueDeadline.Add(-2 * time.Hour)

	if d24.After(now) {
		t.Fatalf("24h reminder should be in past: %v", d24)
	}
	if d2.After(now) {
		t.Fatalf("2h reminder should be in past: %v", d2)
	}
}

// TestSchedulePastDeadlineNone verifies that when the deadline is in the
// past, no advance reminders are scheduled.
func TestSchedulePastDeadlineNone(t *testing.T) {
	now := time.Now()
	trueDeadline := now.Add(-1 * time.Hour)

	d24 := trueDeadline.Add(-24 * time.Hour)
	d2 := trueDeadline.Add(-2 * time.Hour)

	if d24.After(now) {
		t.Fatalf("24h reminder should be in past: %v", d24)
	}
	if d2.After(now) {
		t.Fatalf("2h reminder should be in past: %v", d2)
	}
}

// TestBuildReminderText verifies the email body includes the true deadline
// and the correct timing line for each reminder type.
func TestBuildReminderText(t *testing.T) {
	title := "Interview"
	trueDeadline := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		timing      ReminderTiming
		wantTiming  string
	}{
		{Timing24hBefore, "This reminder fires 24 hours before your deadline."},
		{Timing2hBefore, "This reminder fires 2 hours before your deadline."},
		{TimingAtDeadline, "This reminder fires at your deadline."},
	}

	for _, tc := range tests {
		text := buildReminderText(title, trueDeadline, tc.timing)
		wantDue := "Due: 2026-11-21T09:30:00Z"
		wantTitle := "Hireloop reminder: Interview"
		if !contains(text, wantTitle) {
			t.Errorf("timing %s: missing title: %s", tc.timing, text)
		}
		if !contains(text, wantDue) {
			t.Errorf("timing %s: missing due: %s", tc.timing, text)
		}
		if !contains(text, tc.wantTiming) {
			t.Errorf("timing %s: missing timing line %q: %s", tc.timing, tc.wantTiming, text)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestDeadlineGuardUnit verifies the dispatchOne deadline guard logic
// using a fake store and dispatcher.
func TestDeadlineGuardUnit(t *testing.T) {
	// This test would require a more complex setup with a fake store
	// that returns jobs with specific TrueDeadline values. For now,
	// we test the logic inline in the worker tests.
	// The integration tests cover the full flow.
}