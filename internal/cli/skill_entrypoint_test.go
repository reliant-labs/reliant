package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestSkillListNamesTheEntryPoint pins the pointer `forge skill list` prints
// after its table. An alphabetical listing of ninety skills answers "what
// exists" but not "which one first", and the measured cost of that gap was
// the largest single time bucket in a dogfood run. The pointer is only
// printed when the skill really ships, so this test also guards that it
// still does.
func TestSkillListNamesTheEntryPoint(t *testing.T) {
	skills, err := listForgeShippedSkills()
	if err != nil {
		t.Fatalf("listForgeShippedSkills: %v", err)
	}
	found := false
	for _, s := range skills {
		if s.Path == entryPointSkillPath {
			found = true
			if s.Description == "" {
				t.Errorf("entry-point skill %q has no description — it would list as a bare row", s.Path)
			}
		}
	}
	if !found {
		t.Fatalf("no shipped skill at %q — `forge skill list` would print no entry pointer, and the corpus would have no front door", entryPointSkillPath)
	}

	// Default (grouped) view: the entry point is no longer a pointer printed
	// after an alphabetical table — it LEADS, in the START HERE block above
	// the catalog. Same guarantee, stated where a reader meets it first.
	got := skillListOutput(t)
	head, _, ok := strings.Cut(got, "FORGE")
	if !ok {
		t.Fatalf("`skill list` has no FORGE catalog section:\n%s", got)
	}
	if !strings.Contains(head, entryPointSkillPath) {
		t.Errorf("`skill list` does not lead with the entry point:\n%s", got)
	}
	// The pointer must come with the catalog, not instead of it.
	if !strings.Contains(got, "db") || !strings.Contains(got, "proto") {
		t.Errorf("`skill list` lost its catalog:\n%s", got)
	}

	// --all keeps naming it the old way, after the exhaustive table.
	all := skillListOutput(t, "--all")
	if !strings.Contains(all, "skill load "+entryPointSkillPath) {
		t.Errorf("`skill list --all` does not name the entry point:\n%s", all)
	}
	if !strings.Contains(all, "PATH") {
		t.Errorf("`skill list --all` lost its table:\n%s", all)
	}
}

func skillListOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newSkillListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("skill list %v: %v", args, err)
	}
	return out.String()
}
