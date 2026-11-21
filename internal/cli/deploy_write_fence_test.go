package cli

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// Every preview flag fences cluster writes; a real deploy does not.
func TestFenceDeployPreview_EveryPreviewIsFenced(t *testing.T) {
	previews := map[string]deployCmdFlags{
		"--plan-only": {promote: promoteCmdFlags{planOnly: true}},
		"--plan":      {promote: promoteCmdFlags{plan: true}},
		"--dry-run":   {dryRun: true},
		"--explain":   {explain: true},
	}
	for name, f := range previews {
		if _, fenced := cluster.WritesFenced(fenceDeployPreview(context.Background(), f)); !fenced {
			t.Errorf("%s is not fenced: a preview could write to a cluster", name)
		}
	}
	for name, f := range map[string]deployCmdFlags{
		"deploy":        {},
		"--yes":         {promote: promoteCmdFlags{yes: true}},
		"--approve":     {promote: promoteCmdFlags{approve: "sha256:abc"}},
		"--target only": {targets: []string{"api"}},
	} {
		if _, fenced := cluster.WritesFenced(fenceDeployPreview(context.Background(), f)); fenced {
			t.Errorf("%s is fenced: a real deploy could not apply", name)
		}
	}
}

// The pre-record preflight runs on every release deploy, the approving one
// included, before the promotion is written. It must reach the client deploy
// as a dry run AND under the write fence, so that a stage which ignores the
// dry-run flag is refused instead of applying (88855ea7).
func TestPreflightBeforeRecord_RunsFencedDryRun(t *testing.T) {
	calls := 0
	old := runPromoteClientDeploy
	runPromoteClientDeploy = func(ctx context.Context, _ string, opts deployOptions) error {
		calls++
		if !opts.dryRun || !opts.preflightOnly {
			t.Errorf("preflight ran a client deploy with dryRun=%v preflightOnly=%v", opts.dryRun, opts.preflightOnly)
		}
		if _, fenced := cluster.WritesFenced(ctx); !fenced {
			t.Error("preflight ran a client deploy with cluster writes unfenced")
		}
		// What a stage that forgot the dry-run flag would do: it is refused,
		// and no kubectl process is started.
		if err := cluster.KubectlCommand(ctx, "--context", "prod", "apply", "--server-side", "-f", "-").Run(); err == nil {
			t.Error("an apply under the preflight's context was allowed")
		}
		return nil
	}
	t.Cleanup(func() { runPromoteClientDeploy = old })
	for _, nonBinding := range []bool{true, false} {
		if _, err := preflightBeforeRecord(context.Background(), "prod", "v1.2.3", envLedger{}, promoteFollowOptions{jsonOut: true}, nonBinding); err != nil {
			t.Fatalf("preflight (nonBinding=%v): %v", nonBinding, err)
		}
	}
	if calls != 2 {
		t.Fatalf("client deploy ran %d times, want 2", calls)
	}
}

// THE FENCE IS ONLY A GUARANTEE IF NOTHING GOES AROUND IT. Every kubectl
// process the CLI and the cluster package start is built by
// cluster.KubectlCommand; a direct exec.Command("kubectl", …) would be a write
// path no preview can fence.
func TestKubectlIsBuiltOnlyThroughTheFence(t *testing.T) {
	for _, dir := range []string{".", filepath.Join("..", "cluster")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			if filepath.Base(dir) == "cluster" && name == "write_fence.go" {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
					return true
				}
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, _ := strconv.Unquote(lit.Value); v == "kubectl" {
							t.Errorf("%s: kubectl is exec'd directly; build it with cluster.KubectlCommand so the write fence applies",
								fset.Position(call.Pos()))
						}
						break
					}
				}
				return true
			})
		}
	}
}
