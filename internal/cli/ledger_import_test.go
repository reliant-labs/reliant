package cli

// `forge ledger import` against a fixture git repository.
//
// The fixture plants the RETIRED in-checkout ledger and COMMITS it, because
// that is the only state the import reads: a rev, never the working tree.
// Each test then drives runLedgerImport the way the command does and asserts
// on the selected ledger's contents.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/release"
)

// importFixture is a project whose committed history holds a retired ledger,
// with a private machine ledger as the import target.
type importFixture struct {
	dir string
	rev string
}

// newImportFixture plants a retired ledger, commits it, and points the CLI
// at a private ledger home.
//
// The ledger files are COMMITTED and then DELETED from the working tree, so
// a test that passes could only have read the rev. A fixture that left them
// on disk would pass even if the reader silently fell back to the working
// tree, which is the one mistake this command must not make.
func newImportFixture(t *testing.T, plant func(dir string)) importFixture {
	t.Helper()
	dir := newImportProject(t, "fixture-project")
	plant(dir)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "ledger")
	// Prove the read is from the rev: the files are gone from disk.
	for _, rel := range []string{retiredPromotionsDirRel, retiredReleasesDirRel} {
		if err := os.RemoveAll(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("clear the working tree: %v", err)
		}
	}
	return importFixture{dir: dir, rev: "HEAD"}
}

// newImportProject is a named git project with a private ledger home.
//
// It writes a REAL forge.yaml, unlike the ledgerProjectName seam most ledger
// tests use, because the command resolves its own project directory through
// findProjectConfigFile — a seam that only names the project would leave
// projectDirForKCL pointing at the test binary's working directory.
func newImportProject(t *testing.T, name string) string {
	t.Helper()
	dir := newLedgerTestProject(t, name)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	gitInit(t, dir)
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// importCmd builds the command with its project directory pinned to dir, so
// the test does not chdir (which Go forbids in a parallel test and which
// would leak between tests sharing a process).
func importCmd(t *testing.T, dir string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	if err := cmdutil.SetProjectDir(dir); err != nil {
		t.Fatalf("pin the project dir: %v", err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })
	var out bytes.Buffer
	cmd := newLedgerImportCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	return cmd, &out
}

// runImport drives the command and returns its output.
func runImport(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd, out := importCmd(t, dir)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// retiredPromotion is a promotion as a git ledger holds it.
func importablePromotion(id, env, version string, at time.Time) release.Promotion {
	return release.Promotion{
		ID:         id,
		Env:        env,
		Release:    version,
		Kind:       release.KindPromote,
		Resolved:   map[string]string{"app": "sha256:" + strings.Repeat("a", 64)},
		PromotedAt: at,
	}
}

// retiredRelease is a release as a git ledger holds it.
func importableRelease(version string) release.Release {
	return release.Release{
		Version:   version,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Artifacts: map[string]release.Artifact{
			"app": {
				Kind:    release.KindOCI,
				Mode:    release.ModeShared,
				Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)},
			},
		},
	}
}

// plantRetiredLedger writes promotions and release files under the retired
// paths. stems lets a test name a release FILE differently from the version
// inside it, which is the v1_0_0.json case.
func plantRetiredLedger(t *testing.T, dir string, promotions []release.Promotion, releases map[string]release.Release) {
	t.Helper()
	byEnv := map[string][]release.Promotion{}
	for _, p := range promotions {
		byEnv[p.Env] = append(byEnv[p.Env], p)
	}
	for env, ps := range byEnv {
		path := filepath.Join(dir, retiredPromotionsDirRel, env+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		var lines strings.Builder
		for _, p := range ps {
			line, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			lines.Write(line)
			lines.WriteByte('\n')
		}
		if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for stem, r := range releases {
		path := filepath.Join(dir, retiredReleasesDirRel, stem+".json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// ─── The core contract: ids survive, so the refusal clears ───────────────────

// THE contract test. F3's unimported-checkout refusal matches a checkout's
// promotion against the ledger BY ID, so an import that minted fresh ids
// would leave every record looking absent and the refusal would never go
// quiet. This asserts both halves: the ids are in the store, and the refusal
// has stopped firing.
func TestLedgerImport_PreservesPromotionIDsAndClearsTheRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real git project for the ledger; runs in task test")
	}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	promotions := []release.Promotion{
		importablePromotion("20260901T120000Z-aaaa", "prod", "v1.0.0", base),
		importablePromotion("20260902T120000Z-bbbb", "prod", "v1.1.0", base.Add(24*time.Hour)),
	}
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir, promotions, map[string]release.Release{
			"v1.0.0": importableRelease("v1.0.0"),
			"v1.1.0": importableRelease("v1.1.0"),
		})
	})

	// Before the import, selection refuses — the state the import exists
	// to clear.
	l, err := selectLedger(context.Background(), fx.dir, "prod")
	if err != nil {
		t.Fatalf("select the ledger: %v", err)
	}
	if err := checkLedgerImported(context.Background(), fx.dir, "prod", l); err == nil {
		t.Fatal("expected the unimported-checkout refusal before the import")
	}

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	store := testStore(t, fx.dir)
	got, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d promotions, want 2", len(got))
	}
	for i, want := range promotions {
		if got[i].ID != want.ID {
			t.Errorf("promotion %d id = %q, want %q", i, got[i].ID, want.ID)
		}
		if !got[i].PromotedAt.Equal(want.PromotedAt) {
			t.Errorf("promotion %d promoted_at = %v, want %v", i, got[i].PromotedAt, want.PromotedAt)
		}
	}

	// The refusal is now quiet, with no flag and no state beyond the
	// ledger itself.
	if err := checkLedgerImported(context.Background(), fx.dir, "prod", l); err != nil {
		t.Errorf("the refusal must clear after an import, got: %v", err)
	}
}

// A release file's NAME is not its version. control-plane's own ledger holds
// v1_0_0.json carrying "v1.0.0", from the retired writer's lossy
// version→filename mapping. Keying off the filename would import a release
// called "v1_0_0" that no promotion references, leaving prod's first
// promotion pointing at a release the ledger does not contain.
func TestLedgerImport_KeysReleasesByContentsNotFilename(t *testing.T) {
	at := time.Date(2026, 6, 25, 14, 57, 15, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", at)},
			// The FILE is v1_0_0; the version inside is v1.0.0.
			map[string]release.Release{"v1_0_0": importableRelease("v1.0.0")},
		)
	})

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	store := testStore(t, fx.dir)
	if r, err := store.Release("v1.0.0"); err != nil || r == nil {
		t.Fatalf("release v1.0.0 = %v, %v — the version comes from the file's CONTENTS", r, err)
	}
	if r, _ := store.Release("v1_0_0"); r != nil {
		t.Error("v1_0_0 is a FILENAME, not a version; nothing may be recorded under it")
	}
}

// The order is the record: an append-only log's current binding is its last
// line. A source read in tree order (lexical, so v1.5.10 before v1.5.2) must
// still land in promoted_at order, or `forge env status prod` names a release
// prod stopped running months ago.
func TestLedgerImport_RecordsPromotionsInPromotedAtOrder(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Written to the file NEWEST FIRST, so an importer that preserved
	// file order would get the current binding wrong.
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir, []release.Promotion{
			importablePromotion("c", "prod", "v3.0.0", base.Add(2*time.Hour)),
			importablePromotion("a", "prod", "v1.0.0", base),
			importablePromotion("b", "prod", "v2.0.0", base.Add(time.Hour)),
		}, map[string]release.Release{
			"v1.0.0": importableRelease("v1.0.0"),
			"v2.0.0": importableRelease("v2.0.0"),
			"v3.0.0": importableRelease("v3.0.0"),
		})
	})

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	store := testStore(t, fx.dir)
	got, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	var order []string
	for _, p := range got {
		order = append(order, p.Release)
	}
	if want := "v1.0.0,v2.0.0,v3.0.0"; strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	current, ok, err := store.CurrentPromotion("prod")
	if err != nil || !ok {
		t.Fatalf("current promotion: %v, %v", ok, err)
	}
	if current.Release != "v3.0.0" {
		t.Errorf("current = %q, want v3.0.0 — the newest promotion must be the last line", current.Release)
	}
}

// --apply twice records once. A run interrupted half-way is re-run, not
// repaired, so the second run must be a visible no-op rather than a silent
// one: the plan reports what is already held.
func TestLedgerImport_AppliedTwiceCreatesOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real git project for the ledger; runs in task test")
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")},
		)
	})

	first, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if !strings.Contains(first, "Imported into") || !strings.Contains(first, "1 promotions, 1 releases") {
		t.Errorf("first import must report what it recorded, got:\n%s", first)
	}

	second, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !strings.Contains(second, "already held") {
		t.Errorf("the second import must SAY it is a no-op, got:\n%s", second)
	}
	if !strings.Contains(second, "0 promotions, 0 releases") {
		t.Errorf("the second import must record nothing, got:\n%s", second)
	}

	store := testStore(t, fx.dir)
	promotions, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	if len(promotions) != 1 {
		t.Fatalf("got %d promotions after two applies, want 1", len(promotions))
	}
	releases, err := store.Releases()
	if err != nil {
		t.Fatalf("read releases: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("got %d releases after two applies, want 1", len(releases))
	}
}

// The dry run is the verification step (§11.1 step 2): it prints the counts
// an operator checks against what they know the repository holds, and it
// must change nothing.
func TestLedgerImport_DryRunCountsAndWritesNothing(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir, []release.Promotion{
			importablePromotion("a", "prod", "v1.0.0", base),
			importablePromotion("b", "prod", "v2.0.0", base.Add(time.Hour)),
			importablePromotion("c", "staging", "v1.0.0", base),
		}, map[string]release.Release{
			"v1.0.0": importableRelease("v1.0.0"),
			"v2.0.0": importableRelease("v2.0.0"),
		})
	})

	out, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "2 release(s), 3 promotion(s) across 2 environment(s)") {
		t.Errorf("the dry run must print the counts, got:\n%s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("the dry run must say it is one, got:\n%s", out)
	}

	store := testStore(t, fx.dir)
	if promotions, _ := store.Promotions("prod"); len(promotions) != 0 {
		t.Errorf("a dry run wrote %d promotions; it must write none", len(promotions))
	}
	if releases, _ := store.Releases(); len(releases) != 0 {
		t.Errorf("a dry run wrote %d releases; it must write none", len(releases))
	}
}

// §11.1 step 6. An env that moved since the git ledger was written cannot
// have imported history interleaved with it: the current binding is the last
// line, so the result would be whichever release sorted last. The remedy is
// to import first, and the refusal says so.
func TestLedgerImport_RefusesAnEnvThatAlreadyHoldsItsOwnPromotions(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("from-git", "prod", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")},
		)
	})

	// A promotion made HERE, which the git ledger does not contain.
	store := testStore(t, fx.dir)
	if _, err := store.CutRelease(importableRelease("v9.0.0")); err != nil {
		t.Fatalf("seed a release: %v", err)
	}
	if _, err := store.AppendPromotion(
		importablePromotion("", "prod", "v9.0.0", time.Time{}),
		func([]release.Promotion, release.Promotion) (*release.Promotion, error) { return nil, nil },
	); err != nil {
		t.Fatalf("seed a local promotion: %v", err)
	}

	out, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply")
	if err == nil {
		t.Fatal("expected a refusal: the env holds promotions this import did not bring")
	}
	if !strings.Contains(err.Error(), "already hold promotions") {
		t.Errorf("error = %v, want it to name the ordering problem", err)
	}
	if !strings.Contains(out, "CONFLICT prod") {
		t.Errorf("the plan must name the conflicting env, got:\n%s", out)
	}
	// Nothing was recorded: a refusal is not a partial import.
	promotions, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	if len(promotions) != 1 {
		t.Errorf("got %d promotions after a refused import, want the 1 that was already there", len(promotions))
	}
}

// Each imported promotion records WHERE its line came from — the file and
// the exact blob — so the history stays auditable after §11.1 step 9 deletes
// the files from the repository.
func TestLedgerImport_RecordsTheGitBlobAsProvenance(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")},
		)
	})

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	from, err := testStore(t, fx.dir).ImportedFrom("prod")
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	source := from["id-1"]
	if !strings.HasPrefix(source, "git:"+retiredPromotionsDirRel+"/prod.jsonl@") {
		t.Errorf("imported_from = %q, want git:<path>@<blob sha>", source)
	}
	// The blob half must be a real object sha, not a placeholder: that is
	// what makes the import re-derivable from the repository.
	blob := source[strings.LastIndex(source, "@")+1:]
	if len(blob) != 40 {
		t.Errorf("blob = %q, want a 40-hex object id", blob)
	}
}

// Reading the WORKING TREE instead of the rev is the one mistake this
// command must not make. The fixture deletes the files after committing, so
// a reader that fell back to disk would find nothing — and this asserts the
// opposite of nothing.
func TestLedgerImport_ReadsTheRevNotTheWorkingTree(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")},
		)
	})
	// Belt and braces: the working tree is empty of the ledger.
	if _, err := os.Stat(filepath.Join(fx.dir, retiredPromotionsDirRel)); err == nil {
		t.Fatal("the fixture must leave no ledger in the working tree")
	}

	out, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "1 promotion(s)") {
		t.Errorf("the import must read the rev, not the working tree, got:\n%s", out)
	}
}

// A malformed line is an ERROR here, unlike in the unimported-checkout
// detector where it is skipped. The detector only has to notice that history
// exists; the import is what makes the history authoritative, so a line it
// skipped would be lost for good once the files are deleted.
func TestLedgerImport_RefusesAMalformedPromotionLine(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		path := filepath.Join(dir, retiredPromotionsDirRel, "prod.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	})

	_, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev)
	if err == nil {
		t.Fatal("expected a refusal: a line the import cannot read would be lost")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %v, want it to name the line", err)
	}
}

// A promotion with no id cannot be imported: the id is how a re-run
// recognises what it already recorded, so minting one would make the import
// duplicate on every run and would never clear F3's refusal.
func TestLedgerImport_RefusesAPromotionWithNoID(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t,
			dir,
			[]release.Promotion{importablePromotion("", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")},
		)
	})

	_, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev)
	if err == nil {
		t.Fatal("expected a refusal: an import preserves ids and so needs one")
	}
	if !strings.Contains(err.Error(), "no id") {
		t.Errorf("error = %v, want it to name the missing id", err)
	}
}

// ─── Source selection ───────────────────────────────────────────────────────

func TestLedgerImport_RefusesNoSourceAndBothSources(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})

	if _, err := runImport(t, fx.dir); err == nil {
		t.Error("expected a refusal with no source named")
	}
	if _, err := runImport(t, fx.dir, "--from-git", "--from-file-ledger", t.TempDir()); err == nil {
		t.Error("expected a refusal with two sources named")
	}
}

// §12.4: an env whose KCL newly declares a control plane keeps a non-empty
// machine ledger, and its lines ARE the payload — the two stores serialize
// the same canonical JSON, so nothing is translated.
func TestLedgerImport_FromFileLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git projects for the ledger; runs in task test")
	}
	// A source ledger, built by the store itself so the files are exactly
	// what a real machine ledger holds.
	sourceProject := newImportProject(t, "source-project")
	source := testStore(t, sourceProject)
	if _, err := source.CutRelease(importableRelease("v1.0.0")); err != nil {
		t.Fatalf("seed a release: %v", err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := source.ImportPromotionFrom(importablePromotion("id-1", "prod", "v1.0.0", at), "git:x@y"); err != nil {
		t.Fatalf("seed a promotion: %v", err)
	}

	// The target is a different project directory, which is how a move
	// between ledgers actually looks.
	target := newImportProject(t, "target-project")

	if _, err := runImport(t, target, "--from-file-ledger", source.Dir(), "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	store := testStore(t, target)
	got, err := store.Promotions("prod")
	if err != nil {
		t.Fatalf("read promotions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "id-1" {
		t.Fatalf("promotions = %+v, want one with id id-1", got)
	}
	if r, err := store.Release("v1.0.0"); err != nil || r == nil {
		t.Fatalf("release v1.0.0 = %v, %v", r, err)
	}
}

// Pointing --from-file-ledger at a CHECKOUT is the likely mistake, and the
// right flag exists, so the error names it rather than reporting "no ledger".
func TestLedgerImport_FromFileLedgerPointedAtACheckoutNamesTheOtherFlag(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})
	// Put the retired ledger BACK in the working tree, which is what a
	// user pointing at their checkout would have.
	plantRetiredLedger(t, fx.dir,
		[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})

	_, err := runImport(t, fx.dir, "--from-file-ledger", fx.dir)
	if err == nil {
		t.Fatal("expected a refusal: that is a checkout, not a ledger directory")
	}
	if !strings.Contains(err.Error(), "--from-git") {
		t.Errorf("error = %v, want it to name --from-git", err)
	}
}

// An unresolvable rev fails naming the rev the user typed, with the fetch
// remedy — not later as an opaque "path does not exist in tree".
func TestLedgerImport_UnknownRevNamesTheRev(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})

	_, err := runImport(t, fx.dir, "--from-git", "--rev", "origin/nope")
	if err == nil {
		t.Fatal("expected a refusal for an unresolvable rev")
	}
	if !strings.Contains(err.Error(), "origin/nope") {
		t.Errorf("error = %v, want it to name the rev", err)
	}
	if !strings.Contains(err.Error(), "git fetch") {
		t.Errorf("error = %v, want the fetch remedy", err)
	}
}

// An import brings history for envs whose KCL is gone (control-plane's
// retired staging and preprod, O-9). They get an env record so a reader sees
// them without rendering anything.
func TestLedgerImport_RecordsAnEnvRecordForARetiredEnv(t *testing.T) {
	at := time.Date(2026, 7, 1, 0, 25, 15, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-1", "staging", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	rec, err := testStore(t, fx.dir).Env("staging")
	if err != nil {
		t.Fatalf("read the env record: %v", err)
	}
	if rec == nil {
		t.Fatal("an imported env must get a record, so a reader sees it with no render")
	}
	if rec.Kind != release.EnvSelfManaged {
		t.Errorf("kind = %q, want %q", rec.Kind, release.EnvSelfManaged)
	}
}

// A declaration recorded by `forge env build` knows the kind AND the shape.
// An import must not overwrite it with less.
func TestLedgerImport_LeavesAnExistingEnvRecordAlone(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-1", "prod", "v1.0.0", at)},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})
	store := testStore(t, fx.dir)
	if err := store.PutEnv(release.EnvRecord{Name: "prod", Kind: release.EnvPersistent}); err != nil {
		t.Fatalf("seed an env record: %v", err)
	}

	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	rec, err := store.Env("prod")
	if err != nil || rec == nil {
		t.Fatalf("read the env record: %v, %v", rec, err)
	}
	if rec.Kind != release.EnvPersistent {
		t.Errorf("kind = %q, want the recorded %q — an import must not downgrade a declaration",
			rec.Kind, release.EnvPersistent)
	}
}

// An empty source says so and exits 0. There is nothing wrong with a project
// that never used the retired ledger (hounders, §11.2), and making the
// absence an error would break the forge pin bump for every such project.
func TestLedgerImport_EmptySourceIsNotAnError(t *testing.T) {
	dir := newImportProject(t, "no-ledger-project")

	out, err := runImport(t, dir, "--from-git", "--rev", "HEAD")
	if err != nil {
		t.Fatalf("a project with no retired ledger must not error: %v", err)
	}
	if !strings.Contains(out, "Nothing to import") {
		t.Errorf("output = %q, want it to say there is nothing to import", out)
	}
}

// ─── The hosted target ──────────────────────────────────────────────────────

// fakeImporter records what the import handed a ledger, without a server.
//
// It stands in for the hosted backend one level ABOVE the wire: F4's own
// tests pin the JSON ImportLedger sends, so what is left to prove here is
// the ORCHESTRATION — that the assembled *ledgerImport reaching a backend
// carries every release, an env row per environment, and the promotions in
// promoted_at order, in ONE call.
type fakeImporter struct {
	held     ledgerHeld
	calls    []*ledgerImport
	location string
}

func (f *fakeImporter) Held(context.Context, []string) (ledgerHeld, error) { return f.held, nil }

func (f *fakeImporter) Import(_ context.Context, in *ledgerImport) (ledgerImportResult, error) {
	f.calls = append(f.calls, in)
	return ledgerImportResult{Counts: map[string]int{
		"releases":     len(in.releases),
		"environments": len(in.environments),
		"promotions":   len(in.promotions),
	}}, nil
}

// ONE call, carrying the whole payload with order intact. A client that split
// the import per record would lose the server's single transaction, and one
// that dropped the promoted_at order would record the wrong current binding.
func TestLedgerImport_HostedTargetReceivesOneOrderedPayload(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeImporter{
		held:     ledgerHeld{ReleaseVersions: map[string]struct{}{}, PromotionIDs: map[string]map[string]struct{}{}},
		location: "https://api.example.com",
	}
	plan := ledgerImportPlan{Targets: []*ledgerImportTarget{{
		Location: fake.location,
		Importer: fake,
		Envs:     []string{"prod", "staging"},
		Releases: []sourceRelease{
			{Release: importableRelease("v1.0.0"), From: "git:a@1"},
			{Release: importableRelease("v2.0.0"), From: "git:b@2"},
		},
		Promotions: map[string][]sourcePromotion{
			"prod": {
				{Promotion: importablePromotion("a", "prod", "v1.0.0", base), From: "git:p@3"},
				{Promotion: importablePromotion("b", "prod", "v2.0.0", base.Add(time.Hour)), From: "git:p@3"},
			},
			"staging": {
				{Promotion: importablePromotion("c", "staging", "v1.0.0", base), From: "git:s@4"},
			},
		},
	}}}

	var out bytes.Buffer
	if err := applyLedgerImport(context.Background(), &out, plan, "control-plane"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("got %d Import calls, want exactly 1 — the hosted import is one transaction", len(fake.calls))
	}
	in := fake.calls[0]
	if in.project != "control-plane" {
		t.Errorf("project = %q, want control-plane — a hosted env's identity is (org, project, name)", in.project)
	}
	if len(in.releases) != 2 {
		t.Errorf("carried %d releases, want 2", len(in.releases))
	}
	// Every item carries its own imported_from: that is the per-record
	// dedupe key the whole import's idempotency rests on.
	for _, r := range in.releases {
		if r.ImportedFrom == "" {
			t.Errorf("release %s has no imported_from, so a re-run would duplicate it", r.Release.Version)
		}
	}
	// An env row per environment, so a retired env's promotions do not
	// reference an env the control plane has never heard of.
	var envNames []string
	for _, e := range in.environments {
		envNames = append(envNames, e.Name)
		if e.Kind != release.EnvSelfManaged {
			t.Errorf("env %s imported as kind %q, want %q", e.Name, e.Kind, release.EnvSelfManaged)
		}
		if e.ImportedFrom == "" {
			t.Errorf("env %s has no imported_from", e.Name)
		}
	}
	if strings.Join(envNames, ",") != "prod,staging" {
		t.Errorf("env rows = %v, want prod,staging", envNames)
	}
	// The order is the record. Promotions arrive grouped by env, each
	// group oldest first.
	var ids []string
	for _, p := range in.promotions {
		ids = append(ids, p.Promotion.ID)
		if p.ImportedFrom == "" {
			t.Errorf("promotion %s has no imported_from", p.Promotion.ID)
		}
		// promoted_at is taken from the RECORD, not the clock: the
		// whole point of an import is that these already happened.
		if !p.PromotedAt.Equal(p.Promotion.PromotedAt) {
			t.Errorf("promotion %s sent promoted_at %v, want the record's %v",
				p.Promotion.ID, p.PromotedAt, p.Promotion.PromotedAt)
		}
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("promotion order = %v, want a,b,c (per env, oldest first)", ids)
	}
	if !strings.Contains(out.String(), fake.location) {
		t.Errorf("output must name the target, got:\n%s", out.String())
	}
}

// A hosted import can partially commit: the server applies what it can name
// unambiguously and reports the rest as conflicts, with NO error. That is a
// normal outcome, and it must be visible — the alternative is an operator
// believing the whole history landed.
func TestLedgerImport_PrintsTheServersDeclinedItems(t *testing.T) {
	fake := &fakeImporterWithConflicts{conflicts: []string{"env prod already holds a non-imported promotion"}}
	plan := ledgerImportPlan{Targets: []*ledgerImportTarget{{
		Location:   "https://api.example.com",
		Importer:   fake,
		Envs:       []string{"prod"},
		Promotions: map[string][]sourcePromotion{},
	}}}

	var out bytes.Buffer
	if err := applyLedgerImport(context.Background(), &out, plan, "control-plane"); err != nil {
		t.Fatalf("a partial import is not an error: %v", err)
	}
	if !strings.Contains(out.String(), "declined: env prod already holds") {
		t.Errorf("the server's conflicts must be printed, got:\n%s", out.String())
	}
}

// fakeImporterWithConflicts reports a partial commit.
type fakeImporterWithConflicts struct{ conflicts []string }

func (f *fakeImporterWithConflicts) Held(context.Context, []string) (ledgerHeld, error) {
	return ledgerHeld{ReleaseVersions: map[string]struct{}{}, PromotionIDs: map[string]map[string]struct{}{}}, nil
}

func (f *fakeImporterWithConflicts) Import(context.Context, *ledgerImport) (ledgerImportResult, error) {
	return ledgerImportResult{Counts: map[string]int{"promotions": 0}, Conflicts: f.conflicts}, nil
}

// A ledger that cannot be imported into refuses by name rather than failing
// obscurely at write time.
func TestImporterFor_RefusesALedgerWithNoImport(t *testing.T) {
	_, err := importerFor(envLedger{Bindings: noImportStore{}})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "cannot accept an import") {
		t.Errorf("error = %v, want it to name the missing capability", err)
	}
}

// noImportStore is a bindingStore with no import capability.
type noImportStore struct{}

func (noImportStore) Current(context.Context, string) (release.Promotion, bool, error) {
	return release.Promotion{}, false, nil
}

func (noImportStore) Append(context.Context, release.Promotion, appendGuard) (release.Promotion, error) {
	return release.Promotion{}, fmt.Errorf("not implemented")
}

func (noImportStore) Location() string { return "nowhere" }
