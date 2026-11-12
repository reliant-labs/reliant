package cli

import (
	"reflect"
	"testing"
)

// The shipped listing is read once per process (listForgeShippedSkills), so
// each call must hand out its own slice: a caller that filters or edits what
// it got must not change what the next caller sees.
func TestListForgeShippedSkillsHandsEachCallerItsOwnCopy(t *testing.T) {
	first, err := listForgeShippedSkills()
	if err != nil {
		t.Fatalf("listForgeShippedSkills: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("listForgeShippedSkills returned no skills")
	}
	want := append([]skillMeta(nil), first...)

	first[0].Description = "edited by a caller"
	_ = append(first[:1], skillMeta{Path: "appended-by-a-caller"})

	second, err := listForgeShippedSkills()
	if err != nil {
		t.Fatalf("listForgeShippedSkills: %v", err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("a caller's edits reached the next listing: got %d skills, first %+v; want %d, first %+v",
			len(second), second[0], len(want), want[0])
	}
}
