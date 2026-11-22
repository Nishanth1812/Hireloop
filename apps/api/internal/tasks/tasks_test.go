package tasks

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

const appID = "11111111-1111-4111-8111-111111111111"

type pageScanner struct{ created time.Time }

func (s pageScanner) Scan(dest ...any) error {
	if len(dest) != 11 {
		return fmt.Errorf("expected 11 scan destinations, got %d", len(dest))
	}
	*dest[9].(*time.Time) = s.created
	*dest[10].(*string) = "infinity"
	return nil
}

func TestScanTaskPageUsesCreatedAtAsCursorKey(t *testing.T) {
	created := time.Date(2026, 11, 1, 1, 2, 3, 456, time.UTC)
	page, err := scanTaskPage(pageScanner{created: created})
	if err != nil || !page.CreatedAt.Equal(created) || page.Day != "infinity" {
		t.Fatalf("page = %+v, error = %v", page, err)
	}
}

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
		{"date-only with browser reminder zone", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12", ReminderZone: "Asia/Kolkata"}, true},
		{"date-only with UTC reminder zone", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12", ReminderZone: "UTC"}, true},
		{"date-only with unknown reminder zone", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12", ReminderZone: "Mars/Olympus"}, false},
		{"date-only with server-local reminder zone", Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12", ReminderZone: "Local"}, false},
		{"no deadline with reminder zone", Input{ApplicationID: appID, Title: "Reply", ReminderZone: "UTC"}, false},
		{"instant with reminder zone", Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant, DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata", ReminderZone: "UTC"}, false},
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

func TestDateResolutionKeepsOptionalReminderZoneSeparate(t *testing.T) {
	input := Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate,
		DueDate: "2026-11-12", ReminderZone: "  Asia/Kolkata  "}
	precision, deadline, ok := input.resolve()
	if !ok || precision != PrecisionDate || input.ReminderZone != "Asia/Kolkata" {
		t.Fatalf("resolve: %v %+v %v", precision, deadline, ok)
	}
	if deadline.reminderZone != "Asia/Kolkata" || deadline.zone != nil || deadline.date != "2026-11-12" {
		t.Fatalf("deadline = %+v, want zone only in reminderZone", deadline)
	}
	// A missing browser zone is not an error: the task stays a plain date.
	input = Input{ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate, DueDate: "2026-11-12"}
	if _, deadline, ok := input.resolve(); !ok || deadline.reminderZone != nil {
		t.Fatalf("date without zone: %+v %v", deadline, ok)
	}
}

func TestScheduleIdentityTracksReminderInputs(t *testing.T) {
	instant := time.Date(2026, 11, 12, 12, 30, 0, 0, time.UTC)
	base := resolvedDeadline{precision: PrecisionInstant, instant: instant, zone: "Asia/Kolkata"}
	stored := Task{DuePrecision: PrecisionInstant, DueAt: "2026-11-12T12:30:00Z", TimeZone: "Asia/Kolkata"}
	if !sameSchedule(currentSchedule(stored), resolvedSchedule(base)) {
		t.Fatal("an unchanged instant deadline must keep in-flight jobs")
	}
	changed := base
	changed.instant = instant.Add(time.Minute)
	if sameSchedule(currentSchedule(stored), resolvedSchedule(changed)) {
		t.Fatal("a changed instant must reschedule jobs")
	}

	dateTask := Task{DuePrecision: PrecisionDate, DueDate: "2026-11-12", ReminderZone: "Asia/Kolkata"}
	dateDeadline := resolvedDeadline{precision: PrecisionDate, date: "2026-11-12", reminderZone: "Asia/Kolkata"}
	if !sameSchedule(currentSchedule(dateTask), resolvedSchedule(dateDeadline)) {
		t.Fatal("an unchanged date deadline and zone must keep in-flight jobs")
	}
	dateDeadline.reminderZone = "UTC"
	if sameSchedule(currentSchedule(dateTask), resolvedSchedule(dateDeadline)) {
		t.Fatal("a changed reminder zone must reschedule approximate jobs")
	}
	dateDeadline.reminderZone = ""
	if sameSchedule(currentSchedule(dateTask), resolvedSchedule(dateDeadline)) {
		t.Fatal("removing the reminder zone must retire approximate jobs")
	}
}

func TestInstantResolutionRejectsGapsAndAmbiguousWallTimes(t *testing.T) {
	// Gaps never happened, and overlaps happened twice. Neither may be silently
	// resolved to an instant the user never chose. Non-hour shifts matter too:
	// Lord Howe's fall-back is 30 minutes, not an hour.
	for _, tc := range []struct{ name, local, zone string }{
		{"one-hour gap", "2026-03-08T02:30", "America/New_York"},
		{"one-hour overlap", "2026-11-01T01:30", "America/New_York"},
		{"half-hour gap", "2026-10-04T02:15", "Australia/Lord_Howe"},
		{"half-hour overlap", "2026-04-05T01:45", "Australia/Lord_Howe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
				DueLocal: tc.local, TimeZone: tc.zone}
			if in.Valid() {
				t.Fatalf("%s %s must be rejected", tc.local, tc.zone)
			}
			if _, _, ok := in.resolve(); ok {
				t.Fatalf("%s %s resolved despite being invalid", tc.local, tc.zone)
			}
		})
	}
}

func TestInstantResolutionKeepsValidUnambiguousTimesIncludingNonHourZones(t *testing.T) {
	for _, tc := range []struct{ local, zone, utc string }{
		{"2026-11-01T00:30", "America/New_York", "2026-11-01T04:30:00Z"},
		{"2026-11-01T02:30", "America/New_York", "2026-11-01T07:30:00Z"},
		{"2026-04-05T01:15", "Australia/Lord_Howe", "2026-04-04T14:15:00Z"},
		{"2026-11-12T18:00", "Asia/Kolkata", "2026-11-12T12:30:00Z"},
		{"2026-11-12T18:00", "UTC", "2026-11-12T18:00:00Z"},
	} {
		t.Run(tc.local+" "+tc.zone, func(t *testing.T) {
			in := Input{ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
				DueLocal: tc.local, TimeZone: tc.zone}
			_, deadline, ok := in.resolve()
			if !ok {
				t.Fatal("valid wall time was rejected")
			}
			instant := deadline.instant.(time.Time)
			if !instant.Equal(mustParseUTC(t, tc.utc)) {
				t.Fatalf("resolved %v, want %s", instant, tc.utc)
			}
			utc, local := renderInstant(instant, tc.zone)
			if utc != tc.utc || local != tc.local {
				t.Fatalf("rendered %q / %q, want %q / %q", utc, local, tc.utc, tc.local)
			}
		})
	}
}

func mustParseUTC(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
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

// A cursor is a position in the four-column sort key. These checks pin the
// bounds and the filter binding without a database.
func TestParseQueryIsBoundedAndRejectsUnknownInput(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown parameter":  "?status=open",
		"empty filter":       "?filter=",
		"bad filter":         "?filter=today",
		"empty cursor":       "?cursor=",
		"bad cursor":         "?cursor=not-a-token",
		"zero page size":     "?page_size=0",
		"negative page size": "?page_size=-1",
		"oversized page":     "?page_size=51",
		"numeric garbage":    "?page_size=many",
	} {
		t.Run(name, func(t *testing.T) {
			values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseQuery(values); err == nil {
				t.Fatalf("%s was accepted", raw)
			}
		})
	}
	for raw, want := range map[string]struct {
		size    int
		showAll bool
	}{
		"":                         {DefaultPageSize, false},
		"?filter=open":             {DefaultPageSize, false},
		"?filter=all":              {DefaultPageSize, true},
		"?page_size=1":             {1, false},
		"?page_size=50&filter=all": {MaxPageSize, true},
	} {
		values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err != nil {
			t.Fatal(err)
		}
		filter, err := ParseQuery(values)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if filter.PageSize != want.size || filter.ShowAll != want.showAll || filter.After != nil {
			t.Fatalf("%s = %+v", raw, filter)
		}
	}
}

func TestParseQueryBindsApplicationIDToFilter(t *testing.T) {
	applicationID := "123e4567-e89b-42d3-a456-426614174000"
	values := url.Values{"application_id": {applicationID}}
	filter, err := ParseQuery(values)
	if err != nil || filter.ApplicationID != applicationID {
		t.Fatalf("application filter = %+v, %v", filter, err)
	}
	filter.After = &Cursor{Day: "2026-11-01", At: "-infinity", ID: "123e4567-e89b-42d3-a456-426614174001"}
	token := filter.encodeCursor(filter.After.Day, filter.After.At, time.Now(), filter.After.ID)
	if _, err := ParseQuery(url.Values{"application_id": {applicationID}, "cursor": {token}}); err != nil {
		t.Fatalf("matching application cursor rejected: %v", err)
	}
	if _, err := ParseQuery(url.Values{"application_id": {"bad"}}); err == nil {
		t.Fatal("invalid application ID accepted")
	}
	if _, err := ParseQuery(url.Values{"application_id": {""}}); err == nil {
		t.Fatal("empty application ID accepted")
	}
}

func TestApplicationCursorIsBoundToItsFilter(t *testing.T) {
	appID := "123e4567-e89b-42d3-a456-426614174000"
	otherID := "123e4567-e89b-42d3-a456-426614174001"
	created := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	filter := Filter{ApplicationID: appID, PageSize: 2}
	token := filter.encodeCursor("infinity", "-infinity", created, appID)
	if _, err := ParseQuery(url.Values{"application_id": {appID}, "cursor": {token}}); err != nil {
		t.Fatalf("matching application cursor rejected: %v", err)
	}
	for name, values := range map[string]url.Values{
		"other application": {"application_id": {otherID}, "cursor": {token}},
		"no application":    {"cursor": {token}},
		"other view":        {"application_id": {appID}, "filter": {"all"}, "cursor": {token}},
	} {
		if _, err := ParseQuery(values); err == nil {
			t.Errorf("%s: expected the cursor to be rejected", name)
		}
	}
}

func TestCursorCarriesEveryTieBreakAndIsFilterBound(t *testing.T) {
	filter := Filter{PageSize: 2}
	created := time.Date(2026, 11, 1, 9, 0, 0, 123456789, time.UTC)
	id := "33333333-3333-4333-8333-333333333333"
	token := EncodeCursor(filter, "2026-12-01", "2026-12-01T09:00:00.123456789Z", created, id)

	values, err := url.ParseQuery("page_size=2&cursor=" + url.QueryEscape(token))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseQuery(values)
	if err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	if parsed.After == nil || parsed.After.Day != "2026-12-01" || parsed.After.At != "2026-12-01T09:00:00.123456789Z" ||
		!parsed.After.CreatedAt.Equal(created) || parsed.After.ID != id || parsed.PageSize != 2 {
		t.Fatalf("decoded cursor = %+v / %+v", parsed, *parsed.After)
	}
	// The same token is a different view when showAll changes.
	allValues, err := url.ParseQuery("filter=all&cursor=" + url.QueryEscape(token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseQuery(allValues); err == nil {
		t.Fatal("cursor minted for open tasks was accepted for filter=all")
	}

	// The no-deadline and no-instant sentinels survive an exact round trip.
	sentinel := EncodeCursor(Filter{ShowAll: true}, noDeadlineDay, noInstantAt, created, id)
	sentinelValues, err := url.ParseQuery("filter=all&cursor=" + url.QueryEscape(sentinel))
	if err != nil {
		t.Fatal(err)
	}
	fromSentinel, err := ParseQuery(sentinelValues)
	if err != nil || fromSentinel.After == nil || fromSentinel.After.Day != noDeadlineDay || fromSentinel.After.At != noInstantAt {
		t.Fatalf("sentinel cursor = %+v, err = %v", fromSentinel, err)
	}
}

func ptr(value string) *string { return &value }
