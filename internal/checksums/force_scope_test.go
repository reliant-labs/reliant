// Tests for the --force scope chokepoint.
//
// Journey fr-a04f8c0609: recovering from a Tier-1 stomp-guard trip via
// `forge generate --force` overwrote files the guard never flagged.
// --force means "discard the edits the guard just told me about", not
// "force-overwrite anything any emitter touches this run" — so the
// pipeline now scopes force to the exact path set the guard reported,
// via SetForceScope. When no scope is installed (non-pipeline callers
// like `forge project upgrade`, or presets that deliberately skip the guard),
// force keeps its historical unscoped meaning.
package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHandEdited materializes the "user hand-edited a tracked file"
// state at root/rel: a stamped forge render mutated after stamping, so
// the marker survives but no longer verifies (Verify == Modified).
func writeHandEdited(t *testing.T, root, rel string) []byte {
	t.Helper()
	stamped, ok := Stamp(rel, []byte("// as generated\n"))
	if !ok {
		t.Fatalf("Stamp(%q): unstampable", rel)
	}
	edited := append(stamped, []byte("// hand edit "+rel+"\n")...)
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if Verify(edited) != Modified {
		t.Fatalf("fixture must verify Modified; got %v", Verify(edited))
	}
	return edited
}

func TestForceScope_LimitsForceToFlaggedPaths(t *testing.T) {
	ResetSkipWrite()
	ResetPerRunState()
	t.Cleanup(ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}

	// Two tracked, hand-edited files. The guard flagged only flagged.go.
	writeHandEdited(t, root, "flagged.go")
	unflaggedContent := writeHandEdited(t, root, "unflagged.go")

	SetForceScope([]string{"flagged.go"})

	// force=true on the flagged path overwrites.
	wrote, err := WriteGeneratedFile(root, "flagged.go", []byte("// regen\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("force=true on a guard-flagged path must overwrite")
	}

	// force=true on a path the guard did NOT flag is inert — the
	// hand-edit survives.
	wrote, err = WriteGeneratedFile(root, "unflagged.go", []byte("// regen\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("force=true outside the guard-flagged scope overwrote a hand-edited file")
	}
	got, _ := os.ReadFile(filepath.Join(root, "unflagged.go"))
	if string(got) != string(unflaggedContent) {
		t.Errorf("unflagged hand-edit destroyed; got %q", got)
	}
}

func TestForceScope_NilScopeKeepsLegacyUnscopedForce(t *testing.T) {
	ResetSkipWrite()
	ResetPerRunState() // clears any scope — nil means unscoped
	t.Cleanup(ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}
	writeHandEdited(t, root, "a.go")

	wrote, err := WriteGeneratedFile(root, "a.go", []byte("// regen\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("with no scope installed, force=true must keep its historical unscoped behavior")
	}
}

func TestForceScope_EmptyScopeMakesForceInert(t *testing.T) {
	ResetSkipWrite()
	ResetPerRunState()
	t.Cleanup(ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}
	writeHandEdited(t, root, "a.go")

	// The guard ran and flagged NOTHING (e.g. --force passed on a clean
	// tree) — force must not become a license to overwrite paths that
	// drift later in the run or that the guard never checks.
	SetForceScope(nil)

	wrote, err := WriteGeneratedFile(root, "a.go", []byte("// regen\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("force=true with an installed-but-empty scope overwrote a hand-edited file")
	}
}

// A file derived from a hand-written source (a contract package's mock_gen.go,
// rendered from contract.go) is stale the moment the source is edited. Under
// `forge generate --force` the scope is installed empty, which made the heal
// guard leave the pristine older render in place as a "possible hand edit" —
// and the validation build then failed on the very file the run declined to
// rewrite. WriteDerivedFileOutcome refreshes it whatever the force scope, and
// still leaves a genuinely hand-edited one alone.
func TestWriteDerivedFileOutcome_RefreshesPristineOlderRenderUnderEmptyForceScope(t *testing.T) {
	ResetSkipWrite()
	ResetPerRunState()
	t.Cleanup(ResetPerRunState)
	var healed, skipped []string
	origHeal, origSkip := HealNoticeFn, NoHealSkipFn
	HealNoticeFn = func(p string, _ HealTrigger) { healed = append(healed, p) }
	NoHealSkipFn = func(p string) { skipped = append(skipped, p) }
	t.Cleanup(func() { HealNoticeFn, NoHealSkipFn = origHeal, origSkip })

	root := t.TempDir()
	cs := &FileChecksums{}
	const rel = "internal/health/mock_gen.go"
	stale, ok := Stamp(rel, []byte("package health // rendered from the old contract\n"))
	if !ok {
		t.Fatal("Stamp: unstampable")
	}
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stale, 0o644); err != nil {
		t.Fatal(err)
	}

	SetForceScope(nil) // `forge generate --force` on a tree the drift guard found clean

	// The ordinary Tier-1 writer declines: byte-indistinguishable from an edit.
	if out, err := WriteGeneratedFileOutcome(root, rel, []byte("package health // new contract\n"), cs, true); err != nil || out.Wrote {
		t.Fatalf("WriteGeneratedFileOutcome wrote=%v err=%v; the heal guard is expected to decline here", out.Wrote, err)
	}

	out, err := WriteDerivedFileOutcome(root, rel, []byte("package health // new contract\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Wrote || !out.Changed {
		t.Fatalf("WriteDerivedFileOutcome = %+v, want the stale derived file rewritten", out)
	}
	got, _ := os.ReadFile(full)
	if Verify(got) != Pristine {
		t.Errorf("rewritten file must verify pristine, got %v", Verify(got))
	}
	FlushHealNotices(root)
	if len(healed) != 0 {
		t.Errorf("a source-derived refresh reverts nothing and must not print a heal notice; got %v", healed)
	}

	// A hand edit made AFTER forge stamped the file is still the user's.
	edited := append(append([]byte{}, got...), []byte("// hand edit\n")...)
	if err := os.WriteFile(full, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := WriteDerivedFileOutcome(root, rel, []byte("package health // newer\n"), cs, true); err != nil || out.Wrote {
		t.Fatalf("a hand-edited derived file was overwritten (wrote=%v err=%v)", out.Wrote, err)
	}
	if after, _ := os.ReadFile(full); string(after) != string(edited) {
		t.Errorf("hand edit destroyed:\n%s", after)
	}
}
