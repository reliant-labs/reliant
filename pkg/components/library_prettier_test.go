package components

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// scaffoldPrettierVersion is the prettier the scaffolded frontends pin, and
// forge's own pre-commit hook runs over these files (templates.PrettierVersion).
const scaffoldPrettierVersion = templates.PrettierVersion

// Library sources are copied verbatim into projects, which run prettier with
// the scaffolded config. A source that prettier would rewrite becomes churn in
// every project and fails its pre-commit hook. The sibling .prettierrc.json
// mirrors the scaffolded prettier.config.js (also what forge's own pre-commit
// resolves for these files).
func TestLibrarySourcesArePrettierClean(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the prettier run (downloads prettier on first use)")
	}
	cfg, err := os.ReadFile("../../internal/templates/frontend/nextjs/prettier.config.js")
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile(".prettierrc.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"printWidth", "100"}, {"trailingComma", `"all"`}, {"singleQuote", "false"}, {"tabWidth", "2"}, {"semi", "true"}} {
		if !strings.Contains(string(cfg), kv[0]+": "+strings.Trim(kv[1], `"`)) && !strings.Contains(string(cfg), kv[0]+": "+kv[1]) {
			t.Fatalf("scaffold prettier config no longer sets %s=%s; update .prettierrc.json and this test", kv[0], kv[1])
		}
		if !strings.Contains(strings.Join(strings.Fields(string(local)), ""), `"`+kv[0]+`":`+kv[1]) {
			t.Errorf(".prettierrc.json out of sync with scaffold config for %s=%s", kv[0], kv[1])
		}
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("npx (node) not on PATH under CI: install node so the prettier check runs")
		}
		t.Skip("npx not on PATH")
	}
	out, err := exec.Command(npx, "-y", "prettier@"+scaffoldPrettierVersion, "--check", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("library sources are not prettier-clean (run `npx prettier@%s --write pkg/components`):\n%s", scaffoldPrettierVersion, out)
	}
}
