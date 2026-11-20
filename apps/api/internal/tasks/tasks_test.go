package tasks

import (
	"testing"
	"time"
)

const appID = "11111111-1111-4111-8111-111111111111"

func TestDeadlinePrecisionIsValidatedAndKeptDistinct(t *testing.T) {
	tests := []struct {
		name  string
		input Input
		valid bool
	}{
		{"no deadline", Input{ApplicationID: appID, Title: "Reply to recruiter"}, true},
		{"no deadline with stray time", Input{ApplicationID: appID, Title: "Reply", DueLocal: "2026-11-12T18:00"}, false},
		{"date-only", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12"}, true},
		{"date-only with a time", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12T18:00"}, false},
		{"date-only with a zone", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12", TimeZone: "Asia/Kolkata"}, false},
		{"impossible date", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-02-30"}, false},
		{"exact instant", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata"}, true},
		{"instant without zone", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00"}, false},
		{"instant without wall time", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, TimeZone: "Asia/Kolkata"}, false},
		{"instant with a date-only field", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueDate: "2026-11-12", DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata"}, false},
		{"unknown zone", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00", TimeZone: "Mars/Olympus"}, false},
		{"server-local zone", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00", TimeZone: "Local"}, false},
		{"ambiguous abbreviation", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00", TimeZone: "IST"}, false},
		{"unknown precision", Input{ApplicationID: appID, Title: "Interview", DuePrecision: "sometime", DueDate: "2026-11-12"}, false},
		{"missing title", Input{ApplicationID: appID, DuePrecision: PrecisionDate, DueDate: "2026-11-12"}, false},
		{"missing application", Input{Title: "Interview"}, false},
		{"nul in title", Input{ApplicationID: appID, Title: "Inter\u0000view"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			if got := input.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v (%+v)", got, tc.valid, input)
			}
		})
	}
}

func TestInstantResolutionUsesOriginalZone(t *testing.T) {
	input := Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata"}
	precision, deadline, ok := input.resolve()
	if !ok || precision != PrecisionInstant {
		t.Fatalf("resolve: %v %+v %v", precision, deadline, ok)
	}
	instant, ok := deadline.instant.(time.Time)
	if !ok || !instant.Equal(time.Date(2026, 11, 12, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("18:00 Asia/Kolkata resolved to %v, want 12:30Z", deadline.instant)
	}
	utc, local := renderInstant(instant, input.TimeZone)
	if utc != "2026-11-12T12:30:00Z" || local != "2026-11-12T18:00" {
		t.Fatalf("rendered %q / %q, want the original instant and wall time", utc, local)
	}
}

func TestInstantResolutionHandlesDaylightSavingBoundaries(t *testing.T) {
	// A local time inside the spring-forward gap does not exist. Reject it
	// rather than silently storing and showing a different clock time.
	spring := Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-03-08T02:30", TimeZone: "America/New_York"}
	if spring.Valid() {
		t.Fatal("a nonexistent spring-forward wall time must be rejected")
	}

	// An ambiguous local time (fall back) resolves to one real instant in the
	// zone, and rendering keeps that exact wall time with its own offset.
	fall := Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-01T01:30", TimeZone: "America/New_York"}
	_, fallDeadline, ok := fall.resolve()
	if !ok {
		t.Fatal("fall-back wall time should resolve, not error")
	}
	fallInstant := fallDeadline.instant.(time.Time)
	if fallInstant.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) == false &&
		fallInstant.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) == false {
		t.Fatalf("fall-back instant = %v, want one of the two real 01:30 instants", fallInstant)
	}
	utc, local := renderInstant(fallInstant, fall.TimeZone)
	if utc != fallInstant.Format(time.RFC3339) || local != "2026-11-01T01:30" {
		t.Fatalf("fall-back render = %q / %q", utc, local)
	}
}

func TestDateOnlyHasNoInstantOrZone(t *testing.T) {
	input := Input{ApplicationID: appID, Title: "Apply", DuePrecision: PrecisionDate, DueDate: "2026-11-12"}
	_, deadline, ok := input.resolve()
	if !ok || deadline.instant != nil || deadline.zone != nil || deadline.date != "2026-11-12" {
		t.Fatalf("date-only deadline = %+v, ok = %v", deadline, ok)
	}
}

func TestPatchInputValid(t *testing.T) {
	empty := PatchInput{}
	if empty.Valid() {
		t.Fatal("an empty patch must be rejected")
	}
	for name, patch := range map[string]PatchInput{
		"complete":      {Status: ptr(StatusDone)},
		"cancel":        {Status: ptr(StatusCancelled)},
		"title":         {Title: ptr("  New title  ")},
		"clear date":    {DuePrecision: ptr(PrecisionNone)},
		"invalid state": {Status: ptr("paused")},
		"blank title":   {Title: ptr("   ")},
		"bad precision": {DuePrecision: ptr("tomorrow")},
		"nul field":     {DueDate: ptr("2026-11\u0000-12")},
	} {
		t.Run(name, func(t *testing.T) {
			want := name == "complete" || name == "cancel" || name == "title" || name == "clear date"
			if got := patch.Valid(); got != want {
				t.Fatalf("Valid() = %v, want %v", got, want)
			}
		})
	}
	if title := (PatchInput{Title: ptr("  New title  ")}); title.Valid() && *title.Title != "New title" {
		t.Fatalf("patch values are not trimmed: %q", *title.Title)
	}
}

func ptr(value string) *string { return &value }
