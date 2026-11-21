package tasks

import (
	"strings"
	"time"
	"unicode/utf8"
)

// ExportIcs renders one task as a single-task VCALENDAR holding one VTODO.
// The export uses only the deadline shape already stored on the task:
//
//   - PrecisionDate:    DUE;VALUE=DATE with the stored calendar day — no time
//     and no zone are invented for a deadline the user gave as a day.
//   - PrecisionInstant: DUE;TZID=<original zone> with the stored wall time in
//     that zone, so the exported instant is the one the user chose.
//   - PrecisionNone:    no DUE at all.
//
// Tasks whose deadline fields are empty, inconsistent or invalid for their
// precision are rejected instead of being exported as invented facts, so an
// export never claims a time or zone that was never stored.
func ExportIcs(task Task) (string, error) {
	var dueLine string
	switch task.DuePrecision {
	case PrecisionDate:
		if !validDate(task.DueDate) || task.DueAt != "" || task.TimeZone != "" {
			return "", ErrInvalid
		}
		dueLine = "DUE;VALUE=DATE:" + strings.ReplaceAll(task.DueDate, "-", "")
	case PrecisionInstant:
		if task.DueDate != "" || !validZone(task.TimeZone) {
			return "", ErrInvalid
		}
		instant, err := time.Parse(time.RFC3339, task.DueAt)
		if err != nil {
			return "", ErrInvalid
		}
		_, wall := renderInstant(instant, task.TimeZone)
		if wall == "" {
			return "", ErrInvalid
		}
		// The app stores minutes; DATE-TIME values carry no seconds in this
		// export, so none are invented for a deadline recorded as minutes.
		dueLine = "DUE;TZID=" + task.TimeZone + ":" + compactIcsDateTime(wall)
	case PrecisionNone:
		if task.DueDate != "" || task.DueAt != "" || task.TimeZone != "" {
			return "", ErrInvalid
		}
	default:
		return "", ErrInvalid
	}
	if !validStatus(task.Status) || !ValidID(task.ID) {
		return "", ErrInvalid
	}
	dtstamp := task.CreatedAt
	if parsed, err := time.Parse(time.RFC3339, task.CreatedAt); err == nil {
		// ICS DATE-TIME in UTC uses no separators; RFC 3339 uses them.
		dtstamp = parsed.UTC().Format("20060102T150405Z")
	} else {
		return "", ErrInvalid
	}

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//Hireloop//Tasks//EN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	b.WriteString("BEGIN:VTODO\r\n")
	writeIcsLine(&b, "UID:"+task.ID)
	writeIcsLine(&b, "DTSTAMP:"+dtstamp)
	writeIcsLine(&b, "SUMMARY:"+escapeIcsText(task.Title))
	writeIcsLine(&b, "STATUS:"+icsStatus(task.Status))
	if dueLine != "" {
		writeIcsLine(&b, dueLine)
	}
	b.WriteString("END:VTODO\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
}

// compactIcsDateTime turns a wall time such as "2026-11-12T18:00" into the
// RFC 5545 DATE-TIME form "20261112T1800" (minutes precision, no seconds).
func compactIcsDateTime(wall string) string {
	return strings.ReplaceAll(strings.ReplaceAll(wall, "-", ""), ":", "")
}

func icsStatus(status string) string {
	switch status {
	case StatusDone:
		return "COMPLETED"
	case StatusCancelled:
		return "CANCELLED"
	default:
		return "NEEDS-ACTION"
	}
}

// escapeIcsText escapes a TEXT value per RFC 5545: backslash, semicolon and
// comma are quoted with a backslash, and line breaks become the two-character
// sequence "\n" so a value can never disturb the calendar's line structure.
func escapeIcsText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, ";", "\\;")
	value = strings.ReplaceAll(value, ",", "\\,")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return value
}

// writeIcsLine writes one logical ICS line with CRLF endings. Physical lines
// longer than 75 octets are folded with a CRLF followed by one space, the
// RFC 5545 limit, cutting at a rune boundary so folding never splits a UTF-8
// character. The first physical line holds up to 75 octets; continuation
// lines hold up to 74 octets of content after their leading space.
func writeIcsLine(b *strings.Builder, line string) {
	first := true
	for {
		limit := 75
		if !first {
			limit = 74
		}
		if len(line) <= limit {
			b.WriteString(line)
			b.WriteString("\r\n")
			return
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		if cut == 0 {
			// A pathological non-UTF-8 string; write one octet so progress is
			// guaranteed.
			cut = 1
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		first = false
	}
}
