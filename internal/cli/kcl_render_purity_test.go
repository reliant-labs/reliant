package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// `forge generate` renders KCL only to READ from it, but evaluating a module
// fires every file.write in it. When such a write lands on a TRACKED file,
// generate's committed output stops being a pure function of committed
// inputs — see internal/cli/kcl_render_purity.go for the control-plane
// incident that motivated the guard (a gitignored .forge/blocks.json key
// leaking a junk NATS account into a tracked deploy/nats/nats.conf).
//
// These tests exercise the guard's own logic — detect a tracked file that
// went clean → dirty across a render, restore it, leave everything else
// alone — against a real git repo, without needing the KCL toolchain to
// produce the side effect.

// initGitRepo creates a git repo at dir with one commit containing files.
// Identity is set locally (never --global): this must not touch the machine's
// git config, which is shared with everything else running here.
func initGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "forge-test@example.com"},
		{"config", "user.name", "forge test"},
		{"add", "."},
		{"commit", "--quiet", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func writeGuardFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readGuardFile(t *testing.T, dir, rel string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(got)
}

// TestDirtyTrackedFiles_SeesModifiedTrackedFileOnly pins the detector the
// guard's clean → dirty comparison rests on: it must see a modified tracked
// file, and must NOT see an untracked one. Untracked files are excluded on
// purpose — a render dropping a new gitignored file into .forge/ is ordinary,
// and treating it as a violation would make the guard fire constantly.
func TestDirtyTrackedFiles_SeesModifiedTrackedFileOnly(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/nats/nats.conf": "accounts { CP_DEFAULT: {} }\n",
		"README.md":             "fixture\n",
	})

	if dirty := dirtyTrackedFiles(dir); len(dirty) != 0 {
		t.Fatalf("clean repo reported dirty files: %v", dirty)
	}

	writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { CP_DEFAULT: {} CP_prod_: {} }\n")
	writeGuardFile(t, dir, "untracked-side-effect.txt", "not under version control\n")

	dirty := dirtyTrackedFiles(dir)
	if !dirty["deploy/nats/nats.conf"] {
		t.Errorf("modified TRACKED file not reported: %v", dirty)
	}
	if dirty["untracked-side-effect.txt"] {
		t.Errorf("untracked file reported as dirty: %v", dirty)
	}
}

// TestDirtyTrackedFiles_NonRepoDisarms pins the fail-open contract: outside a
// git checkout the guard returns nil and the caller skips the comparison
// entirely. forge has to stay usable in a plain directory, and this is a
// diagnostic rather than a gate.
func TestDirtyTrackedFiles_NonRepoDisarms(t *testing.T) {
	if dirty := dirtyTrackedFiles(t.TempDir()); dirty != nil {
		t.Errorf("dirtyTrackedFiles outside a repo = %v, want nil (guard disarmed)", dirty)
	}
}

// TestRestoreTrackedFileFromHEAD_RevertsTheSideEffect is the core repair: the
// exact shape of the incident — an extra NATS account appended to a tracked
// conf — is undone byte-for-byte from HEAD.
func TestRestoreTrackedFileFromHEAD_RevertsTheSideEffect(t *testing.T) {
	const committed = "accounts {\n  CP_DEFAULT: {}\n}\n"
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{"deploy/nats/nats.conf": committed})

	writeGuardFile(t, dir, "deploy/nats/nats.conf",
		"accounts {\n  CP_DEFAULT: {}\n  CP_prod_: { user: \"control-plane-prod-\" }\n}\n")

	if err := restoreTrackedFileFromHEAD(dir, "deploy/nats/nats.conf"); err != nil {
		t.Fatalf("restoreTrackedFileFromHEAD: %v", err)
	}
	if got := readGuardFile(t, dir, "deploy/nats/nats.conf"); got != committed {
		t.Errorf("file not restored to committed bytes:\n--- got ---\n%s\n--- want ---\n%s", got, committed)
	}
	if dirty := dirtyTrackedFiles(dir); len(dirty) != 0 {
		t.Errorf("tree still dirty after restore: %v", dirty)
	}
}

// TestRestoreImpureKCLWrites_PureModuleLeavesConcurrentEdits is growth's
// friction #18. During a 6-minute generate an agent replaced two committed
// handlers_scaffold_<rpc>_test.go files with real tests; when generate
// finished they held the scaffold again. The edits landed while a KCL render
// ran, so the guard saw two tracked files go clean → dirty across the render
// and "restored" them from HEAD. A module with no file.write cannot have
// written them: someone else did, and the guard must leave their bytes.
func TestRestoreImpureKCLWrites_PureModuleLeavesConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	const scaffold = "package content_test\n\n// WantScaffoldStub row\n"
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/dev/main.k": "import forge\n\n# no side effects here\n_env = {name = \"dev\"}\n",
		"internal/handlers/content/handlers_scaffold_draft_post_test.go": scaffold,
	})
	before := dirtyTrackedFiles(dir)

	// The concurrent edit, landing while the render runs.
	const realTests = "package content_test\n\n// real assertions\n"
	writeGuardFile(t, dir, "internal/handlers/content/handlers_scaffold_draft_post_test.go", realTests)

	if restored := restoreImpureKCLWrites(dir, before, func() {}); len(restored) != 0 {
		t.Errorf("restored %v from a module that cannot write files", restored)
	}
	if got := readGuardFile(t, dir, "internal/handlers/content/handlers_scaffold_draft_post_test.go"); got != realTests {
		t.Errorf("a concurrent edit was reverted:\n--- got ---\n%s\n--- want ---\n%s", got, realTests)
	}
}

// TestRestoreImpureKCLWrites_WritingModuleRevertsProvenWrite keeps the
// control-plane repair (a file.write leaking into a tracked conf is undone)
// while making it lossless: a named file is reverted only once a second
// render writes the same bytes again, which proves they are its output.
func TestRestoreImpureKCLWrites_WritingModuleRevertsProvenWrite(t *testing.T) {
	dir := t.TempDir()
	const committed = "accounts { CP_DEFAULT: {} }\n"
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/dev/main.k": "import file\n\n_w = file.write(\"deploy/nats/nats.conf\", _conf)\n",
		"deploy/nats/nats.conf": committed,
	})
	before := dirtyTrackedFiles(dir)
	const leaked = "accounts { CP_DEFAULT: {} CP_prod_: {} }\n"
	render := func() { writeGuardFile(t, dir, "deploy/nats/nats.conf", leaked) }
	render()

	restored := restoreImpureKCLWrites(dir, before, render)
	if len(restored) != 1 || restored[0] != "deploy/nats/nats.conf" {
		t.Fatalf("restored = %v, want [deploy/nats/nats.conf]", restored)
	}
	if got := readGuardFile(t, dir, "deploy/nats/nats.conf"); got != committed {
		t.Errorf("not restored to HEAD:\n%s", got)
	}
	if kept, _ := filepath.Glob(filepath.Join(dir, ".forge/kcl-reverted/deploy/nats/nats.conf.*")); len(kept) != 0 {
		t.Errorf("proven render output still kept aside: %v", kept)
	}
}

// TestRestoreImpureKCLWrites_NamedButUnwrittenEditSurvives is the review's
// case: a module that calls file.write and also MENTIONS a tracked path (a
// constant, a doc string) that it never writes. A concurrent edit to that
// file during the render must survive: the second render leaves it at HEAD,
// which proves the dirty bytes were not the render's.
func TestRestoreImpureKCLWrites_NamedButUnwrittenEditSurvives(t *testing.T) {
	dir := t.TempDir()
	const committed = "# docs\n"
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/dev/main.k": "import file\n\n_DOC = \"docs/nats.md\"\n_w = file.write(\"deploy/nats/nats.conf\", _conf)\n",
		"deploy/nats/nats.conf": "accounts {}\n",
		"docs/nats.md":          committed,
	})
	before := dirtyTrackedFiles(dir)
	const edit = "# docs\n\nhand-written while generate ran\n"
	writeGuardFile(t, dir, "docs/nats.md", edit)

	restored := restoreImpureKCLWrites(dir, before, func() {})
	if len(restored) != 0 {
		t.Errorf("restored %v; the render never wrote them", restored)
	}
	if got := readGuardFile(t, dir, "docs/nats.md"); got != edit {
		t.Errorf("a concurrent edit to a merely named file was reverted:\n--- got ---\n%s\n--- want ---\n%s", got, edit)
	}
	if kept, _ := filepath.Glob(filepath.Join(dir, ".forge/kcl-reverted/docs/nats.md.*")); len(kept) != 0 {
		t.Errorf("a returned edit is still kept aside: %v", kept)
	}
}

// TestRestoreImpureKCLWrites_UnprovenBytesStay: when the second render writes
// different bytes (a render that is not deterministic, or an edit landing
// mid-proof), nothing proves what is on disk is the render's, so it stays and
// the first bytes are kept aside.
func TestRestoreImpureKCLWrites_UnprovenBytesStay(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/dev/main.k": "import file\n\n_w = file.write(\"out.conf\", _conf)\n",
		"out.conf":              "v0\n",
	})
	before := dirtyTrackedFiles(dir)
	writeGuardFile(t, dir, "out.conf", "v1\n")

	restored := restoreImpureKCLWrites(dir, before, func() { writeGuardFile(t, dir, "out.conf", "v2\n") })
	if len(restored) != 0 {
		t.Errorf("restored %v without proof", restored)
	}
	if got := readGuardFile(t, dir, "out.conf"); got != "v2\n" {
		t.Errorf("out.conf = %q, want the newest bytes left on disk", got)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, ".forge/kcl-reverted/out.conf.*"))
	if len(kept) != 1 {
		t.Fatalf("kept = %v, want one copy of the first bytes", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != "v1\n" {
		t.Errorf("kept copy = %q, want v1", b)
	}
}

// TestRestoreImpureKCLWrites_WritingModuleLeavesUnnamedEdits: a module that
// does call file.write can only have written a path it names. A concurrent
// edit to any other tracked file during the render is someone else's work,
// so it is left as is while the named write beside it is still undone.
func TestRestoreImpureKCLWrites_WritingModuleLeavesUnnamedEdits(t *testing.T) {
	dir := t.TempDir()
	const committed = "accounts { CP_DEFAULT: {} }\n"
	const scaffold = "package content_test\n\n// WantScaffoldStub row\n"
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/lib/nats.k": "import file\n\n_OUT = \"deploy/nats/nats.conf\"\n_w = file.write(_OUT, _conf)\n",
		"deploy/nats/nats.conf": committed,
		"internal/handlers/content/handlers_scaffold_draft_post_test.go": scaffold,
	})
	before := dirtyTrackedFiles(dir)
	render := func() { writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { CP_DEFAULT: {} CP_prod_: {} }\n") }
	render()
	const realTests = "package content_test\n\n// real assertions\n"
	writeGuardFile(t, dir, "internal/handlers/content/handlers_scaffold_draft_post_test.go", realTests)

	restored := restoreImpureKCLWrites(dir, before, render)
	if len(restored) != 1 || restored[0] != "deploy/nats/nats.conf" {
		t.Fatalf("restored = %v, want [deploy/nats/nats.conf] only", restored)
	}
	if got := readGuardFile(t, dir, "deploy/nats/nats.conf"); got != committed {
		t.Errorf("the named write was not undone:\n%s", got)
	}
	if got := readGuardFile(t, dir, "internal/handlers/content/handlers_scaffold_draft_post_test.go"); got != realTests {
		t.Errorf("a concurrent edit to a file the module never names was reverted:\n--- got ---\n%s\n--- want ---\n%s", got, realTests)
	}
}

func TestKCLFileWriteTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		path  string
		want  bool
	}{
		{"no file.write", map[string]string{"deploy/kcl/dev/main.k": "_p = \"a.conf\"\n"}, "a.conf", false},
		{"commented out", map[string]string{"deploy/kcl/dev/main.k": "# _w = file.write(\"a.conf\", \"b\")\n"}, "a.conf", false},
		{"a named write", map[string]string{"deploy/kcl/dev/main.k": "_w = file.write(\"a.conf\", \"b\")\n"}, "a.conf", true},
		{"an unnamed path", map[string]string{"deploy/kcl/dev/main.k": "_w = file.write(\"a.conf\", \"b\")\n"}, "internal/x_test.go", false},
		{"named in a shared module", map[string]string{"deploy/kcl/dev/main.k": "_w = file.write (p, body)\n", "deploy/kcl/nats.k": "_OUT = './deploy/nats/nats.conf'\n"}, "deploy/nats/nats.conf", true},
		{"relative to the .k file", map[string]string{"deploy/kcl/dev/main.k": "_w = file.write(\"out.yaml\", b)\n"}, "deploy/kcl/dev/out.yaml", true},
		{"node_modules is not the module", map[string]string{"frontends/web/node_modules/x/y.k": "file.write(\"a.conf\", \"b\")\n"}, "a.conf", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				writeGuardFile(t, dir, rel, body)
			}
			if got := kclFileWriteTargets(dir)[tc.path]; got != tc.want {
				t.Errorf("kclFileWriteTargets(...)[%q] = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestRenderKCLPure_LeavesPreexistingEditsAlone is the safety property that
// makes restoring acceptable at all. This checkout is routinely shared with
// other agents, so the guard may only revert a file it PROVED was clean
// immediately before the render — a file that was already dirty going in
// carries someone's uncommitted work and must survive untouched.
//
// It drives the comparison directly rather than through renderKCLPure, which
// would need the KCL toolchain; the logic under test is the before/after set
// difference, and that is what this reproduces.
func TestRenderKCLPure_LeavesPreexistingEditsAlone(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/nats/nats.conf": "accounts { CP_DEFAULT: {} }\n",
		"internal/work.go":      "package work\n",
	})

	// Another agent's uncommitted edit, present BEFORE the render.
	const inFlight = "package work\n\n// half-finished change\n"
	writeGuardFile(t, dir, "internal/work.go", inFlight)
	before := dirtyTrackedFiles(dir)
	if !before["internal/work.go"] {
		t.Fatalf("fixture setup: pre-existing edit not seen as dirty: %v", before)
	}

	// The render's side effect lands on a DIFFERENT, previously-clean file.
	writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { CP_DEFAULT: {} CP_prod_: {} }\n")

	var restored []string
	for path := range dirtyTrackedFiles(dir) {
		if before[path] {
			continue
		}
		if err := restoreTrackedFileFromHEAD(dir, path); err != nil {
			t.Fatalf("restore %s: %v", path, err)
		}
		restored = append(restored, path)
	}

	if len(restored) != 1 || restored[0] != "deploy/nats/nats.conf" {
		t.Errorf("restored = %v, want [deploy/nats/nats.conf] only", restored)
	}
	if got := readGuardFile(t, dir, "internal/work.go"); got != inFlight {
		t.Errorf("a pre-existing uncommitted edit was reverted — this would destroy another "+
			"agent's work:\n--- got ---\n%s\n--- want ---\n%s", got, inFlight)
	}
}

// TestRestoreImpureKCLWrites_EditAfterTheReadIsNeverOverwritten is the
// review's race: the guard read a named file's dirty bytes and kept a copy,
// and before it reset the file to HEAD someone edited it again. That newer
// edit is in neither the read nor the copy, so the reset must not happen.
func TestRestoreImpureKCLWrites_EditAfterTheReadIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/kcl/dev/main.k": "import file\n\n_w = file.write(\"deploy/nats/nats.conf\", _conf)\n",
		"deploy/nats/nats.conf": "accounts {}\n",
	})
	before := dirtyTrackedFiles(dir)
	writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { leaked: {} }\n")

	const newer = "accounts { hand-edited after the read: {} }\n"
	prev := kclResetHook
	kclResetHook = func(string) { writeGuardFile(t, dir, "deploy/nats/nats.conf", newer) }
	t.Cleanup(func() { kclResetHook = prev })

	rerendered := false
	restored := restoreImpureKCLWrites(dir, before, func() { rerendered = true })
	if len(restored) != 0 {
		t.Errorf("restored %v; the file changed again before the reset", restored)
	}
	if got := readGuardFile(t, dir, "deploy/nats/nats.conf"); got != newer {
		t.Errorf("the edit made after the read was overwritten:\n--- got ---\n%s\n--- want ---\n%s", got, newer)
	}
	if rerendered {
		t.Error("the proof rerendered with no candidate left to decide about")
	}
	if kept, _ := filepath.Glob(filepath.Join(dir, ".forge/kcl-reverted/deploy/nats/nats.conf.*")); len(kept) != 0 {
		t.Errorf("the older bytes are still kept aside although the file was left untouched: %v", kept)
	}
}
