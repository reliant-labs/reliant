package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

// newExplainDriftCtx builds a minimal pipelineContext over a tmpdir
// with one drifted Tier-1 file: the embedded forge:hash marker carries
// the hash of render v1, the on-disk body is the user's hand-edit —
// Verify answers Modified, exactly the stomp guard's drift signal.
// Returns the ctx, the drift set, and the exact on-disk bytes.
func newExplainDriftCtx(t *testing.T, rel string, rendered, onDisk []byte) (*pipelineContext, []checksums.Tier1DriftEntry, []byte) {
	t.Helper()
	root := t.TempDir()
	cs := &generator.FileChecksums{}

	stamped, ok := checksums.StampWithValue(rel, onDisk, checksums.BodyHash(rendered))
	if !ok {
		t.Fatalf("stamp %s: unstampable", rel)
	}
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &pipelineContext{
		ProjectDir:   root,
		AbsPath:      root,
		ExplainDrift: true,
		Checksums:    cs,
	}
	drift := scanProjectDrift(root, cs)
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift entry, got %d", len(drift))
	}
	return ctx, drift, stamped
}

// TestExplainDrift_EndToEnd walks the full mechanism at unit level:
// prepare redirects the emitter write to a side render (the drifted
// file — including its embedded marker — is never touched, so there is
// no manifest snapshot/restore dance anymore: the truth lives in the
// file), and finish prints a diff and returns the drift error.
func TestExplainDrift_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	defer checksums.ResetPerRunState()

	const rel = "pkg/app/wire_gen.go"
	rendered := []byte("package app\n\nfunc wireA() {}\n")
	onDisk := []byte("package app\n\nfunc wireA() {}\n\nfunc userEdit() {}\n")
	ctx, drift, stampedOnDisk := newExplainDriftCtx(t, rel, rendered, onDisk)

	prepareExplainDrift(ctx, drift)

	// Emitter pass: the fresh render must land in .forge/render/, not
	// over the user's file.
	fresh := []byte("package app\n\nfunc wireA() {}\n\nfunc wireB() {}\n")
	wrote, err := checksums.WriteGeneratedFile(ctx.AbsPath, rel, fresh, ctx.Checksums, false)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("explain-drift run overwrote the drifted file")
	}
	gotOnDisk, _ := os.ReadFile(filepath.Join(ctx.AbsPath, rel))
	if string(gotOnDisk) != string(stampedOnDisk) {
		t.Errorf("user content modified: %q", gotOnDisk)
	}
	// The parked render is the stamped fresh render (writes go through
	// the certification chokepoint before the side-render redirect).
	gotRender, err := os.ReadFile(filepath.Join(ctx.AbsPath, checksums.RenderDir, rel))
	if err != nil || checksums.BodyHash(gotRender) != checksums.BodyHash(fresh) {
		t.Errorf("fresh render not parked (err=%v content=%q)", err, gotRender)
	}
	// No merge base for a drifted path — a stale base would poison a
	// future merge view (fork-era invariant kept for the render dir).
	if _, err := os.Stat(filepath.Join(ctx.AbsPath, checksums.RenderBaseDir, rel)); !os.IsNotExist(err) {
		t.Errorf("explain-drift seeded render-base for a non-forked path")
	}

	var diffOut strings.Builder
	printExplainDriftDiffs(&diffOut, ctx)
	out := diffOut.String()
	for _, want := range []string{rel, "func userEdit()", "func wireB()"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q; got:\n%s", want, out)
		}
	}

	finishErr := finishExplainDrift(ctx)
	if finishErr == nil {
		t.Fatal("finishExplainDrift must return the drift error")
	}
	if !strings.Contains(finishErr.Error(), "Tier-1 file-stomp guard") {
		t.Errorf("error should carry the standard drift report; got %q", finishErr.Error())
	}
	// The drift must NOT have been blessed: the on-disk file still fails
	// its own certification (the manifest-era snapshot-restore assertion,
	// re-pointed at the file itself).
	finalOnDisk, _ := os.ReadFile(filepath.Join(ctx.AbsPath, rel))
	if checksums.Verify(finalOnDisk) != checksums.Modified {
		t.Errorf("drifted file no longer verifies as Modified — the run blessed the drift")
	}
}

// TestExplainDrift_DiffTruncation pins the bounded-diff contract.
func TestExplainDrift_DiffTruncation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	defer checksums.ResetPerRunState()

	origCap := explainDriftDiffCapLines
	explainDriftDiffCapLines = 5
	defer func() { explainDriftDiffCapLines = origCap }()

	const rel = "pkg/app/wire_gen.go"
	rendered := []byte("package app\n// r1\n// r2\n// r3\n// r4\n// r5\n// r6\n// r7\n// r8\n")
	onDisk := []byte("package app\n// e1\n// e2\n// e3\n// e4\n// e5\n// e6\n// e7\n// e8\n")
	ctx, drift, _ := newExplainDriftCtx(t, rel, rendered, onDisk)

	prepareExplainDrift(ctx, drift)
	if _, err := checksums.WriteGeneratedFile(ctx.AbsPath, rel, rendered, ctx.Checksums, false); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	printExplainDriftDiffs(&b, ctx)
	out := b.String()
	if !strings.Contains(out, "diff truncated at 5 lines") {
		t.Errorf("missing truncation note; got:\n%s", out)
	}
	if !strings.Contains(out, checksums.SideRenderRelPath(rel)) {
		t.Errorf("truncation note should point at the full render; got:\n%s", out)
	}
}

// TestExplainDrift_NoRenderProduced: when the drifted file's emitter
// never ran (step preset / gate), the diff section says so instead of
// failing.
//
// The path is registered as a Tier-1 TARGET without being written, which
// is what a gated-off emitter looks like from here: forge still owns the
// path, it just produced nothing this run. That distinction is what the
// note reports — see explainDriftNoRenderNote, which tells this apart from
// a path forge no longer emits at all (where no flag can produce a diff).
func TestExplainDrift_NoRenderProduced(t *testing.T) {
	checksums.ResetPerRunState()
	defer checksums.ResetPerRunState()

	const rel = "pkg/app/wire_gen.go"
	ctx, drift, _ := newExplainDriftCtx(t, rel, []byte("package app\n"), []byte("package app // edit\n"))
	prepareExplainDrift(ctx, drift)
	// Forge still owns the path; its emitter step just didn't run.
	checksums.MarkTier1Target(rel)

	var b strings.Builder
	printExplainDriftDiffs(&b, ctx)
	if !strings.Contains(b.String(), "no fresh render this run") {
		t.Errorf("missing no-render note; got:\n%s", b.String())
	}
}

// TestExplainDrift_NoRenderBecauseNoLongerEmitted: the other half of the
// no-render case, and the one that made `--explain-drift` useless exactly
// when the drift error recommended it.
//
// A path forge has STOPPED emitting (frontends/*/src/app/dashboard_gen.tsx
// after the dashboard became scaffold-once) can never have a fresh render,
// so the note must say that plainly rather than blaming a gate the user
// could go looking for.
func TestExplainDrift_NoRenderBecauseNoLongerEmitted(t *testing.T) {
	checksums.ResetPerRunState()
	defer checksums.ResetPerRunState()

	const rel = "frontends/web/src/app/dashboard_gen.tsx"
	ctx, drift, _ := newExplainDriftCtx(t, rel,
		[]byte("export const x = 1;\n"), []byte("export const x = 2; // edit\n"))
	prepareExplainDrift(ctx, drift)
	// Deliberately NOT a Tier-1 target: no emitter claims this path.

	var b strings.Builder
	printExplainDriftDiffs(&b, ctx)
	out := b.String()
	if !strings.Contains(out, "no longer emits this file") {
		t.Errorf("note must say forge stopped emitting the file; got:\n%s", out)
	}
	if strings.Contains(out, "--steps") {
		t.Errorf("note points at --steps, which cannot produce a render here; got:\n%s", out)
	}
}

// TestFinishExplainDrift_NoOpWithoutDrift: plain runs (no flag, or no
// drift) pass through untouched.
func TestFinishExplainDrift_NoOpWithoutDrift(t *testing.T) {
	ctx := &pipelineContext{}
	if err := finishExplainDrift(ctx); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}
