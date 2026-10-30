// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// The org a request names reaches forge's environment as RELIANT_FORGE_ORG,
// which the credential helper reads; a request naming none adds nothing.
func TestForgeOrg_ReachesTheChildEnvironment(t *testing.T) {
	ctx, err := bindForgeOrg(context.Background(), " org-b ")
	if err != nil {
		t.Fatal(err)
	}
	if got := forgeOrgEnv(ctx); len(got) != 1 || got[0] != "RELIANT_FORGE_ORG=org-b" {
		t.Fatalf("env = %v", got)
	}
	ctx, err = bindForgeOrg(context.Background(), "")
	if err != nil || forgeOrgEnv(ctx) != nil {
		t.Fatalf("an unnamed org must add nothing: %v %v", err, forgeOrgEnv(ctx))
	}
}

func TestForgeOrg_RefusesAnIdThatCouldNotBeOne(t *testing.T) {
	for _, bad := range []string{"a b", "a\nb", "x\x00", strings.Repeat("a", 129)} {
		if _, err := bindForgeOrg(context.Background(), bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestForgeOrg_HandlerCarriesTheRequestedOrgToTheRunner(t *testing.T) {
	previous := runForge
	defer func() { runForge = previous }()
	var got string
	runForge = func(ctx context.Context, _ string, _ []string) (forgeCommandResult, error) {
		got = forgeOrgOf(ctx)
		return forgeCommandResult{Stdout: []byte(`{}`)}, nil
	}
	payload, err := json.Marshal(map[string]any{"project_path": forgeProject(t), "org_id": "org-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleForgeTopology(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if got != "org-b" {
		t.Fatalf("runner org = %q, want org-b", got)
	}
}

// The forge subprocess is first-party: keep its existing helper/server/OS
// environment, but the request's captured org wins over ambient stale state.
func TestForgeOrg_RealChildInheritsHelperAndServerButOverridesOrg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell child")
	}
	t.Setenv("RELIANT_SERVER_URL", "https://session.example.test")
	t.Setenv("FORGE_CREDENTIAL_HELPER", "reliant auth forge-credential --daemon-server https://session.example.test")
	t.Setenv(ForgeOrgEnv, "ambient-org")
	t.Setenv("FORGE_ENV_FORWARD_TEST", "inherited-value")
	ctx, err := bindForgeOrg(context.Background(), "captured-org")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runForgeProcess(ctx, t.TempDir(), "sh", "-c",
		`printf '%s\n' "$RELIANT_SERVER_URL" "$FORGE_CREDENTIAL_HELPER" "$RELIANT_FORGE_ORG" "$FORGE_ENV_FORWARD_TEST"`)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("child failed: exit=%d err=%v", result.ExitCode, err)
	}
	want := "https://session.example.test\nreliant auth forge-credential --daemon-server https://session.example.test\ncaptured-org\ninherited-value\n"
	if string(result.Stdout) != want || len(result.Stderr) != 0 {
		t.Fatal("child did not preserve the helper/server environment and captured org override")
	}
}

// A detached deploy job outlives its request, so the org rides the job.
func TestForgeOrg_SurvivesIntoADetachedJob(t *testing.T) {
	ctx, _ := bindForgeOrg(context.Background(), "org-b")
	job := &forgeDeployJob{orgID: forgeOrgOf(ctx)}
	jobCtx := withForgeOrg(context.Background(), job.orgID)
	if forgeOrgOf(jobCtx) != "org-b" {
		t.Fatalf("job context lost the org: %q", forgeOrgOf(jobCtx))
	}
}
