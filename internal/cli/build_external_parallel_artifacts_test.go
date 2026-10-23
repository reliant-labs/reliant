package cli

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/buildtarget"
)

// The shared artifact and another image must actually overlap, not merely run
// once in the parallel branch. Duplicate consumers may never enter the runner.
func TestBuildExternalSharedArtifactParallelDistinct(t *testing.T) {
	builds, _, _ := mockExternalArtifactBuild(t, false)
	old := externalShellBuild
	entered := make(chan string, 4)
	release := make(chan struct{})
	externalShellBuild = func(ctx context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		entered <- spec.Image
		select {
		case <-release:
		case <-ctx.Done():
			return buildtarget.BuildResult{Err: ctx.Err()}
		}
		return old(ctx, spec)
	}
	services := sharedExternalConsumers()
	other := shellSvc("api", "registry.example/distinct", "different command", "", nil)
	// Different consumer name, but keep the mocked capture's deterministic
	// owner assertion local to the shared artifact.
	other.Name = "z-other"
	externalSourceCapture = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	services = append(services, other)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan []buildResult, 1)
	project := t.TempDir()
	go func() {
		done <- buildExternalServices(ctx, services, buildOptions{env: "prod", parallel: true}, "v1", project)
	}()
	seen := map[string]bool{}
	for range 2 {
		select {
		case image := <-entered:
			if seen[image] {
				t.Errorf("duplicate concurrent build for %s", image)
			}
			seen[image] = true
		case <-ctx.Done():
			t.Error("distinct artifacts did not execute concurrently")
		}
	}
	close(release)
	results := <-done
	if len(results) != 4 || builds.Load() != 2 {
		t.Fatalf("results=%d builds=%d", len(results), builds.Load())
	}
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
	}
}
