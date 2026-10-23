package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

func deployEnabledConfig(t *testing.T) *config.ProjectConfig {
	t.Helper()
	cfg := &config.ProjectConfig{}
	cfg.Features = cfg.Features.With(config.FeatureDeploy, true)
	if !cfg.Features.DeployEnabled() {
		t.Fatalf("test setup: deploy feature did not enable; the FeaturesConfig shape has changed")
	}
	return cfg
}

// TestKCLPluginCheckNameIsTheReliantContract pins the name reliant's daemon
// matches exactly (forgeKCLPluginCheckName). A rename is a silent break of
// the Forge tab's "can this daemon render?" answer.
func TestKCLPluginCheckNameIsTheReliantContract(t *testing.T) {
	if kclPluginCheckName != "forge: kcl-plugin" {
		t.Fatalf("check name = %q; reliant matches \"forge: kcl-plugin\" exactly", kclPluginCheckName)
	}
}

func TestKCLPluginCheckPassesWhenTheProbeRenders(t *testing.T) {
	got := kclPluginCheckResult(deployEnabledConfig(t), t.TempDir(), func() error { return nil })
	if got.Status != doctor.StatusPass {
		t.Fatalf("status = %q, want %q", got.Status, doctor.StatusPass)
	}
}

// TestKCLPluginCheckFailsWithARunbook: a machine that cannot load KCL must
// not report healthy, and must say where the library lives and how to move it.
func TestKCLPluginCheckFailsWithARunbook(t *testing.T) {
	t.Setenv("KCL_LIB_HOME", "")
	got := kclPluginCheckResult(deployEnabledConfig(t), t.TempDir(), func() error {
		return errors.New("open kcl.dll: Access is denied")
	})
	if got.Status != doctor.StatusFail {
		t.Fatalf("status = %q, want %q", got.Status, doctor.StatusFail)
	}
	for _, want := range []string{"Access is denied", "KCL_LIB_HOME", "kcl"} {
		if !strings.Contains(got.Message+"\n"+got.Evidence, want) {
			t.Errorf("report missing %q\n--- message ---\n%s\n--- evidence ---\n%s", want, got.Message, got.Evidence)
		}
	}
}

func TestKCLPluginCheckSkippedWhenDeployDisabled(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Features = cfg.Features.With(config.FeatureDeploy, false)
	probed := false
	got := kclPluginCheckResult(cfg, t.TempDir(), func() error { probed = true; return nil })
	if got.Status != doctor.StatusSkip {
		t.Fatalf("status = %q, want %q for a project with deploy disabled", got.Status, doctor.StatusSkip)
	}
	if probed {
		t.Error("a project that never renders must not pay for loading the KCL runtime")
	}
}

func TestKCLPluginCheckEmitsNothingUnderSignalFilter(t *testing.T) {
	if got := runKCLPluginDoctorCheck(t.Context(), deployEnabledConfig(t), t.TempDir(), "deploy"); got != nil {
		t.Fatalf("filtered run emitted %d checks, want none", len(got))
	}
}

// TestKCLPluginCheckRealProbePasses runs the real probe: on any machine where
// forge's tests run, KCL loads, so the shipped check must pass here.
func TestKCLPluginCheckRealProbePasses(t *testing.T) {
	got := runKCLPluginDoctorCheck(t.Context(), deployEnabledConfig(t), t.TempDir(), "")
	if len(got) != 1 || got[0].Status != doctor.StatusPass {
		t.Fatalf("real probe: %+v", got)
	}
}
