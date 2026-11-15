package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMain drops a cmd/<bin>/main.go with the given body and returns its path.
func writeMain(t *testing.T, root, bin, content string) string {
	t.Helper()
	dir := filepath.Join(root, "cmd", bin)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, "main.go")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return p
}

const pristineMain = `package main

import (
	"example.com/proj/cmd/proj/cmd"
	"example.com/proj/cmd/proj/cmd/services"
)

func main() {
	cmd.Execute(
		// services
		services.NewBillingCmd,
	)
}
`

// TestWireComponentIntoMain_AppendsCtorAndImport is the core of the wiring
// fix: the constructor lands in the cmd.Execute arg list and the group import
// is present, so the freshly scaffolded service is actually REACHABLE.
func TestWireComponentIntoMain_AppendsCtorAndImport(t *testing.T) {
	root := t.TempDir()
	p := writeMain(t, root, "proj", pristineMain)

	got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd")
	if err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}
	if got != WireApplied {
		t.Fatalf("outcome = %v, want WireApplied", got)
	}

	out := readFile(t, p)
	if !strings.Contains(out, "services.NewOrdersCmd") {
		t.Errorf("main.go did not gain the constructor:\n%s", out)
	}
	// The pre-existing service must survive — this is an append, not a
	// re-derivation.
	if !strings.Contains(out, "services.NewBillingCmd") {
		t.Errorf("main.go lost the pre-existing service:\n%s", out)
	}
	// One argument per line. A multi-line arg list that collapses onto one
	// line the moment forge touches it produces a diff nobody wants to
	// review, in a file whose whole point is that a human reads it.
	if !strings.Contains(out, "\t\tservices.NewOrdersCmd,\n") {
		t.Errorf("the appended argument did not keep the call's one-per-line layout:\n%s", out)
	}
	// Everything the user did not ask to change must be byte-identical.
	// Only whole lines are added; no existing line is rewritten.
	assertOnlyAddedLines(t, pristineMain, out)
	assertParses(t, "main.go", out)
}

// assertOnlyAddedLines fails unless `after` is `before` with lines INSERTED —
// every original line still present, in order, unmodified. This is the
// mechanical statement of "main.go is yours": forge may add its one argument
// and its one import, and may not reformat, reorder, or reflow anything else.
func assertOnlyAddedLines(t *testing.T, before, after string) {
	t.Helper()
	origLines := strings.Split(strings.TrimRight(before, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(after, "\n"), "\n")
	i := 0
	for _, orig := range origLines {
		found := false
		for ; i < len(newLines); i++ {
			if newLines[i] == orig {
				i++
				found = true
				break
			}
		}
		if !found {
			t.Errorf("original line %q is missing or was rewritten:\n--- before ---\n%s\n--- after ---\n%s", orig, before, after)
			return
		}
	}
}

// TestWireComponentIntoMain_AddsMissingImport covers the worker/operator case:
// the group subpackage has never been imported, so the wiring has to add the
// import line as well as the argument.
func TestWireComponentIntoMain_AddsMissingImport(t *testing.T) {
	root := t.TempDir()
	p := writeMain(t, root, "proj", pristineMain)

	if _, err := WireComponentIntoMain(root, "proj", "example.com/proj", "workers", "NewReaperCmd"); err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, `"example.com/proj/cmd/proj/cmd/workers"`) {
		t.Errorf("main.go is missing the workers import:\n%s", out)
	}
	if !strings.Contains(out, "workers.NewReaperCmd") {
		t.Errorf("main.go is missing the worker constructor:\n%s", out)
	}
	assertParses(t, "main.go", out)
}

// TestWireComponentIntoMain_PreservesHandEdits is the ownership guarantee.
// main.go is "yours" code; the append must leave every other byte's meaning
// intact — the user's comments, their own imports, their extra statements.
func TestWireComponentIntoMain_PreservesHandEdits(t *testing.T) {
	root := t.TempDir()
	const edited = `package main

import (
	"log/slog"
	"os"

	"example.com/proj/cmd/proj/cmd"
	"example.com/proj/cmd/proj/cmd/services"
)

// MY NOTE: we set the log level before the tree is built.
func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)
	if os.Getenv("PROJ_TRACE") != "" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cmd.Execute(
		// services
		services.NewBillingCmd,
	)
}
`
	p := writeMain(t, root, "proj", edited)

	got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd")
	if err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}
	if got != WireApplied {
		t.Fatalf("outcome = %v, want WireApplied — a recognizable Execute call is wirable even when the file has hand edits", got)
	}

	out := readFile(t, p)
	for _, keep := range []string{
		"MY NOTE: we set the log level",
		"slog.SetLogLoggerLevel",
		`os.Getenv("PROJ_TRACE")`,
		`"log/slog"`,
		"services.NewBillingCmd",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("wiring destroyed hand-edited content %q:\n%s", keep, out)
		}
	}
	if !strings.Contains(out, "services.NewOrdersCmd") {
		t.Errorf("main.go did not gain the constructor:\n%s", out)
	}
	assertParses(t, "main.go", out)
}

// TestWireComponentIntoMain_InsertsWithinItsOwnGroup pins placement inside a
// grouped arg list. The scaffolded main.go labels its sections with
// `// services`, `// workers`, `// operators` comments, and appending a
// service after the last operator puts it under the wrong heading — the file
// still compiles and now reads as a lie, in the one file whose whole purpose
// is that a human reads it.
func TestWireComponentIntoMain_InsertsWithinItsOwnGroup(t *testing.T) {
	root := t.TempDir()
	p := writeMain(t, root, "proj", `package main

import (
	"example.com/proj/cmd/proj/cmd"
	"example.com/proj/cmd/proj/cmd/operators"
	"example.com/proj/cmd/proj/cmd/services"
	"example.com/proj/cmd/proj/cmd/workers"
)

func main() {
	cmd.Execute(
		// services
		services.NewBillingCmd,
		// workers
		workers.NewReaperCmd,
		// operators
		operators.NewSyncerCmd,
	)
}
`)
	if _, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd"); err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}

	out := readFile(t, p)
	orders := strings.Index(out, "services.NewOrdersCmd")
	workersComment := strings.Index(out, "// workers")
	if orders < 0 {
		t.Fatalf("main.go did not gain the constructor:\n%s", out)
	}
	if orders > workersComment {
		t.Errorf("the new service was appended after the `// workers` heading instead of into the services group:\n%s", out)
	}
	assertParses(t, "main.go", out)
}

// TestWireComponentIntoMain_AlreadyWiredIsNoop keeps a --resume/--force re-run
// from appending a duplicate argument (which would not compile).
func TestWireComponentIntoMain_AlreadyWiredIsNoop(t *testing.T) {
	root := t.TempDir()
	p := writeMain(t, root, "proj", pristineMain)
	before := readFile(t, p)

	got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewBillingCmd")
	if err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}
	if got != WireAlreadyWired {
		t.Errorf("outcome = %v, want WireAlreadyWired", got)
	}
	if after := readFile(t, p); after != before {
		t.Errorf("already-wired run rewrote main.go:\ngot:\n%s\nwant:\n%s", after, before)
	}
}

// TestWireComponentIntoMain_UnrecognizedIsRefused is the safety floor. When
// forge cannot find the structure it would append to, it must decline and let
// the caller PRINT instructions — never guess at a rewrite.
func TestWireComponentIntoMain_UnrecognizedIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{
			name: "no Execute call",
			content: `package main

func main() {
	println("hand-rolled")
}
`,
		},
		{
			name: "unparseable",
			content: `package main

func main( {
`,
		},
		{
			name: "execute built elsewhere",
			content: `package main

import "example.com/proj/cmd/proj/cmd"

func main() {
	tree := buildTree()
	cmd.ExecuteTree(tree)
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := writeMain(t, root, "proj", tc.content)

			got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd")
			if err != nil {
				t.Fatalf("WireComponentIntoMain returned an error instead of an outcome: %v", err)
			}
			if got != WireUnrecognized {
				t.Errorf("outcome = %v, want WireUnrecognized", got)
			}
			if after := readFile(t, p); after != tc.content {
				t.Errorf("forge modified a main.go it did not recognize:\n%s", after)
			}
		})
	}
}

// TestWireComponentIntoMain_MissingFileIsRefused covers the project that
// deleted its composition root, or a bin name that does not exist.
func TestWireComponentIntoMain_MissingFileIsRefused(t *testing.T) {
	root := t.TempDir()
	got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != WireUnrecognized {
		t.Errorf("outcome = %v, want WireUnrecognized for an absent main.go", got)
	}
}

// TestWireComponentIntoMain_EmptyExecute covers the greenfield shape: a
// project whose composition root was scaffolded with no components at all
// renders as a bare `cmd.Execute()`.
func TestWireComponentIntoMain_EmptyExecute(t *testing.T) {
	root := t.TempDir()
	p := writeMain(t, root, "proj", `package main

import "example.com/proj/cmd/proj/cmd"

func main() {
	cmd.Execute()
}
`)
	got, err := WireComponentIntoMain(root, "proj", "example.com/proj", "services", "NewOrdersCmd")
	if err != nil {
		t.Fatalf("WireComponentIntoMain: %v", err)
	}
	if got != WireApplied {
		t.Fatalf("outcome = %v, want WireApplied", got)
	}
	out := readFile(t, p)
	if !strings.Contains(out, "services.NewOrdersCmd") || !strings.Contains(out, `cmd/proj/cmd/services"`) {
		t.Errorf("bare Execute() was not wired:\n%s", out)
	}
	assertParses(t, "main.go", out)
}
