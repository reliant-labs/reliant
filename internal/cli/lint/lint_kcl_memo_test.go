package lint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// The deploy-tree lanes render each env once per run: a second lane asking
// for the same render — through kclrender.Run's arguments or through
// cluster.RenderManifests, which pdb-blocks-drain and flux-safe use with the
// arguments secrets and telemetry pass — gets the first render back. They
// used to render every env each, 21 evaluations for 6 distinct renders on a
// three-env project (Ranger build, friction #10).
func TestKCLRendersAreSharedAcrossLanesInOneRun(t *testing.T) {
	root := t.TempDir()
	mainK := filepath.Join(root, "deploy", "kcl", "dev", "main.k")
	if err := os.MkdirAll(filepath.Dir(mainK), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainK, []byte("output = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	fake := func(workDir, source string, dArgs []string) ([]byte, error) {
		calls[source]++
		if source == filepath.Dir(mainK) {
			return []byte(`{"output":{}}`), nil
		}
		return nil, errors.New("not a manifest render")
	}
	ctx := withKCLRenderMemoUsing(context.Background(), fake)
	if again := withKCLRenderMemo(ctx); again != ctx {
		t.Error("a run that already carries a memo got a second one")
	}

	// pdb-blocks-drain / flux-safe: through cluster.RenderManifests.
	if _, err := cluster.RenderManifests(ctx, mainK, "lint", "", "dev", nil, nil); err == nil {
		t.Fatal("the fake render should not extract")
	}
	// secrets / telemetry: the same render through kclrender.Run's arguments.
	args := []string{"image_tag=" + strconv.Quote("lint"), "namespace=" + strconv.Quote(""), "env=" + strconv.Quote("dev")}
	if _, err := lintKCLRun(ctx, root, mainK, args); err == nil {
		t.Error("a remembered render lost its error")
	}
	if calls[mainK] != 1 {
		t.Errorf("the manifest render ran %d times, want 1", calls[mainK])
	}

	// flux-safe / identity-share / static-export: the env-directory render.
	envDir := filepath.Dir(mainK)
	envArgs := []string{"env=" + strconv.Quote("dev")}
	first, err := lintKCLRun(ctx, root, envDir, envArgs)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X' // a caller's copy is its own
	second, err := lintKCLRun(ctx, root, envDir, envArgs)
	if err != nil || string(second) != `{"output":{}}` {
		t.Errorf("second render = %q, %v; want the first, unmodified", second, err)
	}
	if calls[envDir] != 1 {
		t.Errorf("the env-directory render ran %d times, want 1", calls[envDir])
	}

	// Another run is another tree: nothing is shared across runs.
	if _, err := lintKCLRun(withKCLRenderMemoUsing(context.Background(), fake), root, envDir, envArgs); err != nil {
		t.Fatal(err)
	}
	if calls[envDir] != 2 {
		t.Errorf("a new run reused the previous run's render (calls = %d)", calls[envDir])
	}
}
