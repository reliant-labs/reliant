package scaffold

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

// TestScaffoldCommandsHoldProjectLock pins that EVERY runnable command in the
// `forge scaffold` tree — the bare sweep and each noun — runs under the
// project's generate lock. A noun added without it would let its writes
// interleave with another agent's `forge generate` in the same checkout.
func TestScaffoldCommandsHoldProjectLock(t *testing.T) {
	f := testFactory()
	// `scaffold package` / `scaffold adapter` reuse this RunE; leaving it nil
	// would make them non-runnable here and drop them from the walk.
	f.Gen.RunPackageNew = func(*cobra.Command, []string) error { return nil }
	root := newScaffoldCmd(f)
	var runnable, unlocked []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() {
			runnable = append(runnable, c.CommandPath())
			if c.Annotations[projectLockAnnotation] != "held" {
				unlocked = append(unlocked, c.CommandPath())
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	if len(runnable) < 10 {
		t.Fatalf("walked only %d runnable scaffold commands (%v) — the tree walk is not seeing the nouns", len(runnable), runnable)
	}
	if len(unlocked) > 0 {
		t.Errorf("scaffold commands that run without the project lock: %v", unlocked)
	}
}

// TestHoldProjectLock_WrapsRunE drives the wrapper itself: inside a project
// the command body runs strictly between acquire and release; outside one
// there is nothing to lock and the body runs bare.
func TestHoldProjectLock_WrapsRunE(t *testing.T) {
	var events []string
	f := testFactory()
	f.Gen.HoldProjectLock = func(projectDir string) (func(), error) {
		events = append(events, "lock "+filepath.Base(projectDir))
		return func() { events = append(events, "release") }, nil
	}
	newTree := func() *cobra.Command {
		parent := &cobra.Command{Use: "scaffold", RunE: func(*cobra.Command, []string) error {
			events = append(events, "run scaffold")
			return nil
		}}
		parent.AddCommand(&cobra.Command{Use: "noun", RunE: func(*cobra.Command, []string) error {
			events = append(events, "run noun")
			return nil
		}})
		holdProjectLock(parent, f)
		return parent
	}

	project := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "forge.yaml"), []byte("name: proj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	tree := newTree()
	tree.SetArgs([]string{"noun"})
	if err := tree.Execute(); err != nil {
		t.Fatalf("execute inside a project: %v", err)
	}
	if want := []string{"lock proj", "run noun", "release"}; !slices.Equal(events, want) {
		t.Errorf("inside a project: events = %v, want %v", events, want)
	}

	events = nil
	t.Chdir(t.TempDir()) // no forge.yaml
	tree = newTree()
	tree.SetArgs(nil)
	if err := tree.Execute(); err != nil {
		t.Fatalf("execute outside a project: %v", err)
	}
	if want := []string{"run scaffold"}; !slices.Equal(events, want) {
		t.Errorf("outside a project: events = %v, want %v (nothing to lock)", events, want)
	}
}
