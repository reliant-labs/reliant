package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// AUTO-LOGIN (ADR-0003 F3). A hosted push authenticates itself. The author
// runs `forge env build prod --push` or `forge env deploy prod` and forge logs
// docker in to the platform registry first, from the control-plane credential
// it already resolves — so a bare hosted image does not fail with a realm 401
// that the author can only fix by knowing our registry exists.
//
// These assert on the ORDER as well as the fact: the login must happen before
// anything is built, because a build that compiles and images for minutes and
// only then cannot push has spent all of it to learn something knowable at the
// start.

// errBuildSpy marks the first real build step. Returned from the storage
// admission check, which runBuild calls immediately after the push plan is
// resolved and before any compile, docker build or push.
var errBuildSpy = errors.New("build-spy: the build reached its first step")

// spyOnFirstBuildStep makes the build stop at its first step and records that
// it got there. A SEAM AND NOT A CLOCK: "did the build start" has to be
// observable to assert "the login happened before it", and the storage check
// is the earliest step every lane runs.
func spyOnFirstBuildStep(t *testing.T) *bool {
	t.Helper()
	reached := false
	prev := checkBuildStorageFn
	checkBuildStorageFn = func(string) error {
		reached = true
		return errBuildSpy
	}
	t.Cleanup(func() { checkBuildStorageFn = prev })
	return &reached
}

// mustLoadProjectConfig reads the forge.yaml planProject just wrote. The real
// loader, so these tests exercise the same config path a build does.
func mustLoadProjectConfig(t *testing.T) *config.ProjectConfig {
	t.Helper()
	cfg, err := loadProjectConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestAutoLogin_HostedPushLogsInOncePerHostPerProcess is the memoization,
// which is what keeps a multi-artifact build from writing the same credential
// store entry N times.
//
// It drives renderBuildInputs twice, which is what a build that pushes images
// and then a bundle and then a site release effectively does: each push path
// reaches the credential store on its own. One login is the correct answer for
// all of them, because the docker store is keyed by host.
//
// SABOTAGE: delete the platformLogins Load/Store in loginToPlatformRegistry
// and this goes red with two calls. The PR body records the run.
func TestAutoLogin_HostedPushLogsInOncePerHostPerProcess(t *testing.T) {
	planProject(t, hostedPlatformFixture("", "api"))
	isolatedCredentials(t)
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_ci")
	calls := stubDockerLoginArgs(t)

	for i := 0; i < 2; i++ {
		opts := buildOptions{env: "prod", push: true, buildDocker: true, outputDir: "bin", buildTarget: "all"}
		if _, _, err := renderBuildInputs(t.Context(), mustLoadProjectConfig(t), &opts); err != nil {
			t.Fatalf("resolve build inputs (pass %d): %v", i+1, err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("docker login calls = %d (%+v), want exactly one per host per process", len(*calls), *calls)
	}
	if strings.TrimSpace((*calls)[0].password) != "rlat_ci" {
		t.Errorf("stdin carried %q, want the control-plane credential", (*calls)[0].password)
	}
}

// TestAutoLogin_NoCredentialFailsBeforeAnythingIsBuilt: the whole value of
// hooking this at plan-resolution time rather than at the push.
func TestAutoLogin_NoCredentialFailsBeforeAnythingIsBuilt(t *testing.T) {
	planProject(t, hostedPlatformFixture("", "api"))
	isolatedCredentials(t) // no token env, no credentials file entry
	calls := stubDockerLoginArgs(t)
	reached := spyOnFirstBuildStep(t)

	err := runBuild(t.Context(), buildOptions{
		env: "prod", push: true, buildDocker: true, outputDir: "bin", buildTarget: "all", parallel: true,
	})
	if err == nil {
		t.Fatal("a hosted --push with no resolvable credential must fail")
	}
	if *reached {
		t.Error("the build started before the credential was checked — a build that fails at the push has wasted every step before it")
	}
	if errors.Is(err, errBuildSpy) {
		t.Fatalf("the build ran instead of failing on the credential: %v", err)
	}
	// cloud.ResolveCredential's own runbook, surfaced rather than
	// reworded: it names the three remedies in the order forge tries them.
	for _, want := range []string{"forge login", "FORGE_CONTROL_PLANE_TOKEN", "--token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure must name %q as a remedy; got:\n%v", want, err)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("no credential resolved: nothing to log in with; calls = %+v", *calls)
	}
}

// TestAutoLogin_LocalRegistryNeverLogsIn: a k3d-local registry takes no
// credentials, so a dev env that happens to declare a control plane must not
// be sent looking for a token it has no use for.
func TestAutoLogin_LocalRegistryNeverLogsIn(t *testing.T) {
	planProject(t, hostedPlatformFixture("localhost:5050", "localhost:5050/api"))
	isolatedCredentials(t) // deliberately no credential at all
	calls := stubDockerLoginArgs(t)

	opts := buildOptions{env: "dev", push: true, buildDocker: true, outputDir: "bin", buildTarget: "all"}
	if _, _, err := renderBuildInputs(t.Context(), mustLoadProjectConfig(t), &opts); err != nil {
		t.Fatalf("a local registry must need no credential: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a local registry needs no login; calls = %+v", *calls)
	}
}

// TestAutoLogin_ForeignRegistryIsNotOurs: an env whose images all go to the
// author's own registry resolves no control-plane credential and makes no
// subprocess call. forge has no credential for that host and must not pretend
// to — `forge registry login` with the flags, or a plain `docker login`, is
// still how it is reached.
func TestAutoLogin_ForeignRegistryIsNotOurs(t *testing.T) {
	planProject(t, declaredRegistryFixture)
	isolatedCredentials(t)
	calls := stubDockerLoginArgs(t)

	opts := buildOptions{env: "prod", push: true, buildDocker: true, outputDir: "bin", buildTarget: "all"}
	if _, _, err := renderBuildInputs(t.Context(), mustLoadProjectConfig(t), &opts); err != nil {
		t.Fatalf("a foreign registry must not make the build resolve a platform credential: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("forge holds no credential for a foreign registry; calls = %+v", *calls)
	}
}

// TestAutoLogin_NoPushNoLogin: a build that pushes nothing authenticates
// nothing, which is what keeps `forge build` usable offline.
func TestAutoLogin_NoPushNoLogin(t *testing.T) {
	planProject(t, hostedPlatformFixture("", "api"))
	isolatedCredentials(t)
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_ci")
	calls := stubDockerLoginArgs(t)

	opts := buildOptions{env: "prod", buildDocker: true, outputDir: "bin", buildTarget: "all"}
	if _, _, err := renderBuildInputs(t.Context(), mustLoadProjectConfig(t), &opts); err != nil {
		t.Fatalf("a non-pushing build: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a build that pushes nothing must authenticate nothing; calls = %+v", *calls)
	}
}

// TestAutoLogin_PlanResolvesNoCredential: `--plan` preflights and writes
// nothing, so it must stay runnable on a laptop with no login. A dry run that
// failed for want of a credential could not gate a PR from a fresh checkout.
func TestAutoLogin_PlanResolvesNoCredential(t *testing.T) {
	planProject(t, hostedPlatformFixture("", "api"))
	isolatedCredentials(t) // no credential anywhere
	calls := stubDockerLoginArgs(t)

	opts := buildOptions{env: "prod", push: true, plan: true, buildDocker: true, outputDir: "bin", buildTarget: "all"}
	if _, _, err := renderBuildInputs(t.Context(), mustLoadProjectConfig(t), &opts); err != nil {
		t.Fatalf("--plan must not need a credential: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("--plan writes nothing and must log in to nothing; calls = %+v", *calls)
	}
}
