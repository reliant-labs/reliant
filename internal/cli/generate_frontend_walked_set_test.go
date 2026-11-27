package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The repoint pass walks a whole src/ tree per frontend, and its failure
// mode is SILENCE: a frontend that should have been walked but was not
// keeps an old import specifier, which surfaces much later as a
// TypeScript error in a file nobody connected to this pass. Nothing
// asserted the walked SET before the frontend-location consolidation, so
// this fixes that gap first — it is written to pass against the inline
// `if feDir == "" { feDir = filepath.Join("frontends", fe.Name) }` the
// pass used to carry, so a divergence introduced by the move to
// FrontendConfig.Dir shows up here rather than in a user's build.
//
// The three shapes that matter, in one project:
//
//   - conventional: declared path, walked.
//   - custom in-repo path: walked, at the declared location and NOT at
//     frontends/<name>.
//   - cross-repo source pin: NOT walked. Its code is in another
//     repository; rewriting imports there would edit a tree this project
//     does not own, in files its own forge maintains.
func TestRewriteRenamedFrontendImportsWalkedSet(t *testing.T) {
	root := t.TempDir()

	// Every candidate location gets a file with the OLD specifier, so a
	// pass that walks too much is caught just as loudly as one that
	// walks too little.
	write := func(rel string) string {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "import type { Scenario } from \"../scenario-types\";\n"
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return full
	}

	conventional := write("frontends/console/src/a.ts")
	customPath := write("apps/admin/src/a.ts")
	// The location the convention WOULD have invented for the custom-path
	// frontend. Nothing should ever be walked here.
	inventedForCustom := write("frontends/admin/src/a.ts")
	// The location the convention would invent for the sourced frontend.
	inventedForSourced := write("frontends/sibling-web/src/a.ts")

	cfg := &config.ProjectConfig{
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "console", Type: "vite-spa"}.WithDir("frontends/console"),
			config.FrontendConfig{Name: "admin", Type: "nextjs"}.WithDir("apps/admin"),
			{
				Name:   "sibling-web",
				Type:   "vite-spa",
				Source: &config.GitSource{Repo: "github.com/example/sibling", Ref: "v1.0.0"},
			},
		},
	}

	rewriteRenamedFrontendImports(cfg, root, []renamedFrontendModule{
		{old: "../scenario-types", new: "../scenario-types_gen"},
	})

	rewritten := func(path string) bool {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) == "import type { Scenario } from \"../scenario-types_gen\";\n"
	}

	for _, c := range []struct {
		name string
		path string
		want bool
		why  string
	}{
		{"conventional path", conventional, true,
			"a declared, in-repo frontend must be walked"},
		{"custom in-repo path", customPath, true,
			"a frontend at a custom path must be walked AT that path"},
		{"invented path for custom-path frontend", inventedForCustom, false,
			"frontends/<name> must not be walked for a frontend that declares a different path"},
		{"invented path for sourced frontend", inventedForSourced, false,
			"a cross-repo frontend has no directory here; frontends/<name> is an invented location"},
	} {
		if got := rewritten(c.path); got != c.want {
			t.Errorf("%s: rewritten = %v, want %v — %s", c.name, got, c.want, c.why)
		}
	}
}
