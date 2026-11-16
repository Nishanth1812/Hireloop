package applications

import "testing"

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
