package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// observabilityTestCtx is a pipeline context for the observability step on
// an empty project whose env renders were already read (failed is the envs
// that did not render). cs carries the checksum record across runs, as the
// project's .forge state does between real generate runs.
func observabilityTestCtx(dir string, cs *checksums.FileChecksums, failed []envRenderFailure) *pipelineContext {
	return &pipelineContext{
		Cfg:           &config.ProjectConfig{Name: "demo"},
		ProjectDir:    dir,
		AbsPath:       dir,
		Checksums:     cs,
		graphRootsSet: true,
		renderedRead:  true,
		rendered:      renderedEnvironments{failed: failed},
	}
}

// snapshotObservability reads every file under deploy/observability/.
func snapshotObservability(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(dir, "deploy", "observability")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	return out
}

// captureStderrE runs fn with os.Stderr redirected and returns what it wrote.
func captureStderrE(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := fn()
	os.Stderr = old
	_ = w.Close()
	return <-done, ferr
}

// operatorRenderFailure is what an env render returns after `forge scaffold
// operator` in a project whose prod declares no cluster: the subprocess error,
// then kcl's colourised, source-annotated diagnostic.
const operatorRenderFailure = "evaluate KCL in /tmp/p/addverbs: exit status 1\n" +
	"Error: kpm run /tmp/p/addverbs/deploy/kcl/prod: EvaluationError\n" +
	"  \x1b[1;38;5;12m-->\x1b[0m /tmp/p/addverbs/deploy/kcl/base/env.k:69:1\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\n" +
	"\x1b[1;38;5;12m69 |\x1b[0m         + (\" — the hosted platform refuses ${w.kind}s\" if w.kind in [\"cron\", \"operator\"] else \"\")\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\x1b[1;38;5;9m \x1b[0m \x1b[1;38;5;9mworkload 'fleet' is bound to _on_cluster, but deploy/kcl/prod/main.k declares no `_cluster`: declare the cluster you operate there\x1b[0m\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\n"

// The cause of a failed render is kcl's diagnostic message, not the
// subprocess's "exit status 1" nor the annotated source line above it.
func TestRenderFailureCause_KCLDiagnostic(t *testing.T) {
	want := "workload 'fleet' is bound to _on_cluster, but deploy/kcl/prod/main.k declares no `_cluster`: declare the cluster you operate there (at /tmp/p/addverbs/deploy/kcl/base/env.k:69:1)"
	if got := renderFailureCause(errors.New(operatorRenderFailure)); got != want {
		t.Errorf("renderFailureCause = %q, want %q", got, want)
	}
	if got := renderFailureCause(errors.New("evaluate KCL in /p: exit status 1")); got != "evaluate KCL in /p: exit status 1" {
		t.Errorf("an error with no diagnostic must fall back to its first line, got %q", got)
	}
}

// An environment that does not render must not shrink or delete the
// observability outputs (forge bug report f9a3c9a1), and must not fail the
// run either: `forge scaffold operator` leaves each hosted env that declares
// no cluster refusing to render, on purpose. The outputs stay byte-identical,
// the stale-file sweep keeps them, the warning names the env and its cause,
// and the step returns nil with no soft failure recorded.
func TestObservabilityDashboards_EnvRenderFailureKeepsOutputs(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(checksums.ResetSkipWrite)

	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	// A run in which every env rendered writes the outputs.
	checksums.ResetSkipWrite()
	if err := stepObservabilityDashboards(observabilityTestCtx(dir, cs, nil)); err != nil {
		t.Fatalf("observability step with every env rendered: %v", err)
	}
	before := snapshotObservability(t, dir)
	if len(before) == 0 {
		t.Fatal("the rendered run wrote nothing under deploy/observability/; the keep check below would be vacuous")
	}

	// The next run: prod does not render.
	checksums.ResetSkipWrite()
	ctx := observabilityTestCtx(dir, cs, []envRenderFailure{{Env: "prod", Err: errors.New(operatorRenderFailure)}})
	stderr, err := captureStderrE(t, func() error { return stepObservabilityDashboards(ctx) })
	if err != nil {
		t.Fatalf("observability step failed on an env that did not render; want a warning and nil: %v", err)
	}
	if len(ctx.SoftFailures) != 0 {
		t.Errorf("the kept outputs were recorded as a failed step %v; the run would exit non-zero", ctx.SoftFailures)
	}
	for _, want := range []string{
		"kept the",
		"deploy/kcl/prod",
		"workload 'fleet' is bound to _on_cluster",
		"declares no `_cluster`",
		"declare the cluster a workload bound `_on_cluster` needs",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning does not say %q:\n%s", want, stderr)
		}
	}

	// The stale-file sweep runs after the step; it must keep every output.
	sweep, err := cleanupStaleArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Removed) != 0 {
		t.Errorf("the stale-file sweep removed kept observability outputs: %v", sweep.Removed)
	}

	after := snapshotObservability(t, dir)
	if len(after) != len(before) {
		t.Errorf("deploy/observability/ holds %d file(s) after the failed render, %d before", len(after), len(before))
	}
	for rel, want := range before {
		if got, ok := after[rel]; !ok {
			t.Errorf("%s was deleted", rel)
		} else if got != want {
			t.Errorf("%s changed although an env did not render", rel)
		}
	}
}

// With every env rendered the step regenerates as before: a missing output
// is written again, and no "kept" warning is printed.
func TestObservabilityDashboards_RenderedEnvsRegenerate(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(checksums.ResetSkipWrite)

	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	checksums.ResetSkipWrite()
	if err := stepObservabilityDashboards(observabilityTestCtx(dir, cs, nil)); err != nil {
		t.Fatal(err)
	}
	before := snapshotObservability(t, dir)
	var rel string
	for r := range before {
		if strings.HasSuffix(r, "_gen.json") {
			rel = r
			break
		}
	}
	if rel == "" {
		t.Fatalf("no _gen.json output to make stale among %d file(s)", len(before))
	}
	// Remove one output: a run with every env rendered must write it again.
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}

	checksums.ResetSkipWrite()
	stderr, err := captureStderrE(t, func() error {
		return stepObservabilityDashboards(observabilityTestCtx(dir, cs, nil))
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "kept the") {
		t.Errorf("every env rendered, but the step kept its outputs:\n%s", stderr)
	}
	after := snapshotObservability(t, dir)
	if after[rel] != before[rel] {
		t.Errorf("%s was not regenerated with every env rendered", rel)
	}
}
