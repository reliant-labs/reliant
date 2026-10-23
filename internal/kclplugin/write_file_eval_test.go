package kclplugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// fp.write_file is consumed from KCL, so these tests evaluate real modules
// through kclrender.Run — the seam `forge ci validate-kcl`, `forge doctor`,
// `forge generate`'s KCL queries and every env command render through.
//
// What they guard: control-plane's dev KCL generated its TRACKED
// deploy/nats/nats.conf with KCL's own file.write, which fires on EVERY
// evaluation. `forge ci validate-kcl` renders dev without the dev-stack
// roster, so it rewrote the committed file with the default account only,
// deleting every other worktree's account — a check that mutated the tree it
// was checking.

// writeWriterModule lays down a module that generates `out` with content
// derived from a per-render option, so two renders are distinguishable on
// disk.
func writeWriterModule(t *testing.T, dir, call string) {
	t.Helper()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"write_probe\"\n",
		"main.k": `import file
import kcl_plugin.forge as fp

_content = "generated for " + option("stamp") + "\n"
written = ` + call + `
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func render(t *testing.T, dir, stamp string) string {
	t.Helper()
	out, err := kclrender.Run(dir, dir, []string{"stamp=" + stamp})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

// TestKCLFileWriteMutatesTheTreeOnAReadOnlyRender pins the HAZARD, so the
// reason for write_file cannot quietly stop being true. A module using KCL's
// file.write rewrites its target on any render — including the unarmed ones
// forge's read-only commands perform — and there is nothing forge can do
// about it from outside the KCL runtime.
//
// file.write resolves a relative path against the PROCESS working directory,
// not the render's work dir — which is the project root whenever forge is run
// from it, as every forge command is. Hence the Chdir.
func TestKCLFileWriteMutatesTheTreeOnAReadOnlyRender(t *testing.T) {
	kclplugin.UseFileWriter("")
	dir := t.TempDir()
	t.Chdir(dir)
	writeWriterModule(t, dir, `file.write("committed.conf", _content)`)
	target := filepath.Join(dir, "committed.conf")
	if err := os.WriteFile(target, []byte("committed bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	render(t, dir, "validate-kcl")

	// If this starts failing because the file is untouched, KCL's file.write
	// stopped firing during evaluation — write_file's reason for existing
	// has changed, and this test (and materialize.go's rationale) should be
	// revisited, not skipped.
	if got, _ := os.ReadFile(target); string(got) != "generated for validate-kcl\n" {
		t.Fatalf("an unarmed render through file.write left %q; expected it to rewrite the committed file "+
			"(the hazard fp.write_file exists to remove)", got)
	}
}

// TestWriteFileWritesNothingOnAReadOnlyRender is the fix. Unarmed — every
// render except `env up` and an applying `env deploy` of a local env — the
// builtin leaves the committed file byte-for-byte alone, still returns the
// path so a module that binds it evaluates identically, and records what it
// declined to write.
func TestWriteFileWritesNothingOnAReadOnlyRender(t *testing.T) {
	kclplugin.UseFileWriter("")
	_ = kclplugin.SuppressedWrites()
	dir := t.TempDir()
	writeWriterModule(t, dir, `fp.write_file("deploy/nats/nats.conf", _content)`)
	target := filepath.Join(dir, "deploy", "nats", "nats.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("committed bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := render(t, dir, "validate-kcl")

	if got, _ := os.ReadFile(target); string(got) != "committed bytes\n" {
		t.Errorf("a read-only render rewrote the committed file: %q", got)
	}
	if !strings.Contains(out, "deploy/nats/nats.conf") {
		t.Errorf("write_file must return its path even when it writes nothing; render:\n%s", out)
	}
	if got := kclplugin.SuppressedWrites(); len(got) != 1 || got[0] != "deploy/nats/nats.conf" {
		t.Errorf("SuppressedWrites() = %v, want the one declined path", got)
	}
}

// TestWriteFileWritesWhenArmed is the other half: the commands that launch
// the env must still get their generated file, or the fix just breaks dev.
func TestWriteFileWritesWhenArmed(t *testing.T) {
	dir := t.TempDir()
	kclplugin.UseFileWriter(dir)
	t.Cleanup(func() { kclplugin.UseFileWriter("") })
	writeWriterModule(t, dir, `fp.write_file("deploy/nats/nats.conf", _content)`)
	target := filepath.Join(dir, "deploy", "nats", "nats.conf")

	render(t, dir, "env-up")
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "generated for env-up\n" {
		t.Fatalf("armed write_file did not materialize the file (creating parents): %q, %v", got, err)
	}

	// Byte-identical re-render: the file is left alone, so a watcher that
	// reloads on change (control-plane SIGHUPs NATS) does not fire for
	// nothing, and mtimes stay meaningful.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	render(t, dir, "env-up")
	if info, _ := os.Stat(target); !info.ModTime().Equal(old) {
		t.Errorf("an identical re-render rewrote the file (mtime %v, want %v)", info.ModTime(), old)
	}

	// A real change is written.
	render(t, dir, "env-up-2")
	if got, _ := os.ReadFile(target); string(got) != "generated for env-up-2\n" {
		t.Errorf("a changed render was not written: %q", got)
	}
	// And no temp file is left beside it.
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("write left extra files next to the target: %v", entries)
	}
}

// TestWriteFileSharedWritesToThePrimaryCheckout: shared=True lands in the
// shared root (the primary checkout), and ONLY there. The incident: each
// worktree wrote its own copy of the shared NATS config, the copies diverged,
// and the one NATS mounted whichever checkout had last run compose.
func TestWriteFileSharedWritesToThePrimaryCheckout(t *testing.T) {
	worktree, primary := t.TempDir(), t.TempDir()
	kclplugin.UseFileWriter(worktree, primary)
	t.Cleanup(func() { kclplugin.UseFileWriter("") })

	for _, call := range []string{
		`fp.write_file("deploy/nats/nats.conf", _content, shared=True)`,
		`fp.write_file("deploy/nats/nats.conf", _content, True)`,
	} {
		_ = os.RemoveAll(filepath.Join(primary, "deploy"))
		writeWriterModule(t, worktree, call)
		render(t, worktree, "env-up")

		got, err := os.ReadFile(filepath.Join(primary, "deploy", "nats", "nats.conf"))
		if err != nil || string(got) != "generated for env-up\n" {
			t.Errorf("%s: shared write did not land in the primary checkout: %q, %v", call, got, err)
		}
		if _, err := os.Stat(filepath.Join(worktree, "deploy", "nats", "nats.conf")); err == nil {
			t.Errorf("%s: shared write ALSO wrote the worktree's copy — two copies is the defect", call)
		}
	}

	// shared=False (the default) is unchanged: the current checkout.
	writeWriterModule(t, worktree, `fp.write_file("local.conf", _content, shared=False)`)
	render(t, worktree, "env-up")
	if _, err := os.Stat(filepath.Join(worktree, "local.conf")); err != nil {
		t.Errorf("shared=False must write the current checkout: %v", err)
	}

	// A non-bool is refused rather than guessed.
	writeWriterModule(t, worktree, `fp.write_file("x.conf", _content, shared="yes")`)
	if _, err := kclrender.Run(worktree, worktree, []string{"stamp=x"}); err == nil {
		t.Errorf(`shared="yes" was accepted; a non-bool must be refused`)
	}
}

// TestWriteFileSharedIsInertWhenUnarmed: a read-only render declines a shared
// write exactly like a local one — it must not reach into the primary checkout.
func TestWriteFileSharedIsInertWhenUnarmed(t *testing.T) {
	kclplugin.UseFileWriter("")
	_ = kclplugin.SuppressedWrites()
	dir := t.TempDir()
	writeWriterModule(t, dir, `fp.write_file("deploy/nats/nats.conf", _content, shared=True)`)
	render(t, dir, "validate-kcl")
	if got := kclplugin.SuppressedWrites(); len(got) != 1 {
		t.Errorf("SuppressedWrites() = %v, want the declined shared path", got)
	}
}

// TestWriteFileStaysInsideTheProject: an armed render still may not write
// outside the tree it is materializing.
func TestWriteFileStaysInsideTheProject(t *testing.T) {
	dir := t.TempDir()
	kclplugin.UseFileWriter(dir)
	t.Cleanup(func() { kclplugin.UseFileWriter("") })

	outside := filepath.Join(filepath.Dir(dir), "escaped-"+filepath.Base(dir))
	for _, call := range []string{
		`fp.write_file("../escaped-` + filepath.Base(dir) + `", _content)`,
		`fp.write_file("` + outside + `", _content)`,
	} {
		writeWriterModule(t, dir, call)
		if _, err := kclrender.Run(dir, dir, []string{"stamp=x"}); err == nil {
			t.Errorf("%s: render succeeded; a path outside the project root must be refused", call)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("%s: wrote outside the project root", call)
			_ = os.Remove(outside)
		}
	}
}
