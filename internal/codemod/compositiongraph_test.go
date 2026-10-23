package codemod

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// copyLegacy copies testdata/legacy (a pre-graph scaffold) into a temp dir.
func copyLegacy(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", "legacy")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "go.mod.txt" {
			rel = "go.mod"
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	return dst
}

// snapshot reads every file under dir.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return out
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestCompositionGraph_ConvertsTheScaffoldedComposition(t *testing.T) {
	dir := copyLegacy(t)
	if codegen.GraphComposition(dir) {
		t.Fatal("fixture is not on the legacy composition")
	}

	before := snapshot(t, dir)
	dry, err := runCompositionGraph(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Blocked) > 0 {
		t.Fatalf("the scaffolded composition was refused:\n%s", strings.Join(dry.Blocked, "\n"))
	}
	if len(snapshot(t, dir)) != len(before) || snapshot(t, dir)["internal/app/compose.go"] != before["internal/app/compose.go"] {
		t.Fatal("--dry-run wrote")
	}

	rep, err := runCompositionGraph(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed() {
		t.Fatal("nothing rewritten")
	}
	if !codegen.GraphComposition(dir) {
		t.Fatal("still on the legacy composition after the codemod")
	}

	for _, gone := range []string{
		"internal/app/providers.go", "internal/app/lifecycle.go", "internal/app/mounts_services_gen.go",
		"cmd/app/cmd/serve.go", "cmd/app/cmd/server.go", "cmd/app/cmd/services", "cmd/app/cmd/workers", "cmd/app/cmd/operators",
	} {
		if exists(dir, gone) {
			t.Errorf("%s survived", gone)
		}
	}
	// providers.go and serve.go are re-scaffolded by generate: their birth
	// records are dropped. lifecycle.go is retired: its record stays, so
	// generate leaves it deleted.
	for rel, want := range map[string]bool{"internal/app/providers.go": false, "cmd/app/cmd/serve.go": false, "internal/app/lifecycle.go": true} {
		if got := checksums.ScaffoldRecorded(dir, rel); got != want {
			t.Errorf("ScaffoldRecorded(%s) = %v, want %v", rel, got, want)
		}
	}

	compose := snapshot(t, dir)["internal/app/compose.go"]
	for _, want := range []string{
		"func (g *Graph) newTasks() (*tasks.Service, error) {\n\treturn tasks.New(tasks.Deps{\n\t\tDB:     g.ORM(),",
		"func (g *Graph) newTicker() (*ticker.Worker, error) {",
		"}), nil\n}",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.go lacks %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "infra.") || strings.Contains(compose, "*Components") {
		t.Errorf("compose.go still names the legacy composition:\n%s", compose)
	}

	files := snapshot(t, dir)
	for rel, wants := range map[string][]string{
		"cmd/app/cmd/root.go":    {"func newRootCmd(deps Deps) *cobra.Command {", "root.AddCommand(rootCommands(deps)...)", "func Execute() {", "root := newRootCmd(deps)\n"},
		"cmd/app/main.go":        {"cmd.Execute()\n"},
		"internal/app/auth.go":   {"func APIKeyValidator(_ *DB) auth.KeyValidator {"},
		"e2e/tasks/main_test.go": {`exec.CommandContext(ctx, "./bin/app", "server")`},
	} {
		for _, w := range wants {
			if !strings.Contains(files[rel], w) {
				t.Errorf("%s lacks %q", rel, w)
			}
		}
	}
	if strings.Contains(files["cmd/app/cmd/root.go"], "groupCmds") || strings.Contains(files["cmd/app/main.go"], "cmd/app/cmd/services") {
		t.Error("root.go / main.go still name the retired command groups")
	}
	if !strings.Contains(strings.Join(rep.Next, "\n"), "`app tasks`, `app ticker`") {
		t.Errorf("Next does not name the retired subcommands:\n%s", strings.Join(rep.Next, "\n"))
	}

	again, err := runCompositionGraph(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() || len(again.Blocked) > 0 {
		t.Errorf("second run: %+v", again)
	}
}

// A compose.go holding only the conventional Logger/Config values is
// re-scaffolded rather than translated, so a stock project converges on
// exactly what a new one scaffolds.
func TestCompositionGraph_ConventionalComposeIsRescaffolded(t *testing.T) {
	dir := copyLegacy(t)
	path := filepath.Join(dir, "internal", "app", "compose.go")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "\t\tDB:     infra.ORM,\n", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCompositionGraph(dir, Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "internal/app/compose.go") || checksums.ScaffoldRecorded(dir, "internal/app/compose.go") {
		t.Error("a conventional compose.go must be deleted with its birth record dropped")
	}
}

// Shapes only the author can map onto roots and providers are listed with
// their file:line, and nothing is written.
func TestCompositionGraph_RefusesBespokeShapesAndWritesNothing(t *testing.T) {
	dir := copyLegacy(t)
	edit := func(rel, old, new string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("%s lacks %q", rel, old)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit("internal/app/providers.go", "\tORM *orm.Client\n", "\tORM *orm.Client\n\tLease *orm.Client\n")
	edit("cmd/app/cmd/serve.go", "type ServeSpec struct {\n", "type ServeSpec struct {\n\tWorkerLeases bool\n")
	edit("internal/app/lifecycle.go", `return lifecyclekit.WrapWorker("ticker", c.Ticker)`, `return lifecyclekit.Singleton(nil, lifecyclekit.WrapWorker("ticker", c.Ticker))`)
	if err := os.WriteFile(filepath.Join(dir, "internal", "app", "wire.go"), []byte("package app\n\nfunc leaseOf(infra *Infra) any { return infra.Lease }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	rep, err := runCompositionGraph(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	blocked := strings.Join(rep.Blocked, "\n")
	for _, want := range []string{
		"internal/app/providers.go:", "Infra.Lease",
		"cmd/app/cmd/serve.go:", "ServeSpec.WorkerLeases",
		"internal/app/lifecycle.go:", "(Components).WorkerTicker",
		"internal/app/wire.go:3: Infra is retired",
	} {
		if !strings.Contains(blocked, want) {
			t.Errorf("Blocked lacks %q:\n%s", want, blocked)
		}
	}
	if rep.Changed() {
		t.Errorf("a refused conversion rewrote %q", rep.Rewrites)
	}
	after := snapshot(t, dir)
	if len(after) != len(before) {
		t.Fatalf("files changed: %d → %d", len(before), len(after))
	}
	for rel, b := range before {
		if after[rel] != b {
			t.Errorf("%s changed", rel)
		}
	}
}

// A project with no internal/app, or already on the graph, has nothing for
// the codemod.
func TestCompositionGraph_NothingToDo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runCompositionGraph(dir, Options{Write: true})
	if err != nil || rep.Changed() || len(rep.Blocked) > 0 || len(rep.Next) > 0 {
		t.Fatalf("no internal/app, no binary: %+v, %v", rep, err)
	}

	// A hand-composed binary (reliant's shape: no internal/app, its cobra
	// tree outside cmd/<bin>/cmd) is told how to adopt the graph, not that
	// it already has.
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "tool", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, src := range map[string]string{"cmd/tool/main.go": "package main\n\nfunc main() {}\n", "cmd/tool/commands/root.go": "package commands\n", "cmd/aaa/main.go": "package main\n\nfunc main() {}\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err = runCompositionGraph(dir, Options{Write: true})
	if err != nil || rep.Changed() {
		t.Fatalf("hand-composed: %+v, %v", rep, err)
	}
	next := strings.Join(rep.Next, "\n")
	if !strings.Contains(next, "composes its processes by hand") || !strings.Contains(next, "tool's is cmd/tool/commands, so move it to cmd/tool/cmd") {
		t.Errorf("hand-composed Next:\n%s", next)
	}
}
