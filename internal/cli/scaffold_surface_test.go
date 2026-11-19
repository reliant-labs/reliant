// File: internal/cli/scaffold_surface_test.go
//
// Regression coverage for the shape of the scaffolding surface: ONE verb,
// `forge scaffold`, where arity picks the granularity —
//
//	forge scaffold                  everything the protos imply (the sweep)
//	forge scaffold <noun> [args]    exactly one thing
//
// and the old two-word spellings (`forge project add <noun>`, `forge project
// scaffold`) are GONE — not hidden, not aliased, not suggested. The
// assertions walk the assembled cobra tree rather than grepping source, so a
// resurrected alias or a subcommand quietly re-parented is a failure here.

package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// walkTree returns every command path in the assembled tree, keyed by the
// path with the root name stripped ("scaffold entity", "project audit", …).
func walkTree(t *testing.T) map[string]*cobra.Command {
	t.Helper()
	root := newTestRootCmd(t)
	out := map[string]*cobra.Command{}
	var walk func(c *cobra.Command, prefix []string)
	walk = func(c *cobra.Command, prefix []string) {
		for _, sub := range c.Commands() {
			path := append(append([]string{}, prefix...), sub.Name())
			out[strings.Join(path, " ")] = sub
			walk(sub, path)
		}
	}
	walk(root, nil)
	return out
}

// scaffoldNouns is the full explicit-noun surface. Every one of these was
// spelled `forge project add <noun>` before the collapse.
var scaffoldNouns = []string{
	"adapter",
	"binary",
	"crd",
	"entity",
	"frontend",
	"handler-file",
	"library",
	"operator",
	"package",
	"rpc",
	"scenario",
	"service",
	"webhook",
	"worker",
}

// TestScaffoldIsATopLevelVerb: `forge scaffold` sits at the root, is
// runnable bare (that IS the sweep), and hosts every noun.
func TestScaffoldIsATopLevelVerb(t *testing.T) {
	tree := walkTree(t)

	scaffold, ok := tree["scaffold"]
	if !ok {
		t.Fatalf("no top-level `forge scaffold`; tree has %v", sortedCmdPaths(tree))
	}
	if !scaffold.Runnable() {
		t.Error("`forge scaffold` with no args is not runnable — the bare form must run the sweep, not print usage")
	}
	if scaffold.Parent() == nil || scaffold.Parent().Name() != "forge" {
		t.Errorf("`scaffold` is parented under %q, want the root command", scaffold.Parent().Name())
	}

	for _, noun := range scaffoldNouns {
		sub, ok := tree["scaffold "+noun]
		if !ok {
			t.Errorf("`forge scaffold %s` is missing", noun)
			continue
		}
		if !sub.Runnable() {
			t.Errorf("`forge scaffold %s` is not runnable", noun)
		}
	}

	// Nothing extra crept in: the noun list above is the whole surface.
	for _, sub := range scaffold.Commands() {
		if sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		found := false
		for _, noun := range scaffoldNouns {
			if sub.Name() == noun {
				found = true
			}
		}
		if !found {
			t.Errorf("`forge scaffold %s` exists but is not in scaffoldNouns — update the list or the tree", sub.Name())
		}
	}
}

// TestBareScaffoldResolvesToTheSweep: cobra must route `forge scaffold` with
// no args to the sweep's RunE, and reject an unknown noun as an unknown
// COMMAND rather than swallowing it as a sweep argument.
func TestBareScaffoldResolvesToTheSweep(t *testing.T) {
	root := newTestRootCmd(t)

	resolved, rest, err := root.Find([]string{"scaffold"})
	if err != nil {
		t.Fatalf("`forge scaffold` does not resolve: %v", err)
	}
	if resolved.Name() != "scaffold" || len(rest) != 0 {
		t.Fatalf("`forge scaffold` resolved to %q with leftover args %v", resolved.CommandPath(), rest)
	}
	if err := resolved.ValidateArgs(nil); err != nil {
		t.Errorf("`forge scaffold` (no args) fails its own Args validator: %v", err)
	}

	// An unknown noun must NOT be accepted as a positional arg to the sweep.
	if err := resolved.ValidateArgs([]string{"widget"}); err == nil {
		t.Error("`forge scaffold widget` was accepted — an unknown noun must be rejected, not swept")
	}

	// The bare form actually reaches RunE: outside a project it fails with
	// the project-root error, which only the sweep's body can produce.
	t.Chdir(t.TempDir())
	out, err := execRoot(t, "scaffold")
	if err == nil {
		t.Fatal("expected `forge scaffold` outside a project to fail")
	}
	if !strings.Contains(err.Error(), "forge.yaml") {
		t.Errorf("`forge scaffold` failed with %v, want the forge.yaml-not-found error that proves RunE ran (output: %s)", err, out)
	}
}

// TestOldScaffoldSpellingsAreGone: `forge project add …` and `forge project
// scaffold` must not exist under any guise — no subcommand, no alias, no
// hidden command. Git history is the record; the CLI is not.
func TestOldScaffoldSpellingsAreGone(t *testing.T) {
	tree := walkTree(t)

	for _, gone := range []string{"project add", "project scaffold"} {
		if cmd, ok := tree[gone]; ok {
			t.Errorf("`forge %s` still exists (hidden=%v) — the rename is incomplete", gone, cmd.Hidden)
		}
	}
	for _, noun := range scaffoldNouns {
		if _, ok := tree["project add "+noun]; ok {
			t.Errorf("`forge project add %s` still exists — the rename is incomplete", noun)
		}
	}

	// No command anywhere may alias its way back to the old spelling.
	for path, cmd := range tree {
		for _, alias := range cmd.Aliases {
			if alias == "add" && cmd.Parent() != nil && cmd.Parent().Name() == "project" {
				t.Errorf("%q aliases `add` under `project` — no back-compat shims", path)
			}
		}
	}

	// Cobra must report an unknown command, not "did you mean".
	if _, _, err := newTestRootCmd(t).Find([]string{"project", "add", "entity"}); err != nil {
		// Find only errors at root level; the real check is that the
		// resolved command is `project` with leftover args, below.
		t.Logf("Find reported: %v", err)
	}
	resolved, rest, _ := newTestRootCmd(t).Find([]string{"project", "add", "entity"})
	if resolved.Name() != "project" {
		t.Errorf("`forge project add entity` resolved to %q — nothing should match `add`", resolved.CommandPath())
	}
	if err := resolved.ValidateArgs(rest); err == nil {
		t.Error("`forge project add entity` was accepted by `forge project` — it must fail as an unknown command")
	}
}

// TestProjectKeepsItsInspectionCommands: collapsing the scaffolding verbs out
// of `forge project` must not have taken the project-level create / retire /
// evolve / inspect commands with them.
func TestProjectKeepsItsInspectionCommands(t *testing.T) {
	tree := walkTree(t)
	for _, want := range []string{
		"project new",
		"project delete",
		"project disown",
		"project migrate",
		"project upgrade",
		"project map",
		"project graph",
		"project introspect",
		"project features",
		"project annotations",
		"project audit",
	} {
		if _, ok := tree[want]; !ok {
			t.Errorf("`forge %s` disappeared; tree has %v", want, sortedCmdPaths(tree))
		}
	}
}

func sortedCmdPaths(m map[string]*cobra.Command) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
