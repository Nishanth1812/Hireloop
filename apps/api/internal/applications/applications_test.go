package applications

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPatchRejectsOptionalFieldsWithNUL(t *testing.T) {
	for _, value := range []string{"Referral\x00", "me\x00@example.com"} {
		for _, field := range []string{"source", "address"} {
			input := PatchInput{}
			if field == "source" {
				input.Source = &value
			} else {
				input.Address = &value
			}
			if input.Valid() {
				t.Errorf("accepted NUL in %s", field)
			}
		}
	}
	blank := "   "
	if input := (PatchInput{Source: &blank}); !input.Valid() || *input.Source != "" {
		t.Fatal("blank source must be accepted and trimmed to empty")
	}
	overlong := strings.Repeat("x", 201)
	addressInput := PatchInput{Address: &overlong}
	if addressInput.Valid() {
		t.Fatal("overlong address must be rejected")
	}
}

func TestPatchRejectsNULWithoutRejectingUnicode(t *testing.T) {
	for _, value := range []string{"Acme\x00", "\x00Engineer", "Eng\x00ineer"} {
		for _, field := range []string{"company", "role"} {
			input := PatchInput{}
			if field == "company" {
				input.Company = &value
			} else {
				input.Role = &value
			}
			if input.Valid() {
				t.Errorf("accepted NUL in %s", field)
			}
		}
	}
	company, role := "  株式会社  ", "  Développeur  "
	input := PatchInput{Company: &company, Role: &role}
	if !input.Valid() || company != "株式会社" || role != "Développeur" {
		t.Fatal("valid international names must be accepted and trimmed")
	}
}

func TestManualFieldValidation(t *testing.T) {
	valid := Input{
		Company: "Acme", Role: "Engineer", Status: "applied",
		URL: "https://jobs.example.com/42", Channel: "campus",
		JobID: "REQ-42", AppliedOn: "2026-11-02", Notes: "Placement cell notice.",
	}
	if !valid.Valid() || valid.Company != "Acme" || valid.Channel != "campus" {
		t.Fatalf("valid manual fields rejected: %+v", valid)
	}
	for name, mutate := range map[string]func(*Input){
		"non-http url":     func(i *Input) { i.URL = "javascript:alert(1)" },
		"relative url":     func(i *Input) { i.URL = "/jobs/42" },
		"hostless url":     func(i *Input) { i.URL = "https://" },
		"overlong url":     func(i *Input) { i.URL = "https://example.com/" + strings.Repeat("x", 2048) },
		"unknown channel":  func(i *Input) { i.Channel = "remote" },
		"impossible date":  func(i *Input) { i.AppliedOn = "2026-02-30" },
		"date with a time": func(i *Input) { i.AppliedOn = "2026-11-02T09:00:00Z" },
		"unpadded date":    func(i *Input) { i.AppliedOn = "2026-11-2" },
		"overlong job id":  func(i *Input) { i.JobID = strings.Repeat("x", 201) },
		"overlong notes":   func(i *Input) { i.Notes = strings.Repeat("x", 2001) },
		"NUL in notes":     func(i *Input) { i.Notes = "note\x00" },
		"NUL in job id":    func(i *Input) { i.JobID = "REQ\x00" },
		"NUL in url":       func(i *Input) { i.URL = "https://example.com/\x00" },
	} {
		input := valid
		mutate(&input)
		if input.Valid() {
			t.Errorf("accepted invalid %s: %+v", name, input)
		}
	}
}

func TestPatchTrimsManualFieldsAndClearsOptionalOnes(t *testing.T) {
	url, channel, jobID, appliedOn, notes := "  https://example.com/42  ", " campus ", " REQ-42 ", " 2026-11-02 ", "  notice  "
	input := PatchInput{URL: &url, Channel: &channel, JobID: &jobID, AppliedOn: &appliedOn, Notes: &notes}
	if !input.Valid() {
		t.Fatalf("valid patch rejected: %+v", input)
	}
	if *input.URL != "https://example.com/42" || *input.Channel != "campus" || *input.JobID != "REQ-42" || *input.AppliedOn != "2026-11-02" || *input.Notes != "notice" {
		t.Fatalf("patch fields not trimmed: %+v", input)
	}
	blank := "   "
	for name, patch := range map[string]PatchInput{
		"url": {URL: &blank}, "channel": {Channel: &blank}, "job id": {JobID: &blank},
		"date": {AppliedOn: &blank}, "notes": {Notes: &blank},
	} {
		if !patch.Valid() {
			t.Errorf("blank %s must be a valid clear", name)
		}
	}
}

func TestParseQueryBoundsAndDefaults(t *testing.T) {
	query, err := ParseQuery(url.Values{})
	if err != nil || query.PageSize != DefaultPageSize || query.After != nil {
		t.Fatalf("defaults = %+v, %v", query, err)
	}
	query, err = ParseQuery(url.Values{"q": {"  acme "}, "page_size": {"10"}})
	if err != nil || query.Search != "acme" || query.PageSize != 10 {
		t.Fatalf("parsed query = %+v, %v", query, err)
	}
	for name, values := range map[string]url.Values{
		"zero page size":    {"page_size": {"0"}},
		"oversized page":    {"page_size": {strconv.Itoa(MaxPageSize + 1)}},
		"fractional page":   {"page_size": {"1.5"}},
		"unknown status":    {"status": {"archived"}},
		"unknown channel":   {"channel": {"remote"}},
		"removed page":      {"page": {"2"}},
		"unknown parameter": {"offset": {"2"}},
		"malformed cursor":  {"cursor": {"abc"}},
		"empty cursor":      {"cursor": {""}},
		"overlong cursor":   {"cursor": {strings.Repeat("a", maxCursorLength+1)}},
		"overlong search":   {"q": {strings.Repeat("x", maxSearchLength+1)}},
	} {
		if _, err := ParseQuery(values); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestParseQueryCursorRoundTripAndBinding covers the cursor contract: a token
// round-trips its position, and it is bound to the normalized filters that
// produced it so it cannot be silently applied to a different query.
func TestParseQueryCursorRoundTripAndBinding(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 10, 30, 0, 123456000, time.UTC)
	id := "00000000-0000-0000-0000-000000000001"
	base := Query{Search: "acme", Status: "applied", Channel: "campus", PageSize: 10}
	cursor := base.encodeCursor(createdAt, id)

	query, err := ParseQuery(url.Values{
		"q": {"  acme  "}, "status": {"applied"}, "channel": {"campus"},
		"page_size": {"10"}, "cursor": {cursor},
	})
	if err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	if query.After == nil || !query.After.CreatedAt.Equal(createdAt) || query.After.ID != id {
		t.Fatalf("decoded cursor = %+v", query.After)
	}

	// A cursor minted for one filter set must not be accepted for another.
	for name, values := range map[string]url.Values{
		"different search":  {"q": {"globex"}, "cursor": {cursor}},
		"different status":  {"status": {"saved"}, "cursor": {cursor}},
		"different channel": {"channel": {"off-campus"}, "cursor": {cursor}},
		"no filters":        {"cursor": {cursor}},
	} {
		if _, err := ParseQuery(values); err == nil {
			t.Errorf("%s: expected the cursor to be rejected", name)
		}
	}

	// The binding ignores insignificant differences: a trimmed search and an
	// omitted empty filter still name the same query.
	if _, err := ParseQuery(url.Values{"q": {" acme "}, "status": {"applied"}, "channel": {"campus"}, "cursor": {cursor}}); err != nil {
		t.Fatalf("equivalent normalized filters rejected: %v", err)
	}
}

// TestParseQueryRejectsMalformedCursors pins the boundary behavior: a cursor is
// bounded, versioned, and strictly decoded, so a hand-crafted or corrupted
// token fails as a bad query instead of reaching the database.
func TestParseQueryRejectsMalformedCursors(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)
	id := "00000000-0000-0000-0000-000000000001"
	validToken := func(mutate func(*cursorToken)) string {
		token := cursorToken{
			Version:   cursorVersion,
			CreatedAt: now.Format(time.RFC3339Nano),
			ID:        id,
			Filters:   Query{}.cursorFilterHash(),
		}
		if mutate != nil {
			mutate(&token)
		}
		payload, err := json.Marshal(token)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(payload)
	}
	for name, cursor := range map[string]string{
		"not base64":       "!!!!",
		"not json":         base64.RawURLEncoding.EncodeToString([]byte("not json")),
		"unknown field":    base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"c":"2026-11-01T10:30:00Z","i":"` + id + `","f":"x","extra":true}`)),
		"future version":   validToken(func(token *cursorToken) { token.Version = cursorVersion + 1 }),
		"wrong filters":    validToken(func(token *cursorToken) { token.Filters = "something-else" }),
		"malformed id":     validToken(func(token *cursorToken) { token.ID = "not-a-uuid" }),
		"malformed time":   validToken(func(token *cursorToken) { token.CreatedAt = "yesterday" }),
		"trailing garbage": validToken(nil) + "AAAA",
	} {
		if _, err := ParseQuery(url.Values{"cursor": {cursor}}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLikePatternEscapesWildcards(t *testing.T) {
	if got := likePattern("50%"); got != `%50\%%` {
		t.Fatalf("likePattern(50%%) = %q", got)
	}
	if got := likePattern("a_b"); got != `%a\_b%` {
		t.Fatalf("likePattern(a_b) = %q", got)
	}
	if got := likePattern(`back\slash`); got != `%back\\slash%` {
		t.Fatalf("likePattern(back\\slash) = %q", got)
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"00000000-0000-0000-0000-000000000001", "ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB"} {
		if !ValidID(id) {
			t.Errorf("rejected UUID %q", id)
		}
	}
	for _, id := range []string{"", "../applications", "00000000000000000000000000000001", "gggggggg-gggg-gggg-gggg-gggggggggggg", "--000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000001/extra"} {
		if ValidID(id) {
			t.Errorf("accepted invalid UUID %q", id)
		}
	}
}
