package cli

import (
	"context"
	"strings"
	"testing"
)

// The SC7 rehearsal (defect D4): `forge env deploy <env> -D name=value` was
// accepted, reached the release/build render, and was silently ignored by the
// manifest render that is applied. A flag that changes some renders and not
// others ships a release that disagrees with its own manifests.
//
// Chosen rule: a deploy renders from the repo alone, so -D is refused.

func TestRefuseDeployRenderOptions(t *testing.T) {
	if err := refuseDeployRenderOptions("prod", nil); err != nil {
		t.Fatalf("no -D is fine: %v", err)
	}
	err := refuseDeployRenderOptions("prod", []string{"region=eu", "tier=gold"})
	if err == nil {
		t.Fatal("-D on deploy must be refused")
	}
	for _, want := range []string{"does not take -D", "region=eu", "tier=gold", "forge env up prod -D", "forge env build prod -D", "deploy/kcl/prod/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q:\n%v", want, err)
		}
	}
}

// TestDispatchDeploy_RefusesDBeforeAnyWork drives the real dispatch for every
// shape of the verb: versioned, no version, --dry-run and --explain. The flag
// must stop each one before the project is even loaded, so nothing is
// rendered, built, planned or applied under an option that would be ignored.
//
// Mutation that fails it: restore bindBuildRenderOptions in dispatchDeployCmd
// (the flag is then accepted and the dispatch proceeds past the point where a
// project would be required).
func TestDispatchDeploy_RefusesDBeforeAnyWork(t *testing.T) {
	for name, f := range map[string]deployCmdFlags{
		"no version":       {},
		"--dry-run":        {dryRun: true},
		"--explain":        {explain: true},
		"versioned":        {promote: promoteCmdFlags{version: "v1.0.0"}},
		"--offline-ledger": {offlineLedger: true},
	} {
		t.Run(name, func(t *testing.T) {
			f.renderOptions = []string{"region=eu"}
			err := dispatchDeployCmd(context.Background(), "prod", f)
			if err == nil || !strings.Contains(err.Error(), "does not take -D") {
				t.Fatalf("want the -D refusal, got %v", err)
			}
		})
	}
}
