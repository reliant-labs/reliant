package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

func excludeTestProject(t *testing.T, forgeYAML string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["forge.yaml"] = forgeYAML
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checksums.TakeExcludedSkips()
	t.Cleanup(func() { checksums.TakeExcludedSkips() })
	return root
}

func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOrEmpty(root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

// The guarantee behind scaffold.exclude: whatever a locked run writes under an
// excluded glob — through a chokepoint or around one — is put back before the
// lock is released, and each path is reported once as a skipped write.
func TestExcludeGuard_PutsBackEveryWriteUnderAnExcludedGlob(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/, .github/workflows/deploy.yml]\n"+
		"generate:\n  hooks:\n    - name: own\n      command: [true]\n      writes: [frontends/web/src/own]\n",
		map[string]string{
			"frontends/web/src/app.tsx":    "mine\n",
			"frontends/web/src/gone.tsx":   "keep me\n",
			".github/workflows/deploy.yml": "# my deploy\n",
			"frontends/admin/src/app.tsx":  "admin\n",
		})

	g := armExcludeGuard(root)
	if g == nil {
		t.Fatal("no guard armed for a project with scaffold.exclude")
	}
	// The run: rewrite, delete and create under the globs, plus writes that
	// are not forge's to undo (outside the globs, a hook's declared output).
	writeRel(t, root, "frontends/web/src/app.tsx", "forge's\n")
	if err := os.Remove(filepath.Join(root, "frontends/web/src/gone.tsx")); err != nil {
		t.Fatal(err)
	}
	writeRel(t, root, "frontends/web/src/hooks/x-hooks.ts", "generated\n")
	writeRel(t, root, ".github/workflows/deploy.yml", "# a deploy workflow\n")
	writeRel(t, root, "frontends/admin/src/app.tsx", "admin v2\n")
	writeRel(t, root, "frontends/web/src/own/out.ts", "hook output\n")

	var out strings.Builder
	releaseExcludeGuard(g, &out)

	for rel, want := range map[string]string{
		"frontends/web/src/app.tsx":          "mine\n",
		"frontends/web/src/gone.tsx":         "keep me\n",
		"frontends/web/src/hooks/x-hooks.ts": "<absent>",
		".github/workflows/deploy.yml":       "# my deploy\n",
		"frontends/admin/src/app.tsx":        "admin v2\n",
		"frontends/web/src/own/out.ts":       "hook output\n",
	} {
		if got := readOrEmpty(root, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "frontends/web/src/hooks")); !os.IsNotExist(err) {
		t.Errorf("the emptied directory the run created was left behind")
	}
	report := out.String()
	for _, want := range []string{
		"frontends/web/src/app.tsx skipped (scaffold.exclude: frontends/web/**)",
		"frontends/web/src/hooks/x-hooks.ts skipped (scaffold.exclude: frontends/web/**)",
		".github/workflows/deploy.yml skipped (scaffold.exclude: .github/workflows/deploy.yml)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "admin") || strings.Contains(report, "own/out.ts") {
		t.Errorf("report names a write that was not under an excluded glob:\n%s", report)
	}
}

func TestExcludeGuard_NoGlobsNoGuard(t *testing.T) {
	root := excludeTestProject(t, "name: x\n", map[string]string{})
	if g := armExcludeGuard(root); g != nil {
		t.Fatal("a guard was armed with no scaffold.exclude")
	}
}

func TestPrintExcludedSkips_CapsPerGlobUnlessVerbose(t *testing.T) {
	var skips []checksums.ExcludedSkip
	for i := 0; i < excludeReportPerGlob+3; i++ {
		skips = append(skips, checksums.ExcludedSkip{Path: "web/f" + string(rune('a'+i)), Glob: "web/**"})
	}
	var quiet, verbose strings.Builder
	printExcludedSkips(&quiet, skips, false)
	printExcludedSkips(&verbose, skips, true)
	if n := strings.Count(quiet.String(), "\n"); n != excludeReportPerGlob+1 {
		t.Errorf("quiet report has %d lines, want %d:\n%s", n, excludeReportPerGlob+1, quiet.String())
	}
	if !strings.Contains(quiet.String(), "and 3 more skipped (scaffold.exclude: web/**)") {
		t.Errorf("quiet report does not count the rest:\n%s", quiet.String())
	}
	if n := strings.Count(verbose.String(), "\n"); n != len(skips) {
		t.Errorf("verbose report has %d lines, want %d", n, len(skips))
	}
}

func TestExcludedTSStubs(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/src/gen/]\n", map[string]string{})
	_, err := excludedTSStubs(root, "frontends/web")
	var conflict *checksums.ExcludeConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Dependent, "frontends/web/src/hooks") {
		t.Fatalf("stubs excluded without their hooks: err = %v, want a conflict naming the hooks", err)
	}

	root = excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/]\n", map[string]string{})
	if skip, err := excludedTSStubs(root, "frontends/web"); !skip || err != nil {
		t.Fatalf("whole frontend excluded: skip=%v err=%v", skip, err)
	}
	if skip, err := excludedTSStubs(root, "frontends/admin"); skip || err != nil {
		t.Fatalf("another frontend: skip=%v err=%v", skip, err)
	}
}

func TestRefuseExcludedGoStubs(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [\"gen/**\"]\n", map[string]string{})
	err := refuseExcludedGoStubs(root)
	var conflict *checksums.ExcludeConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "features: {codegen: false}") {
		t.Fatalf("err = %v, want a conflict that names the codegen switch", err)
	}
	root = excludeTestProject(t, "name: x\nscaffold:\n  exclude: [docs/]\n", map[string]string{})
	if err := refuseExcludedGoStubs(root); err != nil {
		t.Fatalf("unrelated glob: %v", err)
	}
}

// prune --orphans deletes only what still verifies as forge's render: an
// edited orphan, an excluded one and a disowned one all stay.
func TestPruneOrphans_NeverTouchesAnEditedFile(t *testing.T) {
	stamp := func(rel, body string) string {
		b, ok := checksums.Stamp(rel, []byte(body))
		if !ok {
			t.Fatalf("%s is not stampable", rel)
		}
		return string(b)
	}
	pristine := stamp("internal/obs/dash_gen.go", "package obs\n")
	edited := strings.Replace(stamp("internal/obs/panel_gen.go", "package obs\n"), "package obs", "package obs // mine", 1)
	excluded := stamp("docs/gen_gen.go", "package docs\n")
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [docs/]\n", map[string]string{
		"internal/obs/dash_gen.go":  pristine,
		"internal/obs/panel_gen.go": edited,
		"docs/gen_gen.go":           excluded,
	})
	rec := orphanRecord{ForgeYAML: forgeYAMLHash(root), FeaturesOff: []string{"observability"}, Files: []orphanFile{
		{Path: "internal/obs/dash_gen.go"}, {Path: "internal/obs/panel_gen.go"}, {Path: "docs/gen_gen.go"}, {Path: "internal/obs/gone_gen.go"},
	}}
	data, _ := json.Marshal(rec)
	writeRel(t, root, orphansRel, string(data))

	res, err := pruneOrphans(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Removed, ",") != "internal/obs/dash_gen.go" || readOrEmpty(root, "internal/obs/dash_gen.go") != pristine {
		t.Fatalf("dry run: removed=%v (and must delete nothing)", res.Removed)
	}

	res, err = pruneOrphans(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Removed, ",") != "internal/obs/dash_gen.go" {
		t.Errorf("removed = %v, want only the pristine orphan", res.Removed)
	}
	if strings.Join(res.Kept, ",") != "internal/obs/panel_gen.go,docs/gen_gen.go" {
		t.Errorf("kept = %v", res.Kept)
	}
	if strings.Join(res.Gone, ",") != "internal/obs/gone_gen.go" {
		t.Errorf("gone = %v", res.Gone)
	}
	if readOrEmpty(root, "internal/obs/dash_gen.go") != "<absent>" {
		t.Error("the pristine orphan is still there")
	}
	if readOrEmpty(root, "internal/obs/panel_gen.go") != edited || readOrEmpty(root, "docs/gen_gen.go") != excluded {
		t.Error("prune touched an edited or excluded file")
	}
}

func TestPruneOrphans_RefusesARecordFromAnotherForgeYAML(t *testing.T) {
	root := excludeTestProject(t, "name: x\n", map[string]string{})
	data, _ := json.Marshal(orphanRecord{ForgeYAML: "stale", Files: []orphanFile{{Path: "a_gen.go"}}})
	writeRel(t, root, orphansRel, string(data))
	if _, err := pruneOrphans(root, false); err == nil || !strings.Contains(err.Error(), "forge.yaml changed") {
		t.Fatalf("err = %v, want a refusal naming the forge.yaml change", err)
	}
}

// A feature pinned off leaves its old outputs behind: a full generate lists
// them and records the list prune reads; with nothing pinned off the record
// goes away.
func TestRecordOrphans_ListsWhatAFeaturePinnedOffLeftBehind(t *testing.T) {
	stamped, ok := checksums.Stamp("internal/obsdash/dash_gen.go", []byte("package obsdash\n"))
	if !ok {
		t.Fatal("not stampable")
	}
	root := excludeTestProject(t, "name: x\n", map[string]string{"internal/obsdash/dash_gen.go": string(stamped)})
	off, _ := config.FeaturesConfig{}.Pin(config.FeatureObservability, false)
	ctx := &pipelineContext{ProjectDir: root, AbsPath: root, Cfg: &config.ProjectConfig{Features: off}}

	var out strings.Builder
	recordOrphans(&out, ctx)
	for _, want := range []string{"internal/obsdash/dash_gen.go", "features.observability is pinned off", "project prune --orphans"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	var rec orphanRecord
	data, err := os.ReadFile(filepath.Join(root, orphansRel))
	if err != nil || json.Unmarshal(data, &rec) != nil {
		t.Fatalf("no orphan record: %v", err)
	}
	if rec.ForgeYAML != forgeYAMLHash(root) || len(rec.Files) != 1 || rec.Files[0].Edited {
		t.Fatalf("record = %+v", rec)
	}

	ctx.Cfg = &config.ProjectConfig{}
	out.Reset()
	recordOrphans(&out, ctx)
	if out.Len() != 0 || readOrEmpty(root, orphansRel) != "<absent>" {
		t.Fatalf("nothing pinned off: report %q, record %q", out.String(), readOrEmpty(root, orphansRel))
	}
}

// The guard cannot tell forge's writes from anyone else's, so it never
// destroys what it undoes: a file a person or another agent edits or creates
// under an excluded glob while a run holds the lock is kept, byte for byte,
// under .forge/exclude-guard/, and the run says where.
func TestExcludeGuard_KeepsWhatItUndoes(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/]\n",
		map[string]string{"frontends/web/src/app.tsx": "mine\n"})
	g := armExcludeGuard(root)
	if g == nil {
		t.Fatal("no guard armed for a project with scaffold.exclude")
	}
	// Not forge: an editor save and a new file, mid-run.
	writeRel(t, root, "frontends/web/src/app.tsx", "my edit, saved mid-run\n")
	writeRel(t, root, "frontends/web/src/new-page.tsx", "a page I just wrote\n")

	var out strings.Builder
	releaseExcludeGuard(g, &out)

	report := out.String()
	kept := map[string]string{}
	for _, line := range strings.Split(report, "\n") {
		if rel, backup, ok := strings.Cut(strings.TrimSpace(line), " ← "); ok {
			kept[rel] = backup
		}
	}
	for rel, want := range map[string]string{
		"frontends/web/src/app.tsx":      "my edit, saved mid-run\n",
		"frontends/web/src/new-page.tsx": "a page I just wrote\n",
	} {
		backup, ok := kept[rel]
		if !ok {
			t.Errorf("report does not say where %s was kept:\n%s", rel, report)
			continue
		}
		if !strings.HasPrefix(backup, ".forge/exclude-guard/") {
			t.Errorf("%s kept at %s, want under .forge/exclude-guard/", rel, backup)
		}
		if got := readOrEmpty(root, backup); got != want {
			t.Errorf("%s's backup = %q, want what was found, %q", rel, got, want)
		}
	}
	if strings.Count(report, " ← ") != 2 {
		t.Errorf("each kept path is named once:\n%s", report)
	}
}

// A directory the guard never descends (here a frontend's node_modules under
// an excluded frontend) is neither captured nor undone: arm and settle share
// one walk, so whatever happens there during the run is left exactly as it
// is, never removed as if it were new.
func TestExcludeGuard_LeavesUnguardedDirsAlone(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/]\n",
		map[string]string{
			"frontends/web/src/app.tsx":                 "mine\n",
			"frontends/web/node_modules/pkg/index.js":   "v1\n",
			"frontends/web/node_modules/pkg/removed.js": "gone soon\n",
		})
	g := armExcludeGuard(root)
	if g == nil {
		t.Fatal("no guard armed for a project with scaffold.exclude")
	}
	writeRel(t, root, "frontends/web/node_modules/pkg/index.js", "v2\n")
	writeRel(t, root, "frontends/web/node_modules/pkg/added.js", "new\n")
	if err := os.Remove(filepath.Join(root, "frontends/web/node_modules/pkg/removed.js")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	releaseExcludeGuard(g, &out)
	for rel, want := range map[string]string{
		"frontends/web/node_modules/pkg/index.js":   "v2\n",
		"frontends/web/node_modules/pkg/added.js":   "new\n",
		"frontends/web/node_modules/pkg/removed.js": "<absent>",
		"frontends/web/src/app.tsx":                 "mine\n",
	} {
		if got := readOrEmpty(root, rel); got != want {
			t.Errorf("%s = %q, want %q (left as the run left it)", rel, got, want)
		}
	}
	if strings.Contains(out.String(), "node_modules") {
		t.Errorf("the report names a path the guard does not guard:\n%s", out.String())
	}
}

// Nothing the guard does can lose a write it races with: it moves what it
// finds (one rename, so the bytes are always in one place) and writes the
// pre-run version back only into a free path. A file someone writes at the
// path after the move is left exactly as they wrote it, and reported.
func TestExcludeGuard_NeverOverwritesAConcurrentWrite(t *testing.T) {
	root := excludeTestProject(t, "name: x\nscaffold:\n  exclude: [frontends/web/]\n",
		map[string]string{"frontends/web/src/app.tsx": "mine\n"})
	g := armExcludeGuard(root)
	if g == nil {
		t.Fatal("no guard armed for a project with scaffold.exclude")
	}
	full := filepath.Join(root, "frontends/web/src/app.tsx")
	writeRel(t, root, "frontends/web/src/app.tsx", "changed during the run\n")
	if !g.keep("frontends/web/src/app.tsx", full) {
		t.Fatal("keep refused a changed file")
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("keep left the file in place (a copy, not a move): %v", err)
	}
	backup := g.kept["frontends/web/src/app.tsx"]
	if got := readOrEmpty(root, backup); got != "changed during the run\n" {
		t.Fatalf("backup = %q, want what was found", got)
	}
	// Someone writes the path again between the move and the put-back.
	writeRel(t, root, "frontends/web/src/app.tsx", "a save after the move\n")
	if g.restore("frontends/web/src/app.tsx", full, g.pre["frontends/web/src/app.tsx"]) {
		t.Fatal("restore overwrote a file written after the move")
	}
	if got := readOrEmpty(root, "frontends/web/src/app.tsx"); got != "a save after the move\n" {
		t.Fatalf("the concurrent write was lost: %q", got)
	}
	// Into a free path, the pre-run bytes go back.
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if !g.restore("frontends/web/src/app.tsx", full, g.pre["frontends/web/src/app.tsx"]) || readOrEmpty(root, "frontends/web/src/app.tsx") != "mine\n" {
		t.Fatal("restore into a free path did not put the pre-run bytes back")
	}
}
