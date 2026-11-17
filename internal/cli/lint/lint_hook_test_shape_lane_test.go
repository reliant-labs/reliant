package lint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// TestHookTestShapeRunsInTheConventionsLane pins WHERE the gating
// hook-test-shape rule runs. It used to run only under `--tests`, a lane
// documented as "warnings only", so reliant's generate:check — `forge lint
// --conventions` — never ran it, and a starter row calling mutateAsync on a
// NO_SIDE_EFFECTS read reached vitest (#1508). It now gates in
// `--conventions`, and `--tests` (presence rules, warnings) no longer
// carries it, so a full `forge lint` reports each stale row once.
func TestHookTestShapeRunsInTheConventionsLane(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, "frontends", "web", "src", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("run-service-hooks_gen.ts", "export const useLastRunPerWorkflow = createQueryHook<typeof A, B>(\n")
	write("run-service-hooks.test.tsx", `describe("RunService hooks", () => {
  it("useLastRunPerWorkflow resolves a happy-path response", async () => {
    const { result } = renderHook(() => useLastRunPerWorkflow(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {});
  });
});
`)
	t.Chdir(root)
	// No forge.yaml. Installed here, not inherited: run alone (the flake
	// gate's -run), no other test has registered a loader, and the
	// conventions lane reads project config through it.
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		return nil, ErrProjectConfigNotFound
	})

	conv, gated, err := collectConventionsJSON(forgeconv.LintOptions{})
	if err != nil {
		t.Fatalf("collectConventionsJSON: %v", err)
	}
	var shape []lintJSONFinding
	for _, f := range conv {
		if f.Rule == "forgeconv-frontend-hook-test-shape" {
			shape = append(shape, f)
		}
	}
	if len(shape) != 1 || shape[0].Line != 2 {
		t.Fatalf("--conventions must report the stale row at line 2, got %+v", shape)
	}
	if !gated {
		t.Error("a stale starter row can never pass; the conventions lane must gate on it")
	}

	tests, testsGated, err := collectTestsJSON(".")
	if err != nil {
		t.Fatalf("collectTestsJSON: %v", err)
	}
	for _, f := range tests {
		if f.Rule == "forgeconv-frontend-hook-test-shape" {
			t.Errorf("--tests must not double-report the shape rule: %+v", f)
		}
	}
	if testsGated {
		t.Error("--tests carries only warning-level presence rules now; it must not gate")
	}
}
