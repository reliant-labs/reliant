package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

const (
	testRegistryHost = "registry.reliant.dev"
	testProject      = "acme"
)

// hostedImageProject writes a deploy tree with one OnHosted workload carrying
// image. The org is the credential's, so the tree names none.
func hostedImageProject(t *testing.T, image string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "deploy/kcl/workloads.k", `
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    image = "`+image+`"
}
`)
	controlPlane := `forge.ControlPlane {endpoint = "https://cp.example", registry_host = "` + testRegistryHost + `"}`
	write(t, dir, "deploy/kcl/prod/main.k", `
import forge
import workloads as wl

_bundle = forge.Bundle {
    project = "`+testProject+`"
    control_plane = `+controlPlane+`
    workloads = [wl.api | {runtime = forge.OnHosted {}}]
}
`)
	return dir
}

// testCfg is the project config the lint reads the `<project>` segment from.
func testCfg() *config.ProjectConfig { return &config.ProjectConfig{Name: testProject} }

// The lint holds no credential, so it has no org and no push base: every
// finding is the weaker, unverified fact that a host was declared. `forge env
// render <env>` does the verified version against the credential's org.
func TestHostedImageBaseLint_ReportsTheUnverifiedFact(t *testing.T) {
	dir := hostedImageProject(t, "ghcr.io/acme/api")
	findings := hostedImageBaseFindings(dir, testCfg())
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one", findings)
	}
	if findings[0].Verified() {
		t.Error("a finding with no base must not claim to be verified")
	}
	if msg := findings[0].Message(); !strings.Contains(msg, "drop the host and forge resolves it") {
		t.Errorf("message = %s, want the drop-the-host advice", msg)
	}
}

// A BARE hosted image is the shape ADR-0003 F1 wants. It must produce NO
// finding, or the lint would push authors back toward the declaration that
// caused the defect.
func TestHostedImageBaseLint_BareImageIsClean(t *testing.T) {
	dir := hostedImageProject(t, "api")
	if findings := hostedImageBaseFindings(dir, testCfg()); len(findings) != 0 {
		t.Fatalf("a bare hosted image produced findings: %+v", findings)
	}
}

// An OFF-BASE image never gates, in either arm. `forge env render <env>` is
// the authoritative check; this scan under-reports by construction, so a
// project must not be failed on it.
func TestHostedImageBaseLint_OffBaseNeverGates(t *testing.T) {
	dir := hostedImageProject(t, "ghcr.io/acme/api")
	findings, gated, err := collectHostedImageBaseJSON(&lintRunCtx{cwd: dir, cfg: testCfg()})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if gated {
		t.Error("the JSON arm gated")
	}
	if len(findings) != 1 || findings[0].Severity != lintSevWarning {
		t.Fatalf("findings = %+v, want one warning", findings)
	}
	if !strings.HasPrefix(findings[0].Rule, "hosted-image-base/") {
		t.Errorf("rule = %q, want a hosted-image-base/* rule", findings[0].Rule)
	}
}

// A project with no deploy tree is silent.
func TestHostedImageBaseLint_NoDeployTreeIsSilent(t *testing.T) {
	if findings := hostedImageBaseFindings(t.TempDir(), testCfg()); len(findings) != 0 {
		t.Fatalf("findings = %+v, want none", findings)
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
