package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/obsdash"
)

// A project adopting forge's handler model one service at a time — reliant's
// P3b — grows internal/handlers/, which derives the kind to service and
// observability on. These pin what that change does now: it says so the
// first time it writes, and a features: pin keeps it off (F15).

func adoptingTree(t *testing.T, forgeYAMLExtra string) (string, *config.ProjectConfig) {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"cmd/app/main.go":                       "package main\n\nfunc main() {}\n",
		"proto/app/v1/app.proto":                "syntax = \"proto3\";\n",
		"internal/handlers/question/service.go": "package question\n",
		"forge.yaml":                            "name: app\nmodule_path: example.com/app\n" + forgeYAMLExtra,
	} {
		writeTreeFileT(t, filepath.Join(dir, rel), body)
	}
	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

func TestObservability_FirstDerivedEmissionIsAnnounced(t *testing.T) {
	dir, cfg := adoptingTree(t, "")
	ctx := &pipelineContext{AbsPath: dir, ProjectDir: dir, Cfg: cfg}
	if !gateObservabilityDashboardsEnabled(ctx) {
		t.Fatal("precondition: internal/handlers/ derives observability on")
	}
	var out bytes.Buffer
	announceDerivedObservability(&out, ctx)
	for _, want := range []string{"observability is on (derived: internal/handlers/ exists)", "features: {observability: false}"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the first emission must say %q:\n%s", want, out.String())
		}
	}

	// Once its outputs exist, every later run is silent.
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(obsdash.ProjectSpecFile)), "{}\n")
	out.Reset()
	announceDerivedObservability(&out, ctx)
	if out.Len() != 0 {
		t.Errorf("a run that finds the outputs on disk must say nothing:\n%s", out.String())
	}
}

func TestObservability_PinIsTheSwitch(t *testing.T) {
	dir, cfg := adoptingTree(t, "features:\n  observability: false\n")
	if gateObservabilityDashboardsEnabled(&pipelineContext{AbsPath: dir, Cfg: cfg}) {
		t.Error("a pinned-off observability must gate the dashboards off")
	}

	// Pinned on: no announcement, the user asked for it.
	dir, cfg = adoptingTree(t, "features:\n  observability: true\n")
	ctx := &pipelineContext{AbsPath: dir, Cfg: cfg}
	var out bytes.Buffer
	announceDerivedObservability(&out, ctx)
	if out.Len() != 0 {
		t.Errorf("a pinned feature is no surprise and must not be announced:\n%s", out.String())
	}
}

func TestFeatures_ReasonNamesThePin(t *testing.T) {
	_, cfg := adoptingTree(t, "features:\n  observability: false\n")
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printFeatureGraph(cmd, cfg); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, " observability ") {
			if !strings.Contains(line, "[ ]") || !strings.Contains(line, "pinned in forge.yaml") {
				t.Errorf("observability must read off and pinned: %q", line)
			}
			return
		}
	}
	t.Fatalf("no observability line:\n%s", out.String())
}

// The prerequisites audit renders the dev KCL. A service-shaped project that
// forge does not deploy has none, and used to get a warning for it the
// moment internal/handlers/ made it service-kind.
func TestAuditPrerequisites_NoDeployTreeIsNotApplicable(t *testing.T) {
	dir, cfg := adoptingTree(t, "")
	if cfg.EffectiveKind() != config.ProjectKindService {
		t.Fatalf("precondition: service-kind, got %s", cfg.EffectiveKind())
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "kcl")); !os.IsNotExist(err) {
		t.Fatal("precondition: no deploy/kcl")
	}
	cat := auditPrerequisites(cfg, dir)
	if cat.Status != "ok" || !strings.Contains(cat.Summary, "n/a") {
		t.Errorf("want ok n/a without a deploy tree, got %s: %s", cat.Status, cat.Summary)
	}
}
