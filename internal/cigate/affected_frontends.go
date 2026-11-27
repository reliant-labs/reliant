package cigate

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/templates"
)

// RelatedScript is the package.json script a frontend declares to run only
// the tests related to some files: the gate passes the changed files under
// the frontend, relative to its directory, as arguments. A vitest frontend
// declares `"test:related": "vitest related --run --passWithNoTests"`. forge
// reads no test runner's config: the script is the frontend's word for which
// tests a file reaches.
const RelatedScript = "test:related"

// FrontendTestName is the affected-mode row of frontend fe's tests.
func FrontendTestName(fe string) string { return "test-" + fe }

// WithFrontendTests adds a test-<name> row after unit-tests for each frontend
// an affected rule selected (a.Frontends). In full mode, and when no rule
// selected a frontend, checks come back unchanged: `task test` runs every
// frontend's suite itself, and a narrowed `task test -- <packages>` runs
// none, which is what these rows are for.
//
// The row runs the frontend's test:related script with the changed files
// under the frontend when it declares one and every such file is a source
// module (relatableSource); otherwise — a config file, manifest or lockfile
// changed, or no changed file is under it (a rule claimed a file elsewhere
// for it) — the whole `test` script, as `task test` runs it.
func WithFrontendTests(checks []Check, a Affected, root string, frontends []templates.FrontendCIConfig) []Check {
	selected := append([]string(nil), a.Frontends...)
	for _, c := range a.FullChecks {
		if fe, ok := strings.CutPrefix(c, "test-"); ok && !slices.Contains(selected, fe) {
			selected = append(selected, fe)
		}
	}
	sort.Strings(selected)
	if a.Full || len(selected) == 0 {
		return checks
	}
	at := -1
	for i, c := range checks {
		if c.Name == UnitTests {
			at = i
		}
	}
	if at < 0 || checks[at].Deferred != "" {
		return checks
	}
	byName := map[string]templates.FrontendCIConfig{}
	for _, fe := range frontends {
		byName[fe.Name] = fe
	}
	taken := map[string]bool{}
	for _, c := range checks {
		taken[c.Name] = true
	}
	var rows []Check
	for _, name := range selected {
		fe, ok := byName[name]
		if !ok || taken[FrontendTestName(name)] {
			continue // Plan rejects an unknown frontend; a project row of this name wins
		}
		whole := slices.Contains(a.FullChecks, FrontendTestName(name))
		rows = append(rows, frontendTest(root, fe, changedUnder(a.Changed, fe.Path), whole))
	}
	out := make([]Check, 0, len(checks)+len(rows))
	out = append(out, checks[:at+1]...)
	out = append(out, rows...)
	return append(out, checks[at+1:]...)
}

// frontendTest builds fe's row; whole (a rule's full_checks) runs the whole
// `test` script whatever changed.
func frontendTest(root string, fe templates.FrontendCIConfig, changed []string, whole bool) Check {
	c := Check{
		Name:   FrontendTestName(fe.Name),
		Lane:   LaneTest,
		Group:  GroupTest,
		Heavy:  true,
		After:  []string{depsName(fe)},
		Prereq: needsDepsAt(root, fe),
		Dir:    fe.Path,
	}
	scripts, err := packageScripts(filepath.Join(root, fe.Path))
	if err != nil {
		c.Prereq = func() string { return "cannot read the frontend's scripts: " + err.Error() }
		return c
	}
	// npm hands a script its arguments only after `--`; pnpm and yarn pass
	// them as they are (the Taskfile's test:frontend does the same).
	sep := []string(nil)
	if frontenddeps.PackageManager(filepath.Join(root, fe.Path), "") == "npm" {
		sep = []string{"--"}
	}
	// Only source modules can be related to tests. A changed config file,
	// manifest or lockfile can change every test while relating to none, so
	// one among the changes runs the whole suite. A deleted source file is
	// not passed (it has no tests of its own to relate; its importers are
	// related through the files that changed with it, or the suite runs).
	nonSource := ""
	var related []string
	for _, f := range changed {
		if !relatableSource(f) {
			if nonSource == "" {
				nonSource = f
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(root, fe.Path, filepath.FromSlash(f))); err == nil {
			related = append(related, f)
		}
	}
	switch {
	case whole && scripts["test"] != "":
		c.Argv = feRunAt(root, fe, append(append([]string{"test"}, sep...), "--passWithNoTests")...)
		c.Note = "affected mode: whole suite — ci.gate.affected_rules full_checks names " + c.Name
	case scripts[RelatedScript] != "" && nonSource == "" && len(related) > 0:
		c.Argv = feRunAt(root, fe, append(append([]string{RelatedScript}, sep...), related...)...)
		c.Note = fmt.Sprintf("affected mode: tests related to %d changed file(s)", len(related))
	case scripts["test"] != "":
		// The same command `task test` runs for this frontend.
		c.Argv = feRunAt(root, fe, append(append([]string{"test"}, sep...), "--passWithNoTests")...)
		switch {
		case scripts[RelatedScript] == "" && len(changed) > 0:
			c.Note = "affected mode: whole suite (package.json declares no " + RelatedScript + " script)"
		case nonSource != "":
			c.Note = "affected mode: whole suite — " + nonSource + " is not a source module, so no related-test selection covers it"
		case len(changed) > 0:
			c.Note = "affected mode: whole suite — only deleted files changed under " + fe.Path
		default:
			c.Note = "affected mode: selected by ci.gate.affected_rules with no changed file under " + fe.Path + " — whole suite"
		}
	default:
		c.NotApplicable = "package.json declares no test or " + RelatedScript + " script"
	}
	return c
}

// sourceExts are the extensions of the modules a frontend's test runner can
// relate tests to. Anything else (JSON, YAML, HTML, a lockfile, .env, an
// asset) runs the whole suite: forge cannot know which tests read it.
var sourceExts = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".vue": true, ".svelte": true, ".astro": true,
	".css": true, ".scss": true, ".sass": true, ".less": true,
}

// rootConfigPrefixes are base-name prefixes of the tool configs a frontend
// keeps in its root (vite.config.ts, vitest.setup.ts, jest.config.js): a
// file directly in the frontend root that starts with one is config, whatever
// its extension.
var rootConfigPrefixes = []string{"vite.", "vitest.", "jest.", "playwright.", "cypress.", "tsconfig", "next.", "svelte.", "astro.", "nuxt."}

// relatableSource reports whether rel (relative to the frontend) is a source
// module whose tests a test:related script can find. It is deliberately
// generic and conservative — it reads no tool's config: a source extension,
// not a `*.config.*` file anywhere, and not a tool's file in the frontend
// root. A wrong "no" costs a whole-suite run; a wrong "yes" would pass a
// config change as tested.
func relatableSource(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := path.Base(rel)
	if !sourceExts[strings.ToLower(path.Ext(base))] {
		return false
	}
	if strings.Contains(base, ".config.") || strings.HasPrefix(base, ".") {
		return false
	}
	if !strings.Contains(rel, "/") {
		for _, p := range rootConfigPrefixes {
			if strings.HasPrefix(base, p) {
				return false
			}
		}
	}
	return true
}

// changedUnder returns the changed files under dir — deleted ones too —
// relative to dir.
func changedUnder(changed []string, dir string) []string {
	prefix := strings.TrimSuffix(path.Clean(filepath.ToSlash(dir)), "/") + "/"
	if prefix == "./" {
		prefix = "" // a frontend at the project root
	}
	var out []string
	for _, f := range changed {
		if f = filepath.ToSlash(f); strings.HasPrefix(f, prefix) {
			out = append(out, strings.TrimPrefix(f, prefix))
		}
	}
	return out
}

// packageScripts reads the scripts of dir's package.json.
func packageScripts(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return manifest.Scripts, nil
}
