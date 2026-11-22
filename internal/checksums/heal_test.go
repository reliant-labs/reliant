// Tests for the heal contract.
//
// FRICTION cp-forge fr-2c1c2328c7: a hand-edit to pkg/app/bootstrap.go
// happened to hash equal to a PRIOR render in the checksum history, so
// generate classified it as stale codegen and silently reverted it —
// the round's only silent destruction of user work. The CORRECTNESS FIX
// is that healing (overwriting a pristine-but-older-vintage file with
// the current template) is now OPT-IN, not the default: a
// pristine-but-stale file is byte-indistinguishable from a deliberate
// edit, so the default is to SKIP it (NoHealSkipFn), never to silently
// overwrite. Healing is what lets `forge generate --heal` (or `forge
// upgrade --force`) advance stale codegen forward — but only when the
// user explicitly asks, and even then it is LOUD.
//
// Self-certifying-era contract pinned here:
//
//   - The manifest's current-hash-vs-history distinction is GONE: a
//     verifying marker proves a pristine render of SOME vintage. ANY
//     pristine on-disk body that regeneration would change is a
//     heal candidate.
//   - DEFAULT (AutoHeal off): the write SKIPS the pristine-but-stale file
//     and NoHealSkipFn fires once per file per run — forge never
//     silently reverts the user's bytes.
//   - OPT-IN (AutoHeal=true, set by `--heal`; or a scoped --force):
//     the write proceeds and healing is LOUD — HealNoticeFn fires once
//     per file per run.
//   - Notices are DEFERRED: the writer records a pending heal and
//     FlushHealNotices(root) fires the notice only when the FINAL
//     on-disk body differs from the replaced pristine body — so
//     formatting-only churn (goimports converging back) stays silent.
//   - No notice when nothing is destroyed: the new render is
//     body-identical, or a true hand-edit (write skipped entirely).
package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// captureHealNotices swaps HealNoticeFn (and silences NoHealSkipFn) for
// recorders, resets per-run state, and returns the recorded-paths slice
// pointer.
func captureHealNotices(t *testing.T) *[]string {
	t.Helper()
	var got []string
	origHeal, origSkip := HealNoticeFn, NoHealSkipFn
	HealNoticeFn = func(relPath string, _ HealTrigger) { got = append(got, relPath) }
	NoHealSkipFn = func(string) {} // no-op, never nil
	t.Cleanup(func() {
		HealNoticeFn = origHeal
		NoHealSkipFn = origSkip
		ResetPerRunState()
	})
	ResetPerRunState()
	return &got
}

func TestWriteGeneratedFile_HealIsLoud(t *testing.T) {
	renderV1 := []byte("package app // v1\n")
	renderV3 := []byte("package app // v3\n")
	const rel = "pkg/app/app_gen.go"

	stampedV1, _ := Stamp(rel, renderV1)

	tests := []struct {
		name        string
		onDisk      []byte
		write       []byte
		wantWrote   bool
		wantNotices int
	}{
		{
			// The legacy "on-disk matches current hash → silent regen"
			// case has no marker-era equivalent: without history there is
			// no "current vs prior" distinction. Its surviving silent leg
			// is the body-identical regen below.
			name:        "on-disk is the stamped render of the incoming content — no byte change, no notice",
			onDisk:      stampedV1,
			write:       renderV1,
			wantWrote:   true,
			wantNotices: 0,
		},
		{
			name:        "on-disk is a stamped older vintage — heal, LOUD",
			onDisk:      stampedV1,
			write:       renderV3,
			wantWrote:   true,
			wantNotices: 1,
		},
		{
			name:        "true hand-edit — write skipped, no heal notice",
			onDisk:      append(append([]byte{}, stampedV1...), []byte("// user edit\n")...),
			write:       renderV3,
			wantWrote:   false,
			wantNotices: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureHealNotices(t)
			// Healing only happens when the user opts in. captureHealNotices
			// resets per-run state (clearing AutoHeal), so opt in AFTER it.
			AutoHeal = true
			t.Cleanup(func() { AutoHeal = false })
			ResetSkipWrite()
			root := t.TempDir()
			cs := &FileChecksums{}

			full := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, tt.onDisk, 0o644); err != nil {
				t.Fatal(err)
			}

			wrote, err := WriteGeneratedFileTier1(root, rel, tt.write, cs, false)
			if err != nil {
				t.Fatal(err)
			}
			if wrote != tt.wantWrote {
				t.Errorf("wrote = %v, want %v", wrote, tt.wantWrote)
			}
			// Notices are deferred: nothing fires until the flush.
			if len(*got) != 0 {
				t.Errorf("heal notice fired before FlushHealNotices: %v", *got)
			}
			FlushHealNotices(root)
			if len(*got) != tt.wantNotices {
				t.Errorf("heal notices = %v, want %d", *got, tt.wantNotices)
			}
			if tt.wantNotices > 0 && (*got)[0] != rel {
				t.Errorf("heal notice named %q, want the relPath", (*got)[0])
			}
		})
	}
}

// TestFlushHealNotices_FormattingOnlyChurnIsSilent pins the deferral's
// purpose: when the final on-disk body converges back to the replaced
// pristine body (post-write formatters undoing the emitter's raw
// output), no notice fires — nothing was destroyed.
func TestFlushHealNotices_FormattingOnlyChurnIsSilent(t *testing.T) {
	got := captureHealNotices(t)
	AutoHeal = true // opt in AFTER captureHealNotices (it clears AutoHeal)
	t.Cleanup(func() { AutoHeal = false })
	ResetSkipWrite()
	root := t.TempDir()
	cs := &FileChecksums{}
	const rel = "a.go"

	renderV1 := []byte("package app // v1\n")
	stampedV1, _ := Stamp(rel, renderV1)
	if err := os.WriteFile(filepath.Join(root, rel), stampedV1, 0o644); err != nil {
		t.Fatal(err)
	}

	// The write replaces pristine v1 with v2 — a pending heal.
	if _, err := WriteGeneratedFile(root, rel, []byte("package app // v2\n"), cs, false); err != nil {
		t.Fatal(err)
	}
	// A "formatter" rewrites the file back to body-equal v1 before the
	// flush (the converged case).
	if err := os.WriteFile(filepath.Join(root, rel), stampedV1, 0o644); err != nil {
		t.Fatal(err)
	}
	FlushHealNotices(root)
	if len(*got) != 0 {
		t.Errorf("formatting-only heal must be silent; notices = %v", *got)
	}
}

func TestWriteGeneratedFile_HealNoticeDedupedPerRun(t *testing.T) {
	got := captureHealNotices(t)
	AutoHeal = true // opt in AFTER captureHealNotices (it clears AutoHeal)
	t.Cleanup(func() { AutoHeal = false })
	ResetSkipWrite()
	root := t.TempDir()
	cs := &FileChecksums{}
	const rel = "a.go"
	renderV1 := []byte("package app // v1\n")
	stampedV1, _ := Stamp(rel, renderV1)

	// Two writes in the same run (different emitters can touch the same
	// path) — one notice. Restore the stale vintage between writes so
	// both calls see the heal condition.
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(root, rel), stampedV1, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteGeneratedFile(root, rel, []byte("package app // v3\n"), cs, false); err != nil {
			t.Fatal(err)
		}
	}
	FlushHealNotices(root)
	FlushHealNotices(root) // double flush must not re-notice
	if len(*got) != 1 {
		t.Errorf("heal notices = %v, want exactly 1 (per-run dedupe)", *got)
	}

	// A new run (ResetPerRunState) notices again. ResetPerRunState also
	// clears AutoHeal, so re-opt-in to keep exercising the heal path.
	ResetPerRunState()
	AutoHeal = true
	if err := os.WriteFile(filepath.Join(root, rel), stampedV1, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGeneratedFile(root, rel, []byte("package app // v4\n"), cs, false); err != nil {
		t.Fatal(err)
	}
	FlushHealNotices(root)
	if len(*got) != 2 {
		t.Errorf("heal notices after new run = %v, want 2", *got)
	}
}

// TestWriteScaffoldIfMissing_PreservesExistingSilently — scaffold writes
// carry no marker and never heal: an existing file (even a pristine prior
// forge render) is left untouched, silently, with no heal notice. The
// only refresh is delete-then-regenerate.
func TestWriteScaffoldIfMissing_PreservesExistingSilently(t *testing.T) {
	got := captureHealNotices(t)
	ResetSkipWrite()
	root := t.TempDir()

	// A file already on disk (whatever its provenance).
	const rel = "svc.go"
	if err := os.WriteFile(filepath.Join(root, rel), []byte("// user code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err := WriteScaffoldIfMissing(root, rel, []byte("// new template\n"))
	if err != nil || wrote {
		t.Errorf("existing file overwritten by scaffold write: wrote=%v err=%v", wrote, err)
	}
	FlushHealNotices(root)
	if len(*got) != 0 {
		t.Errorf("preserving an existing scaffold file must not fire a heal notice; got %v", *got)
	}
}

// TestDefault_TreatsStaleAsHandEdit pins the DEFAULT (AutoHeal off) —
// the correctness fix for fr-2c1c2328c7. A pristine-but-stale file is
// treated as a possible hand-edit: the write skips it, content is
// preserved, and NoHealSkipFn (not HealNoticeFn) fires once per file per
// run. No flag is needed — this is what plain `forge generate` does.
//
// The legacy assertion that CheckTier1Drift reported the file flagged
// HistoricalMatch has no equivalent: ScanTier1Drift never sees the
// fresh render, so a pristine vintage cannot be distinguished from the
// current one there — the per-write NoHealSkipFn notice is the
// replacement signal.
func TestDefault_TreatsStaleAsHandEdit(t *testing.T) {
	got := captureHealNotices(t)
	var skips []string
	NoHealSkipFn = func(relPath string) { skips = append(skips, relPath) }
	ResetSkipWrite()
	// AutoHeal defaults off; assert that explicitly — the whole point is
	// that no flag is required for the safe behavior.
	AutoHeal = false

	root := t.TempDir()
	cs := &FileChecksums{}
	const rel = "pkg/app/app_gen.go"
	renderV1 := []byte("package app // v1\n")
	stampedV1, _ := Stamp(rel, renderV1)
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stampedV1, 0o644); err != nil {
		t.Fatal(err)
	}

	// The write is skipped, content preserved, no heal notice — the
	// skip notice fires instead, once per run even across two writes.
	for i := 0; i < 2; i++ {
		wrote, err := WriteGeneratedFile(root, rel, []byte("package app // v3\n"), cs, false)
		if err != nil {
			t.Fatal(err)
		}
		if wrote {
			t.Error("write proceeded by default on a pristine-but-stale file — silent revert hazard")
		}
	}
	if content, _ := os.ReadFile(full); string(content) != string(stampedV1) {
		t.Errorf("on-disk content not preserved by default; got %q", content)
	}
	if len(skips) != 1 || skips[0] != rel {
		t.Errorf("NoHealSkipFn calls = %v, want exactly one naming %s", skips, rel)
	}
	FlushHealNotices(root)
	if len(*got) != 0 {
		t.Errorf("no heal notice expected by default (nothing was healed); got %v", *got)
	}

	// The file stays pristine, so the stomp guard does NOT flag it.
	if drift := ScanTier1Drift(root, cs); len(drift) != 0 {
		t.Errorf("pristine-but-stale file must not appear as drift; got %+v", drift)
	}
}

// TestWriteGeneratedFile_HandEditCollidingWithPriorRender_NotReverted is
// the direct regression test for FRICTION cp-forge fr-2c1c2328c7.
//
// The real incident: pkg/app/bootstrap.go carried an uncommitted
// hand-edit whose content hash equaled a PRIOR render that forge had
// once written. The stale-detection logic saw a pristine-looking older
// vintage and silently reverted the user's edit to the current template
// — destroying real work with no warning.
//
// We reproduce it concretely. Forge's history for this path is
// v1 (oldest) → v2 → current=v3. The user reverts/edits the file so its
// on-disk bytes are EXACTLY the v1 render — content that collides with
// an OLDER entry in forge's render history but is NOT what forge last
// wrote and is NOT the current template. The hazard is that forge treats
// this as "stale codegen, safe to regenerate" and overwrites it.
//
// Assert the fix: a plain `forge generate` (AutoHeal off, force off)
// must NOT overwrite the file — it skips, preserves the user's bytes,
// and names the file via NoHealSkipFn. Only an explicit --heal/--force
// may overwrite.
func TestWriteGeneratedFile_HandEditCollidingWithPriorRender_NotReverted(t *testing.T) {
	const rel = "pkg/app/bootstrap.go"
	// The three vintages forge has rendered for this path over time.
	renderV1 := []byte("package app\n\n// bootstrap v1 (oldest prior render)\n")
	renderV3 := []byte("package app\n\n// bootstrap v3 (current template)\n")

	// The user's working tree has the file reverted to the v1 render —
	// a COMPLETE pristine prior render (marker + body self-consistent),
	// which is exactly the "hash collides with a prior render in history"
	// shape that the old logic mistook for stale codegen.
	onDiskV1, ok := Stamp(rel, renderV1)
	if !ok {
		t.Fatal("Stamp failed for a .go path")
	}
	if Verify(onDiskV1) != Pristine {
		t.Fatalf("setup: v1 render must self-verify, got %v", Verify(onDiskV1))
	}

	var skips []string
	origSkip, origHeal := NoHealSkipFn, HealNoticeFn
	NoHealSkipFn = func(p string) { skips = append(skips, p) }
	var heals []string
	HealNoticeFn = func(p string, _ HealTrigger) { heals = append(heals, p) }
	t.Cleanup(func() { NoHealSkipFn, HealNoticeFn = origSkip, origHeal })

	ResetSkipWrite()
	ResetPerRunState() // AutoHeal off, force off — a plain `forge generate`
	t.Cleanup(ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, onDiskV1, 0o644); err != nil {
		t.Fatal(err)
	}

	// Forge regenerates the path with the CURRENT template (v3).
	wrote, err := WriteGeneratedFile(root, rel, renderV3, cs, false)
	if err != nil {
		t.Fatal(err)
	}

	// The core assertion: the user's bytes survive untouched.
	if wrote {
		t.Error("forge silently overwrote a forge-owned file whose content collided with a PRIOR render — the fr-2c1c2328c7 silent revert regressed")
	}
	after, _ := os.ReadFile(full)
	if string(after) != string(onDiskV1) {
		t.Errorf("user's on-disk content was reverted; got %q, want the v1 bytes preserved", after)
	}
	FlushHealNotices(root)
	if len(heals) != 0 {
		t.Errorf("no heal must happen by default; HealNoticeFn fired for %v", heals)
	}
	if len(skips) != 1 || skips[0] != rel {
		t.Errorf("forge must loudly name the skipped file; NoHealSkipFn calls = %v, want one naming %s", skips, rel)
	}

	// And the escape hatch still works: an explicit --force on this path
	// overwrites it (the user opting to throw the bytes away).
	SetForceScope([]string{rel})
	wrote, err = WriteGeneratedFile(root, rel, renderV3, cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("--force on the drifted path must overwrite (the opt-in escape hatch)")
	}
	after, _ = os.ReadFile(full)
	wantV3, _ := Stamp(rel, renderV3)
	if string(after) != string(wantV3) {
		t.Errorf("--force did not regenerate to current template; got %q", after)
	}
}
