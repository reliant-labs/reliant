package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// The scaffold-once advisory lane. The bug it exists to prevent shipped in
// a real app: a frontend src/lib/query-client.ts kept a blanket `retry: 1`
// that retried 4xx long after the template replaced it with an isRetryable
// predicate, and nothing in forge ever mentioned it. These tests pin the
// four things that has to be true for the report to be worth reading:
// silence when a file matches, a finding when it doesn't, a finding that
// does NOT call a customized file merely outdated, and adoption that only
// ever happens when a path was named.

const advisoryQueryClient = "src/lib/query-client.ts"

// advisoryTestProject scaffolds one Next.js frontend and returns the
// project dir plus a config that describes it — including the `output:`
// the scaffolders write into forge.yaml for it.
func advisoryTestProject(t *testing.T) (string, *config.ProjectConfig) {
	t.Helper()
	dir := t.TempDir()
	if err := GenerateFrontendFiles(dir, "github.com/example/demo", "demo", "web", 8080, ""); err != nil {
		t.Fatalf("GenerateFrontendFiles: %v", err)
	}
	cfg := &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs", Output: config.FrontendOutputScaffoldDefault}.WithDir(filepath.Join("frontends", "web")),
		},
	}
	return dir, cfg
}

// TestAdvisories_LegacyStandaloneEntrySaysNothing pins what an EMPTY
// `output:` means to the advisory lane: the standalone shape. Every
// frontend scaffolded before the static default has exactly that — no
// `output:` key, a standalone next.config.ts and Dockerfile. Reading empty as
// the current default would report both files as diverged on every such
// project, and `upgrade --force` would convert its build to a static export
// nobody asked for.
func TestAdvisories_LegacyStandaloneEntrySaysNothing(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(dir, "github.com/example/demo", "demo", "web", 8080, "",
		FrontendGenOptions{Output: config.FrontendOutputStandalone}); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}
	cfg := &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir(filepath.Join("frontends", "web")),
		},
	}
	results := inspect(t, dir, cfg, ForceNone(), true)
	for _, rel := range []string{"next.config.ts", "Dockerfile"} {
		r, ok := results[advisoryPath(cfg, rel)]
		if !ok {
			t.Errorf("%s has no advisory row", rel)
			continue
		}
		if r.Behind() {
			t.Errorf("legacy (no output:) frontend reported %s as %s (+%d/-%d)\n%s", rel, r.Status, r.Missing, r.Local, r.Diff)
		}
	}
}

// advisoryRows renders the advisory set for a project, failing the test on
// any render error.
func advisoryRows(t *testing.T, dir string, cfg *config.ProjectConfig) []AdvisoryFile {
	t.Helper()
	rows, err := AdvisoryFilesFor(dir, cfg)
	if err != nil {
		t.Fatalf("AdvisoryFilesFor: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected advisory rows for a project with a frontend")
	}
	return rows
}

// inspect runs the advisory pass and indexes the results by path.
func inspect(t *testing.T, dir string, cfg *config.ProjectConfig, force ForceSelection, checkOnly bool) map[string]AdvisoryResult {
	t.Helper()
	cs, err := LoadChecksums(dir)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	results, err := InspectAdvisories(dir, cs, advisoryRows(t, dir, cfg), force, checkOnly)
	if err != nil {
		t.Fatalf("InspectAdvisories: %v", err)
	}
	byPath := map[string]AdvisoryResult{}
	for _, r := range results {
		byPath[r.Path] = r
	}
	return byPath
}

func advisoryPath(cfg *config.ProjectConfig, rel string) string {
	return filepath.Join(cfg.Frontends[0].DeclaredDir(), filepath.FromSlash(rel))
}

// TestAdvisories_FreshScaffoldSaysNothing is the false-positive gate for
// the frontend rows: a tree the scaffolder just wrote must produce an
// entirely silent report, which also pins that the payload this lane
// renders from reproduces the scaffold's bytes. A report that fires on a
// minutes-old project is a report nobody reads.
//
// The fixture scaffolds only the frontend, so the assertion is scoped to
// it; the whole-project version of this gate (which covers the .github
// starters) runs against the real project generator in
// internal/cli/upgrade_advisory_test.go.
func TestAdvisories_FreshScaffoldSaysNothing(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	results := inspect(t, dir, cfg, ForceNone(), true)
	frontendDir := cfg.Frontends[0].DeclaredDir()

	for path, r := range results {
		if !strings.HasPrefix(path, frontendDir) {
			continue
		}
		if r.Behind() {
			t.Errorf("fresh scaffold reported %s as %s (+%d/-%d)\n%s",
				path, r.Status, r.Missing, r.Local, r.Diff)
		}
	}
	// And the set is not vacuously silent.
	if _, ok := results[advisoryPath(cfg, advisoryQueryClient)]; !ok {
		t.Errorf("query-client.ts is not in the advisory set: %v", keysOf(results))
	}
}

// TestAdvisories_StaleFileIsReported: a file that is simply an older
// vintage — every line it has, the template still has — is reported as
// behind, with no claim that anything in it is the user's.
func TestAdvisories_StaleFileIsReported(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	dropLines(t, filepath.Join(dir, rel), 8)

	r := results(t, dir, cfg)[rel]
	if r.Status != AdvisoryBehind {
		t.Fatalf("a strictly-older file should be %q, got %q", AdvisoryBehind, r.Status)
	}
	if r.Missing == 0 {
		t.Error("a file missing template lines should report how many")
	}
	if r.Local != 0 {
		t.Errorf("nothing was added to this file, but %d lines were counted as the user's", r.Local)
	}
	if r.Diff == "" {
		t.Error("a behind file must carry the diff that shows what it is missing")
	}
}

// TestAdvisories_CustomizedFileIsNotCalledStale is the case the report
// exists to get right, because it is the common one: the file is BOTH
// customized and behind. The verdict must record both, so the report can
// say "this is a merge you make" instead of "you are out of date".
func TestAdvisories_CustomizedFileIsNotCalledStale(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	full := filepath.Join(dir, rel)
	dropLines(t, full, 6)
	appendLines(t, full,
		"",
		"// bigint-safe query-key hasher — JSON.stringify throws on BigInt.",
		"export function stableQueryKey(input: unknown): string {",
		"  return JSON.stringify(input, (_k, v) => (typeof v === \"bigint\" ? v.toString() : v));",
		"}",
	)

	r := results(t, dir, cfg)[rel]
	if r.Status != AdvisoryDiverged {
		t.Fatalf("a customized file should be %q, got %q", AdvisoryDiverged, r.Status)
	}
	if r.Local == 0 {
		t.Error("the user's own lines must be counted, or the report reads as 'merely outdated'")
	}
	if r.Missing == 0 {
		t.Error("the file is also behind; the template delta must still be reported")
	}
}

// TestAdvisories_SupersetIsSilent: a file that contains everything the
// current template has, plus lines of its own, is not behind at all — the
// template has nothing to offer it. Reporting it anyway ("behind by 0
// lines") is how a report earns its way into the part of the output people
// scroll past, and it fires on exactly the users who extended forge's
// scaffolding the way they were invited to.
func TestAdvisories_SupersetIsSilent(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	appendLines(t, filepath.Join(dir, rel),
		"",
		"export const QUERY_KEY_SALT = \"demo\";",
	)

	if r := results(t, dir, cfg)[rel]; r.Behind() {
		t.Errorf("a file that is a superset of the template reported %q (+%d/-%d)",
			r.Status, r.Missing, r.Local)
	}
}

// TestAdvisories_NamedForceAdopts_AndOnlyThat: adoption is per-path and
// nothing else moves. The unnamed customized file keeps every byte.
func TestAdvisories_NamedForceAdopts_AndOnlyThat(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	adopted := advisoryPath(cfg, advisoryQueryClient)
	spared := advisoryPath(cfg, "src/lib/events.ts")
	dropLines(t, filepath.Join(dir, adopted), 8)
	dropLines(t, filepath.Join(dir, spared), 8)
	sparedBefore := advisoryReadFile(t, filepath.Join(dir, spared))

	byPath := inspect(t, dir, cfg, ForcePaths(adopted), false)
	if r := byPath[adopted]; !r.Adopted {
		t.Fatalf("%s was named after --force but not adopted (status %q)", adopted, r.Status)
	}
	if byPath[spared].Adopted {
		t.Errorf("%s was not named but was written anyway", spared)
	}
	if got := advisoryReadFile(t, filepath.Join(dir, spared)); got != sparedBefore {
		t.Errorf("%s changed on disk without being named", spared)
	}
	// The adopted file now matches the template, so the next run is silent.
	if r := results(t, dir, cfg)[adopted]; r.Behind() {
		t.Errorf("after adoption %s still reports %q", adopted, r.Status)
	}
}

// TestAdvisories_AdoptionDoesNotCertifyTheFile: taking a better template
// must not cost the project ownership of the file it took it into. A
// forge:hash marker here would turn the user's NEXT edit to their own file
// into generated-file drift.
func TestAdvisories_AdoptionDoesNotCertifyTheFile(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	dropLines(t, filepath.Join(dir, rel), 8)

	inspect(t, dir, cfg, ForcePaths(rel), false)

	body := []byte(advisoryReadFile(t, filepath.Join(dir, rel)))
	if _, marked := checksums.ExtractMarker(body); marked {
		t.Error("adopting a scaffold-once template stamped the file as forge-certified")
	}
	if checksums.Verify(body) != checksums.NoMarker {
		t.Error("an adopted scaffold-once file must stay uncertified — it is the user's")
	}
}

// TestAdvisories_BareForceDoesNotReachThisTier: "overwrite everything I
// have edited" cannot be a statement about files forge handed over at
// birth. Adoption here requires naming the path.
func TestAdvisories_BareForceDoesNotReachThisTier(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	full := filepath.Join(dir, rel)
	dropLines(t, full, 8)
	before := advisoryReadFile(t, full)

	byPath := inspect(t, dir, cfg, ForceAll(), false)
	if byPath[rel].Adopted {
		t.Error("bare --force adopted a scaffold-once file")
	}
	if got := advisoryReadFile(t, full); got != before {
		t.Error("bare --force rewrote a scaffold-once file")
	}
}

// TestAdvisories_DisownedIsNeverOffered: disowning is a recorded, one-way
// statement that the file is the user's. Continuing to list it as
// adoptable is forge asking for it back every run.
func TestAdvisories_DisownedIsNeverOffered(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	dropLines(t, filepath.Join(dir, rel), 8)

	cs, err := LoadChecksums(dir)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if err := cs.DisownPaths(dir, []string{rel}, "hand-tuned retry policy"); err != nil {
		t.Fatalf("DisownPaths: %v", err)
	}
	out, err := InspectAdvisories(dir, cs, advisoryRows(t, dir, cfg), ForcePaths(rel), false)
	if err != nil {
		t.Fatalf("InspectAdvisories: %v", err)
	}
	for _, r := range out {
		if r.Path == rel {
			t.Fatalf("a disowned file was reported: %+v", r)
		}
	}
}

// TestAdvisories_AbsentFileIsReported: a project scaffolded before a
// mechanism module existed does not have the file at all. That is the
// purest form of "behind", and adopting it destroys nothing.
func TestAdvisories_AbsentFileIsReported(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	rel := advisoryPath(cfg, advisoryQueryClient)
	if err := os.Remove(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	r := results(t, dir, cfg)[rel]
	if r.Status != AdvisoryAbsent {
		t.Fatalf("a missing mechanism module should be %q, got %q", AdvisoryAbsent, r.Status)
	}
	if r.Missing == 0 {
		t.Error("an absent file should report the size of what the template ships")
	}

	inspect(t, dir, cfg, ForcePaths(rel), false)
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Errorf("naming an absent file after --force did not create it: %v", err)
	}
}

// TestAdvisories_SetIsEveryTemplateWrittenFile pins WHAT is reported.
//
// This lane originally covered the SHARED template roots only, and excluded
// the per-kind tree on the grounds that those files are "the platform
// wiring a project is expected to grow into". That conflated two different
// questions. Whether forge OWNS a file (and may rewrite it) is one; whether
// forge can TELL YOU the template moved is another. Answering the second
// with the first is what made frontends/<name>/eslint.config.mjs invisible
// to every forge command — while .golangci.yml, the same kind of file on
// the backend side, reported a diff and both remedies.
//
// So the rule is now the generic one — if forge wrote it from a template,
// its drift is reportable — and what is pinned here is the small exclusion
// set, each entry of which fails the lane's standing "exactly ONE renderer"
// admission rule.
func TestAdvisories_SetIsEveryTemplateWrittenFile(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	got := map[string]bool{}
	for _, row := range advisoryRows(t, dir, cfg) {
		got[filepath.ToSlash(row.Path)] = true
	}
	base := filepath.ToSlash(cfg.Frontends[0].DeclaredDir()) + "/"
	for _, want := range []string{
		// Shared mechanism modules — the lane's original membership.
		"src/lib/query-client.ts",
		"src/lib/events.ts",
		"src/lib/format-utils.ts",
		"src/hooks/use-api-query.ts",
		// Per-kind files, admitted now — the class eslint.config.mjs
		// belongs to, and which was equally invisible before.
		"src/lib/connect.ts",
		"next.config.ts",
		// The frontend image, rendered from the frontend's output mode —
		// how an existing project learns its Dockerfile is behind.
		"Dockerfile",
		".dockerignore",
	} {
		if !got[base+want] {
			t.Errorf("%s is written from a template but has no advisory row, so its drift "+
				"cannot be reported by any forge command", want)
		}
	}
	for _, unwanted := range []string{
		// Regenerated every run: the drift probe owns these and proves
		// drift from an embedded marker rather than inferring it.
		"src/lib/apiurl_gen.ts",
		"src/lib/basepath_gen.ts",
		// Seeded from the discovered entity set by the nav generator; this
		// lane has the frontend config but not the inventory.
		"src/components/nav.tsx",
		"src/app/dashboard.tsx",
		// Reconciled after render by EnsureWebRuntimeDependency, whose
		// web-runtime specifier differs between a released and a dev forge.
		"package.json",
		// Its peer pins are reconciled after render to the installed layout
		// and DECLARATION FILES. A plain render names package directories,
		// so this row would report every correctly-scaffolded tsconfig as
		// diverged — and adopting it would write the directory pins back,
		// the ones that split the webpack bundle ("No QueryClient set").
		"tsconfig.json",
		// Claimed by the MANAGED lane, which refreshes a pristine copy and
		// offers `disown`. It was registered in both lanes at once, so
		// `upgrade --check` listed it twice with two different remedies.
		frontendManagedRel,
	} {
		if got[base+unwanted] {
			t.Errorf("%s has a second renderer, so an advisory row would report forge's own "+
				"inconsistency as the user's drift", unwanted)
		}
	}

	// Dropping the advisory row must not restore the original bug (total
	// silence on the file) — the managed lane has to still cover it.
	managed := false
	for _, f := range frontendManagedFiles(cfg) {
		if filepath.ToSlash(f.destPath) == base+frontendManagedRel {
			managed = true
		}
	}
	if !managed {
		t.Errorf("%s%s is in neither upgrade lane — hand-editing it is invisible again", base, frontendManagedRel)
	}
}

// TestLineDelta_MeasuresBothDirections pins the discriminator the report
// leans on: lines the template has that you don't (how stale), and lines
// you have that no template line accounts for (how customized). Reordering
// is not rewriting, so it counts as neither.
func TestLineDelta_MeasuresBothDirections(t *testing.T) {
	tmpl := []byte("alpha\nbeta\ngamma\n")
	cases := []struct {
		name           string
		disk           string
		missing, local int
	}{
		{"identical", "alpha\nbeta\ngamma\n", 0, 0},
		{"reordered", "gamma\nalpha\nbeta\n", 0, 0},
		{"reindented", "  alpha\n\tbeta\ngamma\n", 0, 0},
		{"blank lines", "alpha\n\n\nbeta\ngamma\n", 0, 0},
		{"strictly older", "alpha\nbeta\n", 1, 0},
		{"customized", "alpha\nbeta\ngamma\nmine\n", 0, 1},
		{"both", "alpha\nmine\n", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, local := lineDelta([]byte(tc.disk), tmpl)
			if missing != tc.missing || local != tc.local {
				t.Errorf("lineDelta = (missing %d, local %d), want (%d, %d)",
					missing, local, tc.missing, tc.local)
			}
		})
	}
}

// TestFrontendTemplateTreeFor covers both vocabularies a forge.yaml entry
// can arrive in: the type detected from disk (nextjs / react-native /
// vite-spa) and the one a KCL declaration writes (nextjs / vite / rn).
func TestFrontendTemplateTreeFor(t *testing.T) {
	cases := []struct {
		fe   config.FrontendConfig
		want string
	}{
		{config.FrontendConfig{Type: "nextjs"}, "nextjs"},
		{config.FrontendConfig{Type: "react-native"}, "react-native"},
		{config.FrontendConfig{Type: "react_native"}, "react-native"},
		{config.FrontendConfig{Type: "vite-spa"}, "vite-spa"},
		{config.FrontendConfig{Type: "rn"}, "react-native"},
		{config.FrontendConfig{Type: "vite"}, "vite-spa"},
		{config.FrontendConfig{}, "nextjs"},
	}
	for _, tc := range cases {
		if got := frontendTemplateTreeFor(tc.fe); got != tc.want {
			t.Errorf("frontendTemplateTreeFor(%+v) = %q, want %q", tc.fe, got, tc.want)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func results(t *testing.T, dir string, cfg *config.ProjectConfig) map[string]AdvisoryResult {
	t.Helper()
	return inspect(t, dir, cfg, ForceNone(), true)
}

func keysOf(m map[string]AdvisoryResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func advisoryReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// dropLines removes n lines from the middle of a file, producing content
// that is a strict subset of the template's lines — the shape of a file
// that is simply an older vintage.
func dropLines(t *testing.T, path string, n int) {
	t.Helper()
	lines := strings.Split(advisoryReadFile(t, path), "\n")
	if len(lines) < n*3 {
		t.Fatalf("%s is too short (%d lines) to drop %d", path, len(lines), n)
	}
	mid := len(lines) / 2
	kept := append(append([]string{}, lines[:mid]...), lines[mid+n:]...)
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// appendLines adds the user's own content to a file.
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	body := advisoryReadFile(t, path) + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
