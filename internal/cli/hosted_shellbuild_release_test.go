package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// hostedShellEnv is a hosted env whose workload declares its image BARE and
// builds it with a ShellBuild — the ADR-0003 F1 default shape, and the one
// the cp PIN-1 run hit.
func hostedShellEnv() *KCLEntities {
	return &KCLEntities{
		ControlPlane: testControlPlane("acme", "registry.example.com"),
		Workloads: []WorkloadEntity{{
			Name:    "echo",
			Image:   "echo",
			Runtime: RuntimeEntity{Type: RuntimeHosted},
			Build:   BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true"}},
		}},
	}
}

// TestCheckReleaseCoversEnv_KeysHostedWorkloadsByResolvedRepository pins the
// other half of the bare-hosted resolution: the release-coverage gate must
// look the artifact up under the address it was PUSHED to.
//
// The ledger is keyed by repository, and for a bare hosted image that
// repository is `<push base>/<name>` — what the build state records once the
// ShellBuild path resolves it. The gate was comparing against the raw declared
// `echo`, so a cut whose ledger held the correct resolved entry was refused as
// not covering the env. Fixing the build state alone would therefore have
// turned a silent empty digest into a hard refusal.
func TestCheckReleaseCoversEnv_KeysHostedWorkloadsByResolvedRepository(t *testing.T) {
	ents := hostedShellEnv()
	resolved := "registry.example.com/acme/" + hostedProjectName() + "/echo"

	artifacts := map[string]release.Artifact{
		resolved: {
			Kind:    release.KindOCI,
			Mode:    release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("ab", 32)},
		},
	}
	if err := checkReleaseCoversEnv(ents, artifacts, buildOptions{env: "e2eh", release: "v1"}); err != nil {
		t.Fatalf("a ledger holding the RESOLVED ref %s covers this env; the cut must not be refused:\n%v",
			resolved, err)
	}

	// And the gate must still catch a genuinely missing artifact — an entry
	// under the unresolved name is not the address anything pulls.
	unresolved := map[string]release.Artifact{
		"echo": {Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("cd", 32)}},
	}
	err := checkReleaseCoversEnv(ents, unresolved, buildOptions{env: "e2eh", release: "v1"})
	if err == nil {
		t.Fatal("an artifact keyed by the UNRESOLVED name is not the pushed address; the gate must still refuse")
	}
	if !strings.Contains(err.Error(), resolved) {
		t.Errorf("the refusal must name the address forge looked for (%s), so the author can compare it with the ledger; got:\n%v",
			resolved, err)
	}
}
