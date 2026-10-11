// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shell never refuses a hand-made worktree: a user may have a worktree
// layout of their own (a directory their reliant.md tells every agent about),
// and an agent following it must be able to run the command. Each of these
// reaches the machine and comes back as an ordinary result.
func TestShellTool_RunsGitWorktreeAdd(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		`git worktree add ../reliant-agent-credentials -b feat/agent-credentials`,
		`git worktree add -b feat/x ../repo-x main`,
		`git -C /Users/u/projects/p/reliant worktree add ../reliant-x -b x`,
		`cd reliant && git fetch && git worktree add ../reliant-x origin/main`,
		`git worktree add ~/src/rl-agent-worktrees/feature -b feature origin/main`,
		`git worktree add "$HOME/.reliant/worktrees/p/x"`,
		`git worktree add /tmp/inspect-v1 v1.0.0`,
		`git worktree add`,
	} {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			d, resp := runShellAgainst(t, "/Users/u/project", cmd)
			require.Equal(t, []string{cmd}, d.ran, "the command must reach the machine unchanged")
			require.False(t, resp.IsError, "nothing is refused: %q", resp.Content)
			assert.NotContains(t, resp.Content, "refused")
		})
	}
}

func TestWorktreeAddTargets(t *testing.T) {
	t.Parallel()
	const wd = "/Users/u/src/reliant-labs/reliant"
	cases := []struct {
		cmd  string
		want []string
	}{
		{`git worktree add ../reliant-x -b x`, []string{"/Users/u/src/reliant-labs/reliant-x"}},
		{`git worktree add -b feat/x ../repo-x main`, []string{"/Users/u/src/reliant-labs/repo-x"}},
		{`git worktree add --detach ../repo-x`, []string{"/Users/u/src/reliant-labs/repo-x"}},
		{`git worktree add -f --lock --reason "agent" ../repo-x`, []string{"/Users/u/src/reliant-labs/repo-x"}},
		{`git -C /Users/u/p/forge worktree add ../forge-x -b x`, []string{"/Users/u/p/forge-x"}},
		{`git -c core.hooksPath=/dev/null worktree add ~/wt/x`, []string{"~/wt/x"}},
		{`cd .. && git fetch && git worktree add forge-x origin/main`, []string{"/Users/u/src/reliant-labs/forge-x"}},
		{`GIT_TRACE=1 git worktree add ../x`, []string{"/Users/u/src/reliant-labs/x"}},
		{"git worktree add \\\n  ../reliant-x \\\n  -b x", []string{"/Users/u/src/reliant-labs/reliant-x"}},
		{`git worktree add "$HOME/src/rl-agent-worktrees/x"`, []string{"~/src/rl-agent-worktrees/x"}},
		{`git worktree add ${HOME}/wt/y`, []string{"~/wt/y"}},
		{`git worktree add $WT -b x`, []string{"$WT"}},
		{`git worktree add`, []string{""}},
		{`git worktree add a && git worktree add ../b`, []string{wd + "/a", "/Users/u/src/reliant-labs/b"}},
		// Throwaway checkouts in a temp dir are not worktrees anyone keeps.
		{`git worktree add /tmp/inspect-v1 v1.0.0`, nil},
		{`git worktree add --detach "${TMPDIR:-/tmp}/old" abc123`, nil},
		{`git worktree add "$TMPDIR/old" HEAD~3`, nil},
		{`git worktree add /var/folders/xy/T/old HEAD`, nil},
		{`git worktree add "$(mktemp -d)/old" HEAD`, nil},
		{`git worktree add -b scratch /private/tmp/scratch`, nil},
		{`cd /tmp && git worktree add scratch`, nil},
		// Every other worktree subcommand, and text that only mentions it.
		{`git worktree list`, nil},
		{`git worktree remove ../x`, nil},
		{`git worktree prune`, nil},
		{`git -C reliant worktree list --porcelain`, nil},
		{`git log --oneline -- worktree add`, nil},
		{`echo "git worktree add ../x"`, nil},
		{`rg -n 'git worktree add' internal/`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, WorktreeAddTargets(tc.cmd, wd))
		})
	}
}

// The rule as this repository's own memory states it.
const memoryWithWorktreeRoot = `## Shared-machine hygiene

- Ports 5432, 5434 and 3000 are already bound.
- **Worktrees go in ` + "`~/src/rl-agent-worktrees/<name>`" + `**, never ` + "`/tmp`" + `. When
  your work is pushed and the worktree is clean, ` + "`git worktree remove`" + ` it.
- Never ` + "`git stash`" + ` in /Users/u/src/shared.
`

func TestWorktreeRootsFromMemory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		docs []string
		want []string
	}{
		{"the repository's own rule, wrapped over two lines", []string{memoryWithWorktreeRoot}, []string{"~/src/rl-agent-worktrees"}},
		{"a negated clause names no root", []string{"Never create worktrees in ~/projects; put them in ~/wt instead."}, []string{"~/wt"}},
		{"$HOME and absolute roots", []string{"Make git worktrees under $HOME/trees/{branch}.", "- worktree root: /srv/wt/*"}, []string{"~/trees", "/srv/wt"}},
		{"a paragraph that does not mention worktrees", []string{"Logs live in ~/logs/dev."}, nil},
		{"a section about worktrees", []string{"## Worktrees\n\nPut each one in `~/trees/<repo>-<name>`.\n\n## Logs\n\nLogs live in ~/logs/dev."}, []string{"~/trees"}},
		{"a bare home is not a root", []string{"Keep worktrees in ~/ somewhere."}, nil},
		{"duplicates across files collapse", []string{"Worktrees: ~/wt/<name>", "worktrees go in `~/wt/`"}, []string{"~/wt"}},
		{"empty memory", []string{"", ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, WorktreeRootsFromMemory(tc.docs...))
		})
	}
}

// A memory rule that names where worktrees go wins: a checkout made there gets
// no suggestion, however its path is spelled. Anywhere else still does.
func TestWorktreeAddSuggestion_MemoryRootWins(t *testing.T) {
	t.Parallel()
	roots := WorktreeRootsFromMemory(memoryWithWorktreeRoot)
	require.Equal(t, []string{"~/src/rl-agent-worktrees"}, roots)
	const wd = "/Users/u/src/reliant-labs/reliant"

	quiet := []string{
		`git worktree add ~/src/rl-agent-worktrees/reliant-655 -b fix origin/main`,
		`git worktree add "$HOME/src/rl-agent-worktrees/x"`,
		`git worktree add /Users/u/src/rl-agent-worktrees/x -b x`,
		`git worktree add /home/agent/src/rl-agent-worktrees/x -b x`,
		`git worktree add ../../rl-agent-worktrees/x -b x`,
		`git -C /Users/u/src/reliant-labs/forge worktree add ../../rl-agent-worktrees/forge-x`,
	}
	for _, cmd := range quiet {
		assert.Empty(t, WorktreeAddSuggestion(WorktreeAddTargets(cmd, wd), roots), cmd)
	}

	noisy := []string{
		`git worktree add ../reliant-x -b x`,
		`git worktree add ~/src/rl-agent-worktrees-old/x`,
		`git worktree add ~/src/rl-agent-worktrees/x && git worktree add ../y`,
		`git worktree add $WT`,
		`git worktree add`,
	}
	for _, cmd := range noisy {
		assert.Equal(t, worktreeAddSuggestion, WorktreeAddSuggestion(WorktreeAddTargets(cmd, wd), roots), cmd)
	}

	assert.Empty(t, WorktreeAddSuggestion(WorktreeAddTargets(`git worktree add /tmp/x HEAD`, wd), nil), "temp checkouts get none")
	assert.Empty(t, WorktreeAddSuggestion(nil, nil), "no worktree, no suggestion")
}

// The suggestion is one line, names the worktree tool and how to reach it, and
// defers to the user's memory.
func TestWorktreeAddSuggestion_Text(t *testing.T) {
	t.Parallel()
	got := WorktreeAddSuggestion([]string{"/Users/u/project-x"}, nil)
	assert.NotContains(t, got, "\n", "one line")
	assert.NotContains(t, strings.ToLower(got), "refused")
	for _, want := range []string{`"action": "create"`, `load_tool(name="worktree")`, `spawn(worktree=`, "memory", "follow that instead"} {
		assert.Contains(t, got, want)
	}
}

func TestWithShellSuggestion(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(ShellOutput{Stdout: "Preparing worktree (new branch 'x')\n", ExitCode: 0})
	require.NoError(t, err)

	got := WithShellSuggestion(string(encoded), "Suggestion: use the worktree tool.")
	var out ShellOutput
	require.NoError(t, json.Unmarshal([]byte(got), &out), "the result stays one valid shell envelope: %s", got)
	assert.Equal(t, "Preparing worktree (new branch 'x')\n", out.Stdout)
	assert.Equal(t, "Suggestion: use the worktree tool.", out.Suggestion)

	assert.Equal(t, string(encoded), WithShellSuggestion(string(encoded), ""), "no suggestion, no change")
	for _, content := range []string{
		"refused: something else",
		`{"process_id":"p1","command":"git worktree add ../x","backgrounded":true}`,
		string(encoded) + "\ntrailing text",
	} {
		assert.Equal(t, content, WithShellSuggestion(content, "Suggestion: x"), "not a foreground shell result: %q", content)
	}
}
