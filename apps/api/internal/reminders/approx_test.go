package reminders

import (
	"strings"
	"testing"
	"time"
)

// TestDateApproxSlotsFutureDeadline pins the two chosen local hours and the
// day-before calendar arithmetic for a normal future date-only deadline.
func TestDateApproxSlotsFutureDeadline(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	slots := DateApproxSlots("2026-11-21", "Asia/Kolkata", now)
	if len(slots) != 2 {
		t.Fatalf("slots = %+v, want two approximate reminders", slots)
	}
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		timing ReminderTiming
		day    string
		hour   string
	}{
		{TimingDayBeforeApprox, "2026-11-20", "18:00"},
		{TimingMorningOfApprox, "2026-11-21", "09:00"},
	}
	for i, expected := range want {
		got := slots[i].DueAt.In(loc).Format("2006-01-02 15:04")
		if got != expected.day+" "+expected.hour {
			t.Errorf("slot %d local time = %q, want %q", i, got, expected.day+" "+expected.hour)
		}
		if slots[i].Timing != expected.timing || slots[i].DueDay != "2026-11-21" || slots[i].Zone != "Asia/Kolkata" {
			t.Errorf("slot %d = %+v", i, slots[i])
		}
	}
}

// TestDateApproxSlotsAcrossDST verifies that both nudges stay on the intended
// calendar day and local hour across spring-forward, fall-back and a
// non-hour (30-minute) transition. The zone's own database is authoritative.
func TestDateApproxSlotsAcrossDST(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		zone   string
		day    string
		before string
	}{
		{"UK spring forward", "Europe/London", "2026-03-30", "2026-03-29"},
		{"UK fall back", "Europe/London", "2026-10-26", "2026-10-25"},
		{"Lord Howe 30-minute shift", "Australia/Lord_Howe", "2026-10-05", "2026-10-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			slots := DateApproxSlots(tc.day, tc.zone, now)
			if len(slots) != 2 {
				t.Fatalf("slots = %+v, want two", slots)
			}
			want := []struct{ day, hour string }{
				{tc.before, "18:00"},
				{tc.day, "09:00"},
			}
			for i, expected := range want {
				got := slots[i].DueAt.In(loc).Format("2006-01-02 15:04")
				if got != expected.day+" "+expected.hour {
					t.Errorf("slot %d local time = %q, want %q", i, got, expected.day+" "+expected.hour)
				}
			}
		})
	}
}

// TestDateApproxSlotsPastAndPartial verifies past deadlines never schedule a
// reminder, and a due date whose morning has not arrived yet keeps only that
// nudge. This is the "no invented cutoff, no past alarm" invariant.
func TestDateApproxSlotsPastAndPartial(t *testing.T) {
	zone := "UTC"
	cases := []struct {
		name  string
		day   string
		now   time.Time
		count int
	}{
		{"both in future", "2026-11-21", time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC), 2},
		{"day-before passed, morning pending", "2026-11-21", time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC), 1},
		{"both passed", "2026-11-21", time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC), 0},
		{"old date", "2020-01-01", time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slots := DateApproxSlots(tc.day, zone, tc.now)
			if len(slots) != tc.count {
				t.Fatalf("slots = %+v, want %d", slots, tc.count)
			}
			if tc.count == 1 && slots[0].Timing != TimingMorningOfApprox {
				t.Errorf("remaining slot = %+v, want morning-of", slots[0])
			}
		})
	}
}

func TestDateApproxSlotsRejectsInvalidInputs(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ day, zone string }{
		{"", "UTC"},
		{"not-a-date", "UTC"},
		{"2026-02-30", "UTC"},
		{"2026-11-21", ""},
		{"2026-11-21", "Not/AZone"},
		{"2026-11-21", "Local"},
	} {
		if slots := DateApproxSlots(tc.day, tc.zone, now); len(slots) != 0 {
			t.Errorf("day=%q zone=%q produced slots %+v, want none", tc.day, tc.zone, slots)
		}
	}
}

// TestApproxDeadlineGuard verifies the worker uses calendar days in the
// captured zone, not an invented cutoff instant, and fails closed on bad data.
func TestApproxDeadlineGuard(t *testing.T) {
	job := Job{DueDay: "2026-11-21", ReminderZone: "Asia/Kolkata", ReminderTiming: TimingMorningOfApprox}
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"morning of, still same local day", time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC), false},
		{"late evening of due day in UTC but same local day", time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC), false},
		{"next local day", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		if got := deadlinePassed(job, tc.now); got != tc.want {
			t.Errorf("%s: passed=%v, want %v", tc.name, got, tc.want)
		}
	}
	for _, bad := range []Job{
		{DueDay: "2026-11-21"},
		{DueDay: "bad", ReminderZone: "UTC"},
		{DueDay: "2026-11-21", ReminderZone: "Not/AZone"},
	} {
		if !deadlinePassed(bad, time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("job %+v must fail closed", bad)
		}
	}
}

// TestApproxEmailIsClearlyLabelled keeps the "approximate, not a guaranteed
// cutoff" wording in both the subject and the body, and makes sure no exact
// instant is rendered for a date-only deadline.
func TestApproxEmailIsClearlyLabelled(t *testing.T) {
	subject := buildReminderSubject("Submit form", TimingDayBeforeApprox)
	if !strings.HasPrefix(subject, "Hireloop approximate reminder:") {
		t.Errorf("subject = %q", subject)
	}
	body := buildReminderText("Submit form", time.Time{}, TimingDayBeforeApprox, "2026-11-21", "Asia/Kolkata")
	for _, fragment := range []string{
		"Hireloop approximate reminder", "2026-11-21", "date only",
		"day-before", "18:00 Asia/Kolkata", "not a guaranteed cutoff",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("body missing %q: %s", fragment, body)
		}
	}
	if strings.Contains(body, "T09:") || strings.Contains(body, "Z\n") {
		t.Errorf("approximate email must not render an exact instant: %s", body)
	}
	morning := buildReminderText("Submit form", time.Time{}, TimingMorningOfApprox, "2026-11-21", "Asia/Kolkata")
	if !strings.Contains(morning, "morning-of") || !strings.Contains(morning, "09:00 Asia/Kolkata") {
		t.Errorf("morning body = %s", morning)
	}
	if got := buildReminderSubject("Interview", Timing2hBefore); got != "Hireloop reminder: Interview" {
		t.Errorf("exact subject = %q", got)
	}
}
