// Tests for the heal-notice trigger truthfulness fix.
//
// Defect: a plain `forge generate` (no flags) printed "healed stale
// codegen: ... (you opted in via --heal/--force)" for every regenerated
// pristine file. The heal actually proceeded because the pipeline's own
// writers pass force=true and no force scope is installed on a flagless
// run (forceApplies' unscoped-legacy branch) — the user never opted in
// to anything. The notice now records WHAT allowed the overwrite:
//
//   - HealTriggerImplicit  — unscoped pipeline force (plain generate);
//     the default message claims no flag opt-in.
//   - HealTriggerHealFlag  — the user passed --heal (AutoHeal).
//   - HealTriggerForceFlag — the user passed --force and the drift
//     guard scoped it to this file.
package checksums

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureHealTriggers swaps HealNoticeFn for a recorder of (path,
// trigger) pairs and silences NoHealSkipFn. Resets per-run state.
func captureHealTriggers(t *testing.T) *map[string]HealTrigger {
	t.Helper()
	got := map[string]HealTrigger{}
	origHeal, origSkip := HealNoticeFn, NoHealSkipFn
	HealNoticeFn = func(relPath string, trigger HealTrigger) { got[relPath] = trigger }
	NoHealSkipFn = func(string) {}
	t.Cleanup(func() {
		HealNoticeFn = origHeal
		NoHealSkipFn = origSkip
		ResetPerRunState()
	})
	ResetPerRunState()
	return &got
}

// healOlderVintage writes a stamped v1 render to disk, then regenerates
// with v2 through WriteGeneratedFile and flushes the deferred notices.
func healOlderVintage(t *testing.T, force bool) (rel string, wrote bool) {
	t.Helper()
	rel = "pkg/app/app_gen.go"
	root := t.TempDir()
	ResetSkipWrite()
	stampedV1, _ := Stamp(rel, []byte("package app // v1\n"))
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stampedV1, 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err := WriteGeneratedFile(root, rel, []byte("package app // v2\n"), &FileChecksums{}, force)
	if err != nil {
		t.Fatal(err)
	}
	FlushHealNotices(root)
	return rel, wrote
}

func TestHealTrigger_PlainGenerateUnscopedForceIsImplicit(t *testing.T) {
	got := captureHealTriggers(t)
	// No AutoHeal, no force scope installed — exactly the flagless
	// `forge generate` shape where writeForgeOwned passes force=true.
	rel, wrote := healOlderVintage(t, true)
	if !wrote {
		t.Fatal("unscoped pipeline force must still regenerate the older vintage")
	}
	trigger, fired := (*got)[rel]
	if !fired {
		t.Fatal("heal notice must fire — the old bytes were replaced")
	}
	if trigger != HealTriggerImplicit {
		t.Errorf("trigger = %v, want HealTriggerImplicit — the user passed no flag, the notice must not claim they did", trigger)
	}
}

func TestHealTrigger_AutoHealAttributesHealFlag(t *testing.T) {
	got := captureHealTriggers(t)
	AutoHeal = true
	rel, wrote := healOlderVintage(t, false)
	if !wrote {
		t.Fatal("--heal must regenerate the older vintage")
	}
	if trigger := (*got)[rel]; trigger != HealTriggerHealFlag {
		t.Errorf("trigger = %v, want HealTriggerHealFlag", trigger)
	}
}

func TestHealTrigger_ScopedForceAttributesForceFlag(t *testing.T) {
	got := captureHealTriggers(t)
	SetForceScope([]string{"pkg/app/app_gen.go"})
	rel, wrote := healOlderVintage(t, true)
	if !wrote {
		t.Fatal("scoped --force must regenerate the older vintage")
	}
	if trigger := (*got)[rel]; trigger != HealTriggerForceFlag {
		t.Errorf("trigger = %v, want HealTriggerForceFlag", trigger)
	}
}

// TestHealNoticeDefaultMessages pins the user-visible wording: the
// implicit (flagless) message must never claim a flag opt-in, and the
// flagged variants must name exactly the flag that was passed.
func TestHealNoticeDefaultMessages(t *testing.T) {
	capture := func(trigger HealTrigger) string {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := os.Stderr
		os.Stderr = w
		HealNoticeFn("pkg/app/app_gen.go", trigger)
		os.Stderr = orig
		_ = w.Close()
		out := make([]byte, 4096)
		n, _ := r.Read(out)
		_ = r.Close()
		return string(out[:n])
	}

	implicit := capture(HealTriggerImplicit)
	if strings.Contains(implicit, "opted in") {
		t.Errorf("implicit heal message claims a flag opt-in the user never made:\n%s", implicit)
	}
	if !strings.Contains(implicit, "pkg/app/app_gen.go") {
		t.Errorf("implicit heal message must name the file:\n%s", implicit)
	}

	healFlag := capture(HealTriggerHealFlag)
	if !strings.Contains(healFlag, "you opted in via --heal") || strings.Contains(healFlag, "--heal/--force") {
		t.Errorf("--heal message must name exactly --heal:\n%s", healFlag)
	}

	forceFlag := capture(HealTriggerForceFlag)
	if !strings.Contains(forceFlag, "you opted in via --force") || strings.Contains(forceFlag, "--heal/--force") {
		t.Errorf("--force message must name exactly --force:\n%s", forceFlag)
	}
}

// Tone contract for the heal notice (healNoticeText). A heal only ever
// replaces hash-certified PRISTINE prior renders, and the implicit
// trigger is routine pipeline regeneration — on a fresh scaffold it
// fires for every re-rendered file. That message must be a calm
// one-liner: no "restore it", no "disown", no implication that a user
// edit may have been destroyed (that wording trained users to ignore
// the notice — a minutes-old project printed seven scary warnings for
// normal regeneration). The explicit --heal/--force notices keep the
// restore/disown escape hatch: there the user deliberately discarded
// content, which IS the case where a hash-colliding deliberate revert
// could be lost.
func TestHealNoticeText_ImplicitIsCalm(t *testing.T) {
	implicit := healNoticeText("pkg/app/app_gen.go", HealTriggerImplicit)
	if !strings.Contains(implicit, "pkg/app/app_gen.go") {
		t.Errorf("implicit notice must name the file:\n%s", implicit)
	}
	for _, scary := range []string{"restore", "disown", "deliberate edit", "stale codegen"} {
		if strings.Contains(implicit, scary) {
			t.Errorf("implicit (routine regeneration) notice must not carry the %q warning:\n%s", scary, implicit)
		}
	}
	if n := strings.Count(strings.TrimRight(implicit, "\n"), "\n"); n != 0 {
		t.Errorf("implicit notice must be a one-liner, got %d extra line(s):\n%s", n, implicit)
	}

	for _, tc := range []struct {
		name    string
		trigger HealTrigger
	}{
		{"--heal", HealTriggerHealFlag},
		{"--force", HealTriggerForceFlag},
	} {
		got := healNoticeText("pkg/app/app_gen.go", tc.trigger)
		if !strings.Contains(got, "restore it") || !strings.Contains(got, "forge project disown") {
			t.Errorf("%s notice must keep the restore/disown remedy (explicit destructive opt-in):\n%s", tc.name, got)
		}
	}
}
