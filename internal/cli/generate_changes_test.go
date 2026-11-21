package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/changesdecl"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// changesProject is a project with a Go module and two web frontends, one
// of which depends on the web runtime, and a React Native one.
func changesProject(t *testing.T, goModule bool) (string, *config.ProjectConfig) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: shop\nmodule_path: example.com/shop\n")
	if goModule {
		write("go.mod", "module example.com/shop\n\ngo 1.24\n")
	}
	write("frontends/web/package.json", `{"dependencies": {"@reliantlabs/forge-web-runtime": "^0.14.0"}}`)
	write("frontends/bare/package.json", `{"dependencies": {"react": "19"}}`)
	write("frontends/mobile/package.json", `{"dependencies": {"@reliantlabs/forge-web-runtime": "^0.14.0"}}`)
	cfg, err := config.LoadProject([]byte("name: shop\nmodule_path: example.com/shop\n"), filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Frontends = []config.FrontendConfig{{Name: "web", Type: "vite"}, {Name: "bare", Type: "nextjs"}, {Name: "mobile", Type: "react-native"}}
	return dir, cfg
}

func writeChangesYAML(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, changesdecl.FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runChangesStep(t *testing.T, dir string, cfg *config.ProjectConfig) error {
	t.Helper()
	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	decl, err := changesdecl.Load(dir)
	if err != nil {
		return err
	}
	return generateChangesAccessors(dir, cfg, decl, cs)
}

func fileThere(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// The step writes the Go file and the TS file of every web frontend that
// depends on the runtime, rewrites them when the declaration changes, and
// deletes them when changes.yaml goes away; a project without the file gets
// nothing.
func TestChangesAccessorsFollowTheDeclaration(t *testing.T) {
	dir, cfg := changesProject(t, true)
	goFile := filepath.Join(dir, codegen.ChangesGoFile)
	webTS := filepath.Join(dir, "frontends/web", codegen.ChangesTSFile)
	bareTS := filepath.Join(dir, "frontends/bare", codegen.ChangesTSFile)
	mobileTS := filepath.Join(dir, "frontends/mobile", codegen.ChangesTSFile)

	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if fileThere(t, goFile) || fileThere(t, webTS) || fileThere(t, filepath.Join(dir, "internal/changes")) {
		t.Fatal("a project without changes.yaml got generated accessors")
	}

	writeChangesYAML(t, dir, shapeChangesYAML)
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	goSrc, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goSrc), "func NewCheckout(ctx context.Context, s *changes.Store, ec changes.EvalContext) bool") {
		t.Errorf("changes_gen.go lacks the accessor:\n%s", goSrc)
	}
	if checksums.Verify(goSrc) != checksums.Pristine {
		t.Error("changes_gen.go does not certify itself")
	}
	tsSrc, err := os.ReadFile(webTS)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tsSrc), "export function newCheckout(") {
		t.Errorf("changes_gen.ts lacks the accessor:\n%s", tsSrc)
	}
	if fileThere(t, bareTS) {
		t.Error("a frontend without the web runtime got changes_gen.ts, whose import would not resolve")
	}
	if fileThere(t, mobileTS) {
		t.Error("a React Native frontend got changes_gen.ts")
	}

	// An edit to the declaration is the source moving: the file follows.
	writeChangesYAML(t, dir, strings.Replace(shapeChangesYAML, "default: control", "default: treatment", 1))
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if goSrc, _ = os.ReadFile(goFile); !strings.Contains(string(goSrc), "KeyNewCheckout, ec), true)") {
		t.Errorf("changes_gen.go did not follow the new default:\n%s", goSrc)
	}

	if err := os.Remove(filepath.Join(dir, changesdecl.FileName)); err != nil {
		t.Fatal(err)
	}
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if fileThere(t, goFile) || fileThere(t, webTS) {
		t.Error("removing changes.yaml left the generated accessors behind")
	}
}

// A file the user edited after forge wrote it is theirs: removing the
// declaration does not delete it.
func TestChangesAccessorsKeepAHandEditedFile(t *testing.T) {
	dir, cfg := changesProject(t, true)
	writeChangesYAML(t, dir, shapeChangesYAML)
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(dir, codegen.ChangesGoFile)
	src, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, append(src, []byte("\n// mine\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, changesdecl.FileName)); err != nil {
		t.Fatal(err)
	}
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if !fileThere(t, goFile) {
		t.Error("a hand-edited changes_gen.go was deleted")
	}
}

// A project with no Go module (a static site) gets the TypeScript accessors
// only; an invalid declaration fails the step with its line.
func TestChangesAccessorsWithoutAGoModule(t *testing.T) {
	dir, cfg := changesProject(t, false)
	writeChangesYAML(t, dir, shapeChangesYAML)
	if err := runChangesStep(t, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if fileThere(t, filepath.Join(dir, codegen.ChangesGoFile)) {
		t.Error("a project with no go.mod got changes_gen.go")
	}
	if !fileThere(t, filepath.Join(dir, "frontends/web", codegen.ChangesTSFile)) {
		t.Error("the site's frontend got no changes_gen.ts")
	}
	writeChangesYAML(t, dir, "version: 1\nchanges:\n  k:\n    type: bool\n    default: on\n    variants: {off: false}\n")
	if err := runChangesStep(t, dir, cfg); err == nil || !strings.Contains(err.Error(), "changes.yaml:5:") {
		t.Errorf("an invalid declaration: got %v, want a refusal naming changes.yaml:5", err)
	}
}
