package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
)

// An environment that fails to render must not shrink the observability
// outputs: the step fails and writes nothing (forge bug report f9a3c9a1 — a
// version-skewed KCL import dropped a frontend and 512 lines of panels from
// control-plane's project_gen.json while the run went on to write it).
func TestObservabilityDashboards_EnvRenderFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	ctx := &pipelineContext{
		Cfg:          &config.ProjectConfig{Name: "demo"},
		ProjectDir:   dir,
		AbsPath:      dir,
		Strict:       true,
		renderedRead: true,
		rendered: renderedEnvironments{failed: []envRenderFailure{
			{Env: "prod", Err: errors.New("EvaluationError: version skew in imported module")},
		}},
	}
	err := stepObservabilityDashboards(ctx)
	if err == nil {
		t.Fatal("stepObservabilityDashboards succeeded with an env that did not render; want the step to fail")
	}
	for _, want := range []string{"deploy/kcl/prod", "version skew in imported module", "left as they were"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "deploy", "observability")); !os.IsNotExist(statErr) {
		t.Errorf("deploy/observability was written despite the failed render (stat err %v)", statErr)
	}
}

func TestObservabilityRenderGuard_AllRenderedPasses(t *testing.T) {
	if err := observabilityRenderGuard(nil); err != nil {
		t.Fatalf("observabilityRenderGuard(nil) = %v, want nil", err)
	}
}

// `--help-dev` prints a command's hidden flags; it is a flag, not cobra's help
// command, so the root pre-run hook runs for it. It must not configure the
// clone (git hooks, the generated-file merge driver) — forge bug report
// 44e774fb saw `forge generate --help-dev` rewrite a shared git config.
func TestHookActivationSkipped_HelpDev(t *testing.T) {
	cmd := &cobra.Command{Use: "generate", RunE: func(*cobra.Command, []string) error { return nil }}
	cmdutil.HideDevFlags(cmd)
	if hookActivationSkipped(cmd) {
		t.Fatal("hookActivationSkipped = true before --help-dev was passed")
	}
	if err := cmd.Flags().Parse([]string{"--help-dev"}); err != nil {
		t.Fatal(err)
	}
	if !hookActivationSkipped(cmd) {
		t.Fatal("hookActivationSkipped = false for --help-dev; want the help-only run to leave git config alone")
	}
}
