package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/configproto"
)

// Every file forge writes into a project must already be what that project's
// own formatters would make of it.
//
// A scaffolded project runs trailing-whitespace, end-of-file-fixer and
// `buf format` in pre-commit. A file those hooks rewrite is either a
// forge-generated file — which `forge ci verify-generated` then reports as
// drifted, so the two gates fight forever — or a scaffold the user must
// reformat before their first commit can pass. houndersclub's first PR hit
// every file below.

func assertFormatterClean(t *testing.T, name string, content []byte) {
	t.Helper()
	s := string(content)
	for i, line := range strings.Split(s, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Errorf("%s:%d has trailing whitespace: %q", name, i+1, line)
		}
	}
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		tail := s
		if len(tail) > 20 {
			tail = tail[len(tail)-20:]
		}
		t.Errorf("%s must end with exactly one newline; ends %q", name, tail)
	}
}

func TestObservabilityFilesAreFormatterClean(t *testing.T) {
	dir := t.TempDir()
	g := &ProjectGenerator{Name: "demo", Path: dir, ModulePath: "github.com/example/demo"}
	if err := g.generateObservability(); err != nil {
		t.Fatalf("generateObservability: %v", err)
	}
	for _, rel := range []string{
		"deploy/observability/otel-collector.yaml",
		"deploy/observability/dashboards/README.md",
		"deploy/observability/otel-profiles.yaml",
	} {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		assertFormatterClean(t, rel, b)
	}
}

func TestProjectJSONIsFormatterClean(t *testing.T) {
	g := &ProjectGenerator{Name: "demo", Path: t.TempDir(), ModulePath: "github.com/example/demo"}
	reliantDir := filepath.Join(g.Path, ".reliant")
	if err := os.MkdirAll(reliantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.writeProjectJSON(reliantDir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(reliantDir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertFormatterClean(t, ".reliant/project.json", b)
}

// scaffoldConfigProtos writes both scaffolded config protos (plus the
// forge.proto they import and a buf.yaml) into a fresh project dir, through
// the real writers, and returns the dir and the protos' relative paths.
func scaffoldConfigProtos(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	g := &ProjectGenerator{
		Name:       "demo",
		Path:       dir,
		ModulePath: "github.com/example/demo",
		Kind:       "service",
		Features:   config.FeaturesConfig{},
	}
	if err := g.createConfigProto(); err != nil {
		t.Fatalf("createConfigProto: %v", err)
	}
	if err := WriteFrontendConfigProto(dir, configproto.For(g.Name), g.ModulePath, "web", 8080); err != nil {
		t.Fatalf("WriteFrontendConfigProto: %v", err)
	}
	if err := copyTree(t, filepath.Join("..", "assets", "proto", "forge"), filepath.Join(dir, "proto", "forge")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, []string{"proto/demo/config/v1/config.proto", "proto/demo/config/v1/web_config.proto"}
}

// The scaffolded config protos are `buf format`-clean: imports before
// options, and no separators between fields of a message-literal option.
// houndersclub's pre-commit `buf format` rewrote both on its first PR.
func TestScaffoldedConfigProtosAreFormatClean(t *testing.T) {
	dir, protos := scaffoldConfigProtos(t)
	for _, rel := range protos {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		assertFormatterClean(t, rel, b)
		assertProtoStructurallyFormatted(t, rel, string(b))
	}
}

// The same protos, judged by buf itself — the authority the structural check
// above approximates.
func TestScaffoldedConfigProtosPassBufFormat(t *testing.T) {
	requireBuf(t)
	dir, protos := scaffoldConfigProtos(t)
	args := []string{"format", "--diff", "--exit-code"}
	for _, rel := range protos {
		args = append(args, "--path", rel)
	}
	cmd := exec.Command("buf", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("scaffolded config protos are not `buf format` clean (%v):\n%s", err, out)
	}
}

// requireBuf skips when buf is not installed on a developer's machine and
// FAILS under CI (or FORGE_E2E_REQUIRE_TOOLS), where a missing tool is a
// provisioning bug — mirroring internal/cli's requireTool, so the check either
// runs or says out loud that it did not.
func requireBuf(t *testing.T) {
	t.Helper()
	var missing []string
	if _, err := exec.LookPath("buf"); err != nil {
		missing = append(missing, "buf")
	}
	if len(missing) == 0 {
		return
	}
	required := os.Getenv("CI") != ""
	if v, ok := os.LookupEnv("FORGE_E2E_REQUIRE_TOOLS"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "0", "false", "no", "off":
			required = false
		default:
			required = true
		}
	}
	if !required {
		t.Skip("buf not on PATH — skipped locally. This is a HARD FAILURE in CI; run with FORGE_E2E_REQUIRE_TOOLS=1 to reproduce that here.")
	}
	t.Fatal("buf not on PATH under CI, where a missing tool is a provisioning bug, not a property of the machine")
}

// assertProtoStructurallyFormatted checks the two shapes `buf format`
// rewrites in forge's templates, for machines without buf.
func assertProtoStructurallyFormatted(t *testing.T, name, src string) {
	t.Helper()
	lastImport, firstOption := -1, -1
	inLiteral := false
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "import "):
			lastImport = i
		case strings.HasPrefix(trimmed, "option ") && firstOption < 0 && !strings.HasPrefix(line, " "):
			firstOption = i
		}
		if strings.HasSuffix(trimmed, "= {") {
			inLiteral = true
			continue
		}
		if inLiteral && strings.HasPrefix(trimmed, "}") {
			inLiteral = false
			continue
		}
		if inLiteral && strings.HasSuffix(trimmed, ",") && !strings.HasPrefix(trimmed, "//") {
			t.Errorf("%s:%d: separator after a message-literal field (buf format removes it): %q", name, i+1, trimmed)
		}
	}
	if firstOption >= 0 && lastImport > firstOption {
		t.Errorf("%s: a file option precedes an import (buf format moves options after imports)", name)
	}
}

func copyTree(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
