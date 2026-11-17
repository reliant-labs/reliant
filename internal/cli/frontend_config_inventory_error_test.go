package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// 7edd2e69: reliant's dev env stopped rendering (a host process with no
// observability.local_port), so its `web` declaration never reached the
// inventory, and its demo env's `forge-demo` — a second build of the same
// frontends/web directory — took the directory's place. `forge generate` then
// said "forge.yaml declares no frontend named web", naming a file with no
// frontends block and hiding both real causes. The refusal must lead with
// them: the env that did not render (with its check's own message, not
// "exit status 1"), and the directory known under another name.
func TestFrontendConfigMissingFrontendNamesTheRealCause(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "reliant",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "forge-demo", Type: "vite-spa"}.WithDir("frontends/web")},
	}
	messages := []codegen.ConfigMessage{{Name: "ReliantWebConfig", Frontend: "web", Fields: testWebFrontendConfig().Fields}}
	kclErr := errors.New("render deploy/kcl/dev/: evaluate KCL in /p: exit status 1\nError: kpm run /p/deploy/kcl/dev: EvaluationError\n" +
		"   \x1b[1;38;5;12m-->\x1b[0m render.k:822:1\n" +
		"\x1b[1;38;5;9m \x1b[0m \x1b[1;38;5;9mCheck failed on the condition: [reliant-api-server] run as host processes, which export OTLP to the local collector's loopback port\x1b[0m\n")
	failures := func() []envRenderFailure { return []envRenderFailure{{Env: "dev", Err: kclErr}} }

	err := generateFrontendConfigModules(cfg, messages, projectDir, &checksums.FileChecksums{}, failures)
	if err == nil {
		t.Fatal("a config bound to a frontend the inventory lacks must still be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		"deploy/kcl/dev did not render",
		"[reliant-api-server] run as host processes, which export OTLP to the local collector's loopback port",
		"env render dev",
		`frontends/web is in the inventory as "forge-demo"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not say %q:\n%s", want, msg)
		}
	}
	for _, not := range []string{"forge.yaml declares", "\x1b["} {
		if strings.Contains(msg, not) {
			t.Errorf("refusal still carries %q:\n%s", not, msg)
		}
	}
}

// With nothing to blame, the refusal is the plain one, and it does not render
// anything to find out: the failures are read only on the error path.
func TestFrontendConfigMissingFrontendWithNoCause(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "demo", Frontends: []config.FrontendConfig{{Name: "admin", Type: "nextjs"}}}
	messages := []codegen.ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: testWebFrontendConfig().Fields}}
	err := generateFrontendConfigModules(cfg, messages, t.TempDir(), &checksums.FileChecksums{}, func() []envRenderFailure { return nil })
	if err == nil {
		t.Fatal("want a refusal")
	}
	if msg := err.Error(); !strings.Contains(msg, "(its frontends: admin)") || !strings.Contains(msg, "Either correct the annotation") || strings.Contains(msg, "Likely cause") {
		t.Errorf("plain refusal is wrong:\n%s", msg)
	}
}
