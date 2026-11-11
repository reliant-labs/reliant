// Tests for `forge start` — the one-call greenfield brief.
//
// The brief exists to collapse an agent's orientation from six
// documentation calls to one, so both halves of that promise are pinned
// here: the output must CARRY the load-bearing facts (otherwise the agent
// makes a seventh call to recover one of them), and it must FIT the
// consumer's output ceiling (otherwise the tail — where the next-step
// skill pointers live — is cut off and the agent is stranded).

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// maxBriefBytes is the ceiling `forge start` output must fit under.
//
// It mirrors reliant's MaxSkillBodySize / MaxOutputSize — the general
// tool-output ceiling a harness applies to anything it reads, including
// the stdout of a shelled-out command. Past it the tail is truncated, and
// the tail of this brief is the "where to go next" pointer list.
//
// Kept in step with maxDeliveredSkillBytes in
// internal/templates/skills_size_test.go, which guards the same ceiling
// for the skill-delivery path.
const maxBriefBytes = 24_000

// briefComfortBytes is the tighter target. A harness that shells out to
// `forge start` through a generic bash tool caps that tool's output well
// below the skill ceiling, so the brief aims to survive BOTH paths rather
// than only the generous one.
const briefComfortBytes = 16_000

// findSubcommand returns the named direct subcommand of root, or nil.
func findSubcommand(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// runStartCmd executes `forge start` through the real root command tree
// and returns its stdout, normalized to the standalone `forge` spelling.
//
// The brief rewrites bare `forge ` command references to however this
// binary is mounted, exactly as `forge skill load` does, so every command
// it prints is copy-pasteable. Under `go test` the binary is `cli.test`,
// so the assertions below would be checking the test harness's name
// rather than the content. Normalizing keeps them about the content —
// and TestStartRewritesCommandsToTheMountedName pins the rewrite itself.
func runStartCmd(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(rawStartOutput(t), cmdutil.Name()+" ", "forge ")
}

// rawStartOutput runs the command and returns its stdout verbatim.
func rawStartOutput(t *testing.T) string {
	t.Helper()
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"start"})
	if err := root.Execute(); err != nil {
		t.Fatalf("forge start: unexpected error: %v\noutput:\n%s", err, out.String())
	}
	return out.String()
}

// TestStartRewritesCommandsToTheMountedName pins the copy-pasteability
// rule: when forge is mounted under another binary (`reliant forge`), the
// commands the brief prints must carry that prefix. A brief that tells a
// `reliant forge` user to run `forge project new` has sent them to a
// command they do not have.
func TestStartRewritesCommandsToTheMountedName(t *testing.T) {
	raw := rawStartOutput(t)

	// Under `go test` the mount name is the test binary — an
	// already-different name, which is exactly the case being pinned.
	name := cmdutil.Name()
	if name == "forge" {
		t.Skip("binary is mounted as bare `forge`; nothing to rewrite")
	}
	if !strings.Contains(raw, name+" project new") {
		t.Errorf("brief does not rewrite commands to the mounted name %q — a user of that mount would be told to run a command they do not have", name)
	}
	if strings.Contains(raw, "forge project new") {
		t.Errorf("brief still prints the bare `forge project new` spelling under mount %q", name)
	}
}

// TestStartIsDiscoverableFromRootHelp pins the entry point. `forge --help`
// is the first call any agent that has never seen forge makes; a brief it
// cannot find there saves nobody a round trip.
func TestStartIsDiscoverableFromRootHelp(t *testing.T) {
	root := newTestRootCmd(t)

	cmd := findSubcommand(root, "start")
	if cmd == nil {
		t.Fatal("no `forge start` command registered — the greenfield brief must be a top-level verb so it lands in `forge --help`")
	}
	if cmd.Hidden {
		t.Error("`forge start` is hidden — it must appear in `forge --help`, which is call #1 for a new agent")
	}
	if cmd.Short == "" {
		t.Error("`forge start` has no Short — the command list is the only place an agent learns what it does")
	}

	// The Available Commands block can sit past a `head -60` truncation
	// on a tree this size, so the root's own Long must name the brief
	// too — that text prints first, before Usage.
	if !strings.Contains(root.Long, "start") {
		t.Error("root Long help does not name `forge start`; an agent running `forge --help | head -60` must still see it")
	}
}

// TestStartCarriesLoadBearingFacts pins the facts that, if missing, cost
// the agent the extra call the brief exists to remove. Each is checked
// against the wording forge itself uses.
func TestStartCarriesLoadBearingFacts(t *testing.T) {
	out := runStartCmd(t)

	cases := []struct {
		fact string
		want []string
		why  string
	}{
		{
			fact: "--service auto-wires main.go",
			want: []string{"--service", "cmd.Execute("},
			why:  "naming every service up front is the one thing the incremental path cannot do for you",
		},
		{
			fact: "project new flags that matter",
			want: []string{"--in-place", "--name", "--mod", "--frontend"},
			why:  "these are the flags a greenfield invocation actually needs",
		},
		{
			fact: "the entity marker",
			want: []string{"forge:entity"},
			why:  "an entity is marked in the proto, not declared to the CLI",
		},
		{
			fact: "bare scaffold births CRUD + migrations",
			want: []string{"forge scaffold", "migration"},
			why:  "the agent must know CRUD and the create-table migration are injected, not hand-written",
		},
		{
			fact: "forge injects CRUD rpcs ONLY",
			want: []string{"CRUD rpcs only", "service {}"},
			why:  "orphan Request/Response pairs generate nothing and fail at go build",
		},
		{
			fact: "FK-diamond declaration syntax",
			want: []string{"COMMENT ON CONSTRAINT", "forge:ref derived-from="},
			why:  "an undeclared diamond makes `forge db seed` refuse the rows",
		},
		{
			fact: "field-type to column vocabulary",
			want: []string{"TEXT NOT NULL DEFAULT ''", "TIMESTAMPTZ", "JSONB"},
			why:  "the proto->column mapping is what an agent otherwise calls `forge project annotations` for",
		},
		{
			fact: "pointers onward instead of inlined depth",
			want: []string{"forge skill load"},
			why:  "depth is named, not inlined — that is what keeps the brief one call",
		},
	}

	for _, tc := range cases {
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("`forge start` output is missing %q (fact: %s)\n  why it is load-bearing: %s", want, tc.fact, tc.why)
			}
		}
	}
}

// TestStartFitsOutputBudget keeps the brief inside the consumer's
// ceiling. Truncation cuts the TAIL, which is exactly where the
// next-step pointers live, so an oversize brief severs guidance rather
// than merely shortening it. The fix is to cut content, never to raise
// the cap.
func TestStartFitsOutputBudget(t *testing.T) {
	out := runStartCmd(t)
	size := len(out)

	t.Logf("`forge start` output: %d bytes (comfort target %d, hard cap %d)", size, briefComfortBytes, maxBriefBytes)

	if size == 0 {
		t.Fatal("`forge start` produced no output")
	}
	if size > maxBriefBytes {
		t.Errorf("`forge start` output is %d bytes, over the %d-byte hard cap by %d — the tail (next-step pointers) would be truncated",
			size, maxBriefBytes, size-maxBriefBytes)
	}
	if size > briefComfortBytes {
		t.Errorf("`forge start` output is %d bytes, over the %d-byte comfort target by %d — it would not survive a generic bash tool cap when an agent shells out",
			size, briefComfortBytes, size-briefComfortBytes)
	}
}
