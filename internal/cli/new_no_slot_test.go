package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

func TestBootstrapPipelineFlags(t *testing.T) {
	skip := bootstrapPipelineFlags(false)
	if !skip.SkipValidate || skip.SkipValidateNote == "" {
		t.Errorf("default bootstrap = %+v, want the compile check skipped and said so", skip)
	}
	if skip.SkipPreChecks || skip.SkipConfigCheck || skip.Force {
		t.Errorf("default bootstrap = %+v: only the compile tail may be skipped", skip)
	}
	if got := bootstrapPipelineFlags(true); got != (pipelineFlags{}) {
		t.Errorf("--verify bootstrap = %+v, want the full pipeline", got)
	}
}

func TestNewCmdHasVerifyFlag(t *testing.T) {
	f := newNewCmd().Flags().Lookup("verify")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("project new --verify = %v, want a bool flag defaulting to false", f)
	}
}

// TestRunNew_TakesNoHeavySlot pins that `forge project new` never queues for a
// host heavy slot: the only slot of a private pool is held by the test for
// the whole scaffold, so any step that acquired one would block until the
// deadline. Scaffolding is an install step the user waits on, not a build lane.
func TestRunNew_TakesNoHeavySlot(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a real service project with the proto bootstrap (slow); runs in full mode and CI")
	}
	requireProtoToolchain(t)

	t.Setenv(hostslot.EnvDir, t.TempDir())
	t.Setenv("FORGE_HEAVY_SLOTS", "1")
	t.Setenv(hostslot.EnvHeld, "")
	t.Setenv("GIT_AUTHOR_NAME", "forge-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "forge-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "forge-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "forge-test@example.com")

	held, err := hostslot.Acquire(t.Context(), hostslot.Heavy, hostslot.Options{Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	if held.Reentrant() != "" || held.Unlimited() {
		t.Fatalf("the test did not take the only slot (reentrant=%q unlimited=%v); the pool cannot prove anything", held.Reentrant(), held.Unlimited())
	}
	t.Cleanup(func() { _ = held.Release() })

	buildinfo.SetDevBuild(true)
	t.Cleanup(buildinfo.ClearDevBuild)
	prevRoot := buildinfo.DevForgeRoot
	forgeRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	buildinfo.DevForgeRoot = forgeRoot
	t.Cleanup(func() { buildinfo.DevForgeRoot = prevRoot })

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runNew(ctx, "demo", t.TempDir(), "example.com/demo", config.ProjectKindService,
			[]string{"notes"}, nil, "", false, false, nil, "", true, "local", "", false, true, false, false, newFrontendOptions{})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runNew: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("runNew did not return while the only heavy slot was held: `project new` is waiting for a slot")
	}
	if rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Env: os.Environ()}); err != nil || len(rep.Waiting) != 0 {
		t.Errorf("heavy slot queue after project new = %+v (err %v), want nobody waiting", rep.Waiting, err)
	}
}
