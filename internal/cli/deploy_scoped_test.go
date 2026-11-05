package cli

import (
	"os"
	"strings"
	"testing"
)

// A SCOPED deploy with no version is refused, rather than cutting a full
// release to ship part of it (deploy_scoped.go).
//
// These assert on refuseScopedDeployWithoutRelease rather than on
// dispatchDeployCmd, because the dispatcher's next step is a real build-push-cut
// against a real registry. The refusal is the decision under test, and it is
// the ONLY thing between those flags and that build — dispatchDeployCmd calls
// it before runDeployEverything, which TestRefuseScopedDeploy_GuardsTheBuildPath
// below pins.

func TestRefuseScopedDeployWithoutRelease_FrontendsOnly(t *testing.T) {
	t.Parallel()
	err := refuseScopedDeployWithoutRelease("prod", deployCmdFlags{frontendsOnly: true})
	if err == nil {
		t.Fatal("`forge env deploy prod --frontends-only` with no version must be refused: " +
			"it would build and push every backend image and cut a full release to ship the frontend")
	}
	// The message has to name BOTH ways out, because the caller wanted one
	// of them and a refusal that only says "no" sends them to --help.
	for _, want := range []string{
		"forge env deploy prod <version> --frontends-only",
		"forge env deploy prod\n",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q as a way forward:\n%s", want, err)
		}
	}
}

func TestRefuseScopedDeployWithoutRelease_Target(t *testing.T) {
	t.Parallel()
	err := refuseScopedDeployWithoutRelease("staging", deployCmdFlags{targets: []string{"api", "worker"}})
	if err == nil {
		t.Fatal("`forge env deploy staging --target api` with no version must be refused")
	}
	// Every named target is echoed: a caller who passed three and sees one
	// cannot tell whether forge read the others.
	for _, want := range []string{"--target api", "--target worker", "forge env deploy staging <version> --target api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q:\n%s", want, err)
		}
	}
}

// THE FLAGS THAT MUST STILL WORK. The refusal is narrow on purpose: it fires
// only on the combination that cannot be recorded honestly (no version, so a
// release gets cut, plus a scope, so the release would not describe what
// shipped). Everything else keeps working, and a guard that over-fired here
// would break the ordinary deploy.
func TestRefuseScopedDeployWithoutRelease_AllowsEverythingElse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		f    deployCmdFlags
		why  string
	}{
		{"unscoped", deployCmdFlags{}, "the ordinary `forge env deploy <env>`: builds everything, ships everything"},
		{"skip-frontend", deployCmdFlags{skipFrontend: true},
			"the full backend build; the release describes every artifact, only the apply is narrowed"},
		{"empty target list", deployCmdFlags{targets: []string{}},
			"a nil/empty --target is no scope at all — the flag's own default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := refuseScopedDeployWithoutRelease("prod", tc.f); err != nil {
				t.Errorf("must be allowed (%s), got: %v", tc.why, err)
			}
		})
	}
}

// The guard is wired in FRONT of the build, not after it. That placement is
// the entire value: refusing after runDeployEverything had built and pushed
// would cost exactly what the refusal exists to save.
//
// Asserted structurally — the scope check appears in dispatchDeployCmd's
// source before the runDeployEverything call — because the behavioural version
// of this test would have to let a real build start to prove it did not.
func TestRefuseScopedDeploy_GuardsTheBuildPath(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func dispatchDeployCmd(")
	if start < 0 {
		t.Fatal("dispatchDeployCmd not found in deploy.go")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	guard := strings.Index(body, "refuseScopedDeployWithoutRelease(")
	build := strings.Index(body, "runDeployEverything(")
	if guard < 0 {
		t.Fatal("dispatchDeployCmd no longer calls refuseScopedDeployWithoutRelease: " +
			"a scoped versionless deploy would build and push everything to ship part of it")
	}
	if build < 0 {
		t.Fatal("dispatchDeployCmd no longer calls runDeployEverything; re-read this test's premise")
	}
	if guard > build {
		t.Error("the scope guard runs AFTER runDeployEverything, so it refuses a build that already happened")
	}
}
