package templates

import (
	"io/fs"
	"strings"
	"testing"
)

// `applier = "forge"` is a TRANSITIONAL state: every real env, the platform's own
// included, moves onto the Flux path one delivery unit at a time, and direct
// apply narrows to dev and ephemeral envs. The doctrine used to say the platform's
// own env was forge-applied PERMANENTLY, and an agent that read it would have
// built the opposite of what the owner decided. This pins the wording so it
// cannot creep back, in every skill a project is handed and in the KCL schema
// that is the field's reference.
func TestApplierDoctrine_IsTransitionalNotPermanent(t *testing.T) {
	banned := []string{
		"applier permanently",
		"permanently, not as a stopgap",
		"by declaration and permanently",
		"on purpose and permanently",
		"it is not a stopgap",
		"not as a stopgap on the way",
	}
	check := func(name, text string) {
		lower := strings.ToLower(text)
		for _, phrase := range banned {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s still teaches that forge-applied is permanent (%q)", name, phrase)
			}
		}
	}
	walked := 0
	err := fs.WalkDir(templateFS, "project/skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		b, rerr := fs.ReadFile(templateFS, path)
		if rerr != nil {
			return rerr
		}
		walked++
		check(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk skills: %v", err)
	}
	if walked == 0 {
		t.Fatal("walked no skills: the guard checked nothing")
	}
}

// The skills must say what replaces the permanent reading: units move, one at a
// time, by `hub_converges`.
func TestApplierDoctrine_NamesTheUnitByUnitMove(t *testing.T) {
	for _, path := range []string{
		"project/skills/forge/deploy/shape/SKILL.md",
		"project/skills/forge/deploy/flux/SKILL.md",
		"project/skills/forge/deploy/SKILL.md",
	} {
		b, err := fs.ReadFile(templateFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(b)
		if !strings.Contains(text, "hub_converges") {
			t.Errorf("%s does not name hub_converges as the way an env moves onto the hub", path)
		}
	}
}
