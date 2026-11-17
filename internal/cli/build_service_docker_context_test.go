package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestServiceDockerBuildArgs_ContextDefaultsToProjectRoot pins the
// compatibility half of DockerBuild.context: an unset context must still send
// the project root, so every DockerBuild that existed before the field does
// builds byte-identically.
func TestServiceDockerBuildArgs_ContextDefaultsToProjectRoot(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}

	args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "", "")

	if got := args[len(args)-1]; got != "." {
		t.Errorf("unset context must build with the project root; last arg = %q, args=%v", got, args)
	}
	if !argsHave(args, "-f", "Dockerfile") {
		t.Errorf("-f Dockerfile missing; args=%v", args)
	}
}

// TestServiceDockerBuildArgs_ContextIsSent is the load-bearing regression for
// the internal-console break: a frontend Dockerfile written to run from its own
// directory (`COPY package.json ./`) builds with the repo root as its context
// and fails with `failed to compute cache key: "/package.json": not found`.
// A declared context must reach the docker argv as the main context.
func TestServiceDockerBuildArgs_ContextIsSent(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}
	d := &DockerBuild{
		Dockerfile: "frontends/internal-console/Dockerfile",
		Context:    "frontends/internal-console",
	}

	args, _ := serviceDockerBuildArgs(cfg, "internal-console", d.Dockerfile, d, buildOptions{}, "", "")

	if got := args[len(args)-1]; got != "frontends/internal-console" {
		t.Errorf("declared context not sent as the main context; last arg = %q, args=%v", got, args)
	}
	// The Dockerfile stays as declared — it is resolved against the cwd
	// (the project root), NOT against the context, and docker permits -f
	// outside the context.
	if !argsHave(args, "-f", "frontends/internal-console/Dockerfile") {
		t.Errorf("-f must stay project-root-relative; args=%v", args)
	}
}

// TestCheckServiceDockerContext covers the validation forge runs at plan time
// and before the real build. The failure it replaces — docker's "failed to
// compute cache key" — names the Dockerfile, not the context, so it reads as a
// Dockerfile bug; this must name the context.
func TestCheckServiceDockerContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "frontends", "internal-console"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notadir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("unset is always fine", func(t *testing.T) {
		if err := checkServiceDockerContext(root, "svc", &DockerBuild{}); err != nil {
			t.Errorf("unset context must not be refused: %v", err)
		}
		if err := checkServiceDockerContext(root, "svc", nil); err != nil {
			t.Errorf("nil DockerBuild must not be refused: %v", err)
		}
	})

	t.Run("an existing directory passes", func(t *testing.T) {
		d := &DockerBuild{Context: "frontends/internal-console"}
		if err := checkServiceDockerContext(root, "internal-console", d); err != nil {
			t.Errorf("an existing in-project directory must pass: %v", err)
		}
	})

	t.Run("a Dockerfile outside the context is allowed", func(t *testing.T) {
		// docker permits -f outside the context, and forge does not narrow it.
		d := &DockerBuild{Dockerfile: "docker/ic.Dockerfile", Context: "frontends/internal-console"}
		if err := checkServiceDockerContext(root, "internal-console", d); err != nil {
			t.Errorf("a Dockerfile outside the context must be allowed: %v", err)
		}
	})

	t.Run("a missing directory is refused by name", func(t *testing.T) {
		err := checkServiceDockerContext(root, "internal-console", &DockerBuild{Context: "frontends/nope"})
		if err == nil {
			t.Fatal("a context that does not exist must be refused")
		}
		for _, want := range []string{"internal-console", "frontends/nope", "does not exist"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must mention %q; got: %v", want, err)
			}
		}
	})

	t.Run("a file is refused", func(t *testing.T) {
		err := checkServiceDockerContext(root, "svc", &DockerBuild{Context: "notadir"})
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("a file context must be refused as not-a-directory; got: %v", err)
		}
	})

	t.Run("escaping the project root is refused", func(t *testing.T) {
		err := checkServiceDockerContext(root, "svc", &DockerBuild{Context: "../sibling"})
		if err == nil || !strings.Contains(err.Error(), "escapes the project root") {
			t.Errorf("a context outside the project must be refused; got: %v", err)
		}
	})

	t.Run("an absolute context is refused", func(t *testing.T) {
		err := checkServiceDockerContext(root, "svc", &DockerBuild{Context: root})
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("an absolute context must be refused; got: %v", err)
		}
	})
}

// TestPlanKCLDockerRemote_ShowsContext: `--plan` printed `docker build -f
// frontends/internal-console/Dockerfile` with NO context, which is exactly why
// the repo-root-context break reached prod unnoticed — the plan could not
// disagree with the build because it never showed the disputed argument.
func TestPlanKCLDockerRemote_ShowsContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "frontends", "internal-console"), 0o755); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(root, "frontends", "internal-console", "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := planInputs{
		cfg:        &config.ProjectConfig{Name: "control-plane"},
		projectDir: root,
		entities: &KCLEntities{Workloads: []WorkloadEntity{
			{Name: "internal-console", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{
				Dockerfile: "frontends/internal-console/Dockerfile",
				Context:    "frontends/internal-console",
			}}},
			{Name: "api", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{}}},
		}},
	}

	steps := planKCLDockerRemote(in)
	if len(steps) != 2 {
		t.Fatalf("expected a step per docker workload, got %d: %+v", len(steps), steps)
	}
	if want := "docker build -f frontends/internal-console/Dockerfile frontends/internal-console"; steps[0].what != want {
		t.Errorf("plan must print the context;\n got %q\nwant %q", steps[0].what, want)
	}
	if steps[0].problem != "" {
		t.Errorf("a valid context must not be a plan problem: %s", steps[0].problem)
	}
	if want := "docker build -f Dockerfile ."; steps[1].what != want {
		t.Errorf("an unset context must plan as the project root;\n got %q\nwant %q", steps[1].what, want)
	}
}

// TestPlanKCLDockerRemote_RefusesMissingContext: the plan is the PR-time gate,
// so a context that does not exist must be a plan PROBLEM (which fails
// `--plan`), not a surprise eleven minutes into a release cut.
func TestPlanKCLDockerRemote_RefusesMissingContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	steps := planKCLDockerRemote(planInputs{
		cfg:        &config.ProjectConfig{Name: "control-plane"},
		projectDir: root,
		entities: &KCLEntities{Workloads: []WorkloadEntity{
			{Name: "ic", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{Context: "frontends/nope"}}},
		}},
	})

	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}
	if !strings.Contains(steps[0].problem, "frontends/nope") {
		t.Errorf("a missing context must be a plan problem naming it; got problem=%q", steps[0].problem)
	}
}
