package checksums

import (
	"strings"
	"testing"
)

// TestRetireObsoleteDisowns pins the three-way decision: a disown is
// retired ONLY when its path is no longer a Tier-1 target AND an emitter
// that could own it actually ran. A path still in the target set, or one
// whose owning emitter didn't run, keeps its disown (conservative).
func TestRetireObsoleteDisowns(t *testing.T) {
	prevTargets := Tier1TargetSet
	prevNotice := RetireNoticeFn
	t.Cleanup(func() { Tier1TargetSet = prevTargets; RetireNoticeFn = prevNotice })

	// Only the handler file is a live Tier-1 target this run.
	Tier1TargetSet = map[string]bool{"handlers/x/handlers_crud_gen.go": true}
	var notices []string
	RetireNoticeFn = func(p string) { notices = append(notices, p) }

	cs := &FileChecksums{Disowned: map[string]DisownedEntry{
		// Obsolete: a frontend page that became Tier-2 scaffold-once — not
		// in the target set, and the frontend emitter ran → RETIRE.
		"frontends/admin-web/src/app/page.tsx": {Reason: "stale legacy disown"},
		// Still Tier-1: forge would regenerate it but for the disown → KEEP.
		"handlers/x/handlers_crud_gen.go": {Reason: "genuine customization"},
		// Owning emitter did not run (targetable=false) → KEEP (conservative).
		"deploy/kcl/dev/main.k": {Reason: "deploy off this run"},
	}}

	// Frontend emitter ran; deploy did not.
	targetable := func(p string) bool { return strings.HasPrefix(p, "frontends/") }

	retired := RetireObsoleteDisowns(cs, targetable)

	if len(retired) != 1 || retired[0] != "frontends/admin-web/src/app/page.tsx" {
		t.Fatalf("retired = %v, want only the obsolete frontend page", retired)
	}
	if _, still := cs.Disowned["frontends/admin-web/src/app/page.tsx"]; still {
		t.Error("obsolete disown must be removed from cs.Disowned")
	}
	if _, kept := cs.Disowned["handlers/x/handlers_crud_gen.go"]; !kept {
		t.Error("a disown on a live Tier-1 target must be kept")
	}
	if _, kept := cs.Disowned["deploy/kcl/dev/main.k"]; !kept {
		t.Error("a disown whose owning emitter didn't run must be kept (conservative)")
	}
	if len(notices) != 1 || notices[0] != "frontends/admin-web/src/app/page.tsx" {
		t.Errorf("expected exactly one loud retire notice for the page, got %v", notices)
	}
}
