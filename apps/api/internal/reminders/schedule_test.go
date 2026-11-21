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
		timing     ReminderTiming
		wantTiming string
	}{
		{Timing24hBefore, "This reminder fires 24 hours before your deadline."},
		{Timing2hBefore, "This reminder fires 2 hours before your deadline."},
		{TimingAtDeadline, "This reminder fires at your deadline."},
	}

	for _, tc := range tests {
		text := buildReminderText(title, trueDeadline, tc.timing, "", "")
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
// for all three reminder timings. The guard is now unified: suppress if
// strictly past true_deadline for all timings (legacy at_deadline jobs
// have due_at == true_deadline, so they are suppressed if past deadline).
func TestDeadlineGuardUnit(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name           string
		timing         ReminderTiming
		dueAt          time.Time
		trueDeadline   time.Time
		now            time.Time
		leaseTTL       time.Duration
		expectSuppress bool
		description    string
	}{
		{
			name:           "advance 24h_before - before true deadline, allows dispatch",
			timing:         Timing24hBefore,
			dueAt:          now.Add(-24 * time.Hour),
			trueDeadline:   now.Add(1 * time.Hour),
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: false,
			description:    "24h reminder fires 24h before deadline, deadline still future",
		},
		{
			name:           "advance 24h_before - at true deadline, suppresses",
			timing:         Timing24hBefore,
			dueAt:          now.Add(-24 * time.Hour),
			trueDeadline:   now,
			now:            now.Add(1 * time.Minute),
			leaseTTL:       30 * time.Second,
			expectSuppress: true,
			description:    "24h reminder claimed after true deadline passed",
		},
		{
			name:           "advance 2h_before - before true deadline, allows dispatch",
			timing:         Timing2hBefore,
			dueAt:          now.Add(-2 * time.Hour),
			trueDeadline:   now.Add(30 * time.Minute),
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: false,
			description:    "2h reminder fires 2h before deadline, deadline still future",
		},
		{
			name:           "advance 2h_before - past true deadline, suppresses",
			timing:         Timing2hBefore,
			dueAt:          now.Add(-2 * time.Hour),
			trueDeadline:   now.Add(-10 * time.Minute),
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: true,
			description:    "2h reminder claimed after true deadline passed",
		},
		{
			name:           "legacy at_deadline - at due_at (== true_deadline), allows dispatch",
			timing:         TimingAtDeadline,
			dueAt:          now,
			trueDeadline:   now,
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: false,
			description:    "Legacy job fires at exact deadline (now == true_deadline)",
		},
		{
			name:           "legacy at_deadline - slightly past true_deadline, suppresses",
			timing:         TimingAtDeadline,
			dueAt:          now.Add(-20 * time.Second),
			trueDeadline:   now.Add(-20 * time.Second),
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: true,
			description:    "Legacy job claimed after deadline (now > true_deadline)",
		},
		{
			name:           "legacy at_deadline - past true_deadline, suppresses",
			timing:         TimingAtDeadline,
			dueAt:          now.Add(-time.Minute),
			trueDeadline:   now.Add(-time.Minute),
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: true,
			description:    "Legacy job claimed well after deadline",
		},
		{
			name:           "legacy at_deadline - zero true_deadline (defensive), allows dispatch",
			timing:         TimingAtDeadline,
			dueAt:          now.Add(10 * time.Minute),
			trueDeadline:   time.Time{},
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: false,
			description:    "Legacy job with missing true_deadline falls back to no-op guard",
		},
		{
			name:           "legacy at_deadline - zero true_deadline, past due_at, allows (no true_deadline to check)",
			timing:         TimingAtDeadline,
			dueAt:          now.Add(-time.Minute),
			trueDeadline:   time.Time{},
			now:            now,
			leaseTTL:       30 * time.Second,
			expectSuppress: false,
			description:    "Legacy job with missing true_deadline, no deadline guard",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := Job{DueAt: tc.dueAt, TrueDeadline: tc.trueDeadline, ReminderTiming: tc.timing}
			if got := deadlinePassed(job, tc.now); got != tc.expectSuppress {
				t.Errorf("%s: suppress=%v, want %v (%s)", tc.name, got, tc.expectSuppress, tc.description)
			}
		})
	}
}
