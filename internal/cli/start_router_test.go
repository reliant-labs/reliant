// Tests for `forge start` — the router. It is what the paste-able prompt on
// reliantlabs.com sends every agent to first, so three promises are pinned:
// it stays short (the reader pays for every byte in tokens), every command it
// prints parses, and every skill it names exists.

package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// startRouterMaxLines and startRouterMaxBytes are the router's budget. The fix
// for a router over budget is to name a skill instead of inlining it, never to
// raise these.
const (
	startRouterMaxLines = 60
	startRouterMaxBytes = 4096
)

// runStartRouter executes bare `forge start` through the real root command
// tree and returns its stdout, normalized to the standalone `forge` spelling.
func runStartRouter(t *testing.T) string {
	t.Helper()
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"start"})
	if err := root.Execute(); err != nil {
		t.Fatalf("forge start: unexpected error: %v\noutput:\n%s", err, out.String())
	}
	return strings.ReplaceAll(out.String(), cmdutil.Name()+" ", "forge ")
}

func TestStartRouterFitsBudget(t *testing.T) {
	out := runStartRouter(t)
	lines := strings.Count(out, "\n")
	t.Logf("`forge start`: %d lines, %d bytes (budget %d lines, %d bytes)", lines, len(out), startRouterMaxLines, startRouterMaxBytes)
	if lines > startRouterMaxLines {
		t.Errorf("`forge start` prints %d lines, over the %d-line budget: name a skill instead of inlining it", lines, startRouterMaxLines)
	}
	if len(out) > startRouterMaxBytes {
		t.Errorf("`forge start` prints %d bytes, over the %d-byte budget: name a skill instead of inlining it", len(out), startRouterMaxBytes)
	}
}

// TestStartRouterCarriesBothPaths pins the commands the two paths cannot do
// without, and the claim-link handoff the prompt relies on.
func TestStartRouterCarriesBothPaths(t *testing.T) {
	out := runStartRouter(t)
	for _, want := range []string{
		"forge env deploy prod --claimable",        // existing app: adopt + deploy, no account
		"forge project new --in-place --kind site", // empty directory: a site
		"forge start app",                          // empty directory: an app with an API
		"72 hours",                                 // what the agent must tell the user
		"claim link",                               // ...and hand over
		"forge env status prod --claimable",        // how to get the link again
	} {
		if !strings.Contains(out, want) {
			t.Errorf("`forge start` is missing %q", want)
		}
	}
}

// startRouterCommand matches a backticked command the router tells the agent
// to run.
var startRouterCommand = regexp.MustCompile("`forge ([^`]+)`")

// TestStartRouterCommandsParse runs every printed command's words through the
// real command tree and its flag set, so a renamed verb or flag fails here
// instead of in a stranger's terminal.
func TestStartRouterCommandsParse(t *testing.T) {
	out := runStartRouter(t)
	matches := startRouterCommand.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatal("found no `forge ...` commands in `forge start`")
	}
	for _, m := range matches {
		words := strings.Fields(m[1])
		root := newTestRootCmd(t)
		cmd, rest, err := root.Find(words)
		if err != nil || cmd == root {
			t.Errorf("`forge %s`: no such command (%v)", m[1], err)
			continue
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Errorf("`forge %s`: %v", m[1], err)
			continue
		}
		positional := cmd.Flags().Args()
		if cmd.Name() == "load" && cmd.Parent() != nil && cmd.Parent().Name() == "skill" {
			for _, name := range positional {
				if _, err := loadForgeShippedSkill(name); err != nil {
					t.Errorf("`forge %s`: forge ships no %q skill: %v", m[1], name, err)
				}
			}
		}
	}
}

// TestStartRouterNamesShippedSkills checks the skills named bare in prose
// (`frontend/design`), which the command parse above does not see.
func TestStartRouterNamesShippedSkills(t *testing.T) {
	for _, name := range []string{"frontend/design", "frontend/serving", "migration", "deploy/static-site", "deploy/hosting"} {
		if !strings.Contains(startRouter, name) {
			t.Errorf("router no longer names %q: drop it from this list", name)
		}
		if _, err := loadForgeShippedSkill(name); err != nil {
			t.Errorf("router names skill %q, which forge does not ship: %v", name, err)
		}
	}
}
