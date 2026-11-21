package tasks

import (
	"errors"
	"strings"
	"testing"
)

const icsTestID = "22222222-2222-4222-8222-000000000001"

func icsFixtureTask() Task {
	return Task{
		ID:            icsTestID,
		ApplicationID: "11111111-1111-4111-8111-111111111111",
		Title:         "Submit assessment",
		Status:        StatusOpen,
		DuePrecision:  PrecisionDate,
		DueDate:       "2026-11-12",
		CreatedAt:     "2026-11-01T09:00:00Z",
	}
}

// icsLine returns the named line of an export, or "" when absent.
func icsLine(t *testing.T, export, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(export, "\r\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func TestExportIcsDateOnlyHonorsPrecision(t *testing.T) {
	export, err := ExportIcs(icsFixtureTask())
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	if got := icsLine(t, export, "DUE"); got != "DUE;VALUE=DATE:20261112" {
		t.Fatalf("DUE line = %q, want DUE;VALUE=DATE:20261112", got)
	}
	if got := icsLine(t, export, "STATUS"); got != "STATUS:NEEDS-ACTION" {
		t.Fatalf("STATUS line = %q, want STATUS:NEEDS-ACTION", got)
	}
	if strings.Contains(export, "TZID") {
		t.Fatalf("date-only export carries a zone: %q", export)
	}
}

func TestExportIcsDateOnlyIgnoresApproximateReminderZone(t *testing.T) {
	task := icsFixtureTask()
	// A browser zone used only for approximate nudges is not part of the
	// deadline shape: the export must stay a plain DATE with no alarm.
	task.ReminderZone = "Asia/Kolkata"
	export, err := ExportIcs(task)
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	if got := icsLine(t, export, "DUE"); got != "DUE;VALUE=DATE:20261112" {
		t.Fatalf("DUE line = %q, want DUE;VALUE=DATE:20261112", got)
	}
	for _, forbidden := range []string{"TZID", "VALARM", "TRIGGER", "Asia/Kolkata"} {
		if strings.Contains(export, forbidden) {
			t.Errorf("date-only export must not carry %q: %q", forbidden, export)
		}
	}
}

func TestExportIcsInstantPreservesUTCInstant(t *testing.T) {
	task := icsFixtureTask()
	task.DuePrecision = PrecisionInstant
	task.DueDate = ""
	task.DueLocal = "2026-11-12T18:00"
	task.DueAt = "2026-11-12T12:30:00Z"
	task.TimeZone = "Asia/Kolkata"
	export, err := ExportIcs(task)
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	if got := icsLine(t, export, "DUE"); got != "DUE:20261112T123000Z" {
		t.Fatalf("DUE line = %q, want DUE:20261112T123000Z", got)
	}
	if strings.Contains(export, "STATUS:COMPLETED") {
		t.Fatalf("open task exported as completed: %q", export)
	}
}

func TestExportIcsPreservesSecondOccurrenceDuringOverlap(t *testing.T) {
	task := icsFixtureTask()
	task.DuePrecision, task.DueDate = PrecisionInstant, ""
	task.DueAt, task.TimeZone = "2026-11-01T06:30:00Z", "America/New_York"
	export, err := ExportIcs(task)
	if err != nil || icsLine(t, export, "DUE") != "DUE:20261101T063000Z" {
		t.Fatalf("overlap export = %q, err = %v", export, err)
	}
	task.DueAt = "2026-11-01T06:30:00.123456Z"
	if _, err := ExportIcs(task); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fractional instant error = %v, want ErrInvalid", err)
	}
}

func TestExportIcsInstantInUTCZone(t *testing.T) {
	task := icsFixtureTask()
	task.DuePrecision = PrecisionInstant
	task.DueDate = ""
	task.DueLocal = "2026-11-12T18:00"
	task.DueAt = "2026-11-12T18:00:00Z"
	task.TimeZone = "UTC"
	export, err := ExportIcs(task)
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	if got := icsLine(t, export, "DUE"); got != "DUE:20261112T180000Z" {
		t.Fatalf("DUE line = %q, want DUE:20261112T180000Z", got)
	}
}

func TestExportIcsStatusMapsLifecycle(t *testing.T) {
	for status, want := range map[string]string{
		StatusDone:      "STATUS:COMPLETED",
		StatusCancelled: "STATUS:CANCELLED",
		StatusOpen:      "STATUS:NEEDS-ACTION",
	} {
		task := icsFixtureTask()
		task.Status = status
		export, err := ExportIcs(task)
		if err != nil {
			t.Fatalf("ExportIcs(%s): %v", status, err)
		}
		if got := icsLine(t, export, "STATUS"); got != want {
			t.Fatalf("STATUS line for %s = %q, want %q", status, got, want)
		}
	}
}

func TestExportIcsEnvelopeAndDTStamp(t *testing.T) {
	export, err := ExportIcs(icsFixtureTask())
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	lines := strings.Split(export, "\r\n")
	if lines[0] != "BEGIN:VCALENDAR" {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.HasSuffix(export, "END:VCALENDAR\r\n") {
		t.Fatalf("export does not end with END:VCALENDAR: %q", export[len(export)-32:])
	}
	if got := icsLine(t, export, "END:VTODO"); got != "END:VTODO" {
		t.Fatalf("END:VTODO line = %q", got)
	}
	if got := icsLine(t, export, "VERSION"); got != "VERSION:2.0" {
		t.Fatalf("VERSION line = %q", got)
	}
	if icsLine(t, export, "PRODID") == "" || icsLine(t, export, "CALSCALE") == "" {
		t.Fatal("missing PRODID or CALSCALE")
	}
	if got := icsLine(t, export, "BEGIN:VTODO"); got != "BEGIN:VTODO" {
		t.Fatalf("BEGIN:VTODO line = %q", got)
	}
	if got := icsLine(t, export, "UID"); got != "UID:"+icsTestID {
		t.Fatalf("UID line = %q, want UID:%s", got, icsTestID)
	}
	if got := icsLine(t, export, "DTSTAMP"); got != "DTSTAMP:20261101T090000Z" {
		t.Fatalf("DTSTAMP line = %q", got)
	}
	if icsLine(t, export, "END:VTODO") != "END:VTODO" {
		t.Fatal("missing END:VTODO")
	}
}

func TestExportIcsFoldsLongLines(t *testing.T) {
	task := icsFixtureTask()
	task.Title = strings.Repeat("Apply", 40) // 200 characters, over the 75-octet limit.
	export, err := ExportIcs(task)
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	for i, line := range strings.Split(export, "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line %d is %d octets, want <= 75: %q", i, len(line), line)
		}
	}
	// Folding is transparent: the reassembled line is the full property.
	joined := strings.ReplaceAll(export, "\r\n ", "")
	if !strings.Contains(joined, "SUMMARY:"+task.Title) {
		t.Fatalf("folded export lost the summary: %q", joined)
	}
}

func TestExportIcsEscapesTextValues(t *testing.T) {
	task := icsFixtureTask()
	task.Title = "Semicolon; comma, backslash\\ and newline\nend"
	export, err := ExportIcs(task)
	if err != nil {
		t.Fatalf("ExportIcs: %v", err)
	}
	want := "SUMMARY:Semicolon\\; comma\\, backslash\\\\ and newline\\nend"
	if !strings.Contains(export, want) {
		t.Fatalf("escaped summary = %q, want it to contain %q", export, want)
	}
	for _, line := range strings.Split(export, "\r\n") {
		if strings.HasPrefix(line, "SUMMARY:") {
			if strings.ContainsAny(line, "\r\n") {
				t.Fatalf("summary line carries a raw line break: %q", line)
			}
		}
	}
}

func TestExportIcsRemovesInvalidControlCharacters(t *testing.T) {
	task := icsFixtureTask()
	task.Title = "Interview\x01\x7f now"
	export, err := ExportIcs(task)
	if err != nil || !strings.Contains(export, "SUMMARY:Interview now\r\n") {
		t.Fatalf("export = %q, err = %v", export, err)
	}
}

func TestExportIcsRejectsInvalidDeadlinesAndTasks(t *testing.T) {
	base := func() Task {
		task := icsFixtureTask()
		task.DueDate = ""
		task.DuePrecision = PrecisionNone
		return task
	}
	tests := []struct {
		name string
		task Task
	}{
		{"missing id", func() Task { t := base(); t.ID = ""; return t }()},
		{"empty precision", func() Task { t := base(); t.DuePrecision = ""; return t }()},
		{"date without a day", func() Task {
			t := base()
			t.DuePrecision = PrecisionDate
			return t
		}()},
		{"date with a time", func() Task {
			t := base()
			t.DuePrecision = PrecisionDate
			t.DueDate = "2026-11-12"
			t.DueAt = "2026-11-12T18:00:00Z"
			return t
		}()},
		{"date with a zone", func() Task {
			t := base()
			t.DuePrecision = PrecisionDate
			t.DueDate = "2026-11-12"
			t.TimeZone = "Asia/Kolkata"
			return t
		}()},
		{"date without the day of month", func() Task {
			t := base()
			t.DuePrecision = PrecisionDate
			t.DueDate = "2026-11"
			return t
		}()},
		{"none with a day", func() Task { t := base(); t.DuePrecision = PrecisionNone; t.DueDate = "2026-11-12"; return t }()},
		{"instant without wall time", func() Task {
			t := base()
			t.DuePrecision = PrecisionInstant
			t.DueLocal = "2026-11-12T18:00"
			t.TimeZone = "Asia/Kolkata"
			return t
		}()},
		{"instant without a zone", func() Task {
			t := base()
			t.DuePrecision = PrecisionInstant
			t.DueLocal = "2026-11-12T18:00"
			return t
		}()},
		{"instant with an unknown zone", func() Task {
			t := base()
			t.DuePrecision = PrecisionInstant
			t.DueLocal = "2026-11-12T18:00"
			t.TimeZone = "Not/AZone"
			return t
		}()},
		{"instant with a day as well", func() Task {
			t := base()
			t.DuePrecision = PrecisionInstant
			t.DueLocal = "2026-11-12T18:00"
			t.TimeZone = "Asia/Kolkata"
			t.DueDate = "2026-11-12"
			return t
		}()},
		{"invalid status", func() Task { t := base(); t.Status = "bogus"; return t }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ExportIcs(tc.task); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ExportIcs = %v, want ErrInvalid", err)
			}
		})
	}
}
