// Copyright (c) 2026 Reliant Labs
package requirements

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func cliProbe(lines ...string) string {
	return strings.Join(append(lines, probeDone), "\n") + "\n"
}

// Each state a vendor CLI can be in on the machine, as the run's failure
// names it, with the command that fixes it on that machine.
func TestCLILoginStates(t *testing.T) {
	cases := map[string]struct {
		line, problem, fix string
	}{
		"signed out": {"RELIANT_CLI\tclaude\tsigned_out",
			"cli claude: Claude Code isn't signed in on machine ci-box", "run `reliant machine login claude --machine ci-box`"},
		"missing": {"RELIANT_CLI\tclaude\tmissing",
			"cli claude: Claude Code (claude) isn't installed on machine ci-box", "run `reliant machine login claude --machine ci-box` (it installs the CLI)"},
		"expired": {"RELIANT_CLI\tclaude\texpired",
			"cli claude: Claude Code's sign-in on machine ci-box has expired", "run `reliant machine login claude --machine ci-box`"},
		"unknown": {"RELIANT_CLI\tclaude\tunknown",
			"cli claude: could not tell whether Claude Code is signed in on machine ci-box", "run `reliant machine login --status claude --machine ci-box` to see why"},
		"no verdict at all": {"",
			"could not tell whether Claude Code is signed in", "reliant machine login --status claude"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMachine{out: cliProbe(tc.line), name: "ci-box"}
			report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"claude"}}, m, nil)
			require.NoError(t, err)
			require.Len(t, report.Problems, 1, report.Error())
			assert.Contains(t, report.Error(), tc.problem)
			assert.Contains(t, report.Problems[0].Fix, tc.fix)
			assert.Equal(t, "ci-box", report.Problems[0].Machine)
			assert.Equal(t, "cli", report.Problems[0].Kind)
		})
	}
}

func TestCLILoginSignedInIsOK(t *testing.T) {
	m := &fakeMachine{out: cliProbe("RELIANT_CLI\tclaude\tsigned_in", "RELIANT_CLI\tcodex\tsigned_in"), name: "ci-box"}
	report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"claude", "codex"}}, m, nil)
	require.NoError(t, err)
	assert.True(t, report.OK(), report.Error())
	assert.Contains(t, m.command, "RELIANT_CLI", "the probe carries the vendor status script")
	assert.Contains(t, m.command, "auth status --json")
	assert.Contains(t, m.command, "login status")
	assert.NotContains(t, m.command, "gemini", "only the CLIs required are checked")
}

// A daemon older than the one that appends ~/.local/bin to its children's
// PATH: the CLI is there, signed in, but its run steps cannot call it by name.
func TestCLILoginOffPath(t *testing.T) {
	m := &fakeMachine{out: cliProbe("RELIANT_CLI\tcodex\tsigned_in\tlocal_bin"), name: "ci-box"}
	report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"codex"}}, m, nil)
	require.NoError(t, err)
	require.Len(t, report.Problems, 1)
	assert.Contains(t, report.Problems[0].Problem, "installed in ~/.local/bin on machine ci-box, which is not on the PATH its run steps get")
	assert.Contains(t, report.Problems[0].Fix, "update Reliant on machine ci-box")
	assert.Contains(t, report.Problems[0].Fix, `"$HOME/.local/bin/codex"`)
}

// The machine's name is quoted for the shell when it needs to be, and a
// machine whose name is not known is named by where to run the command.
func TestCLILoginMachineNames(t *testing.T) {
	out := cliProbe("RELIANT_CLI\tgemini\tsigned_out")
	m := &fakeMachine{out: out, name: "Sean's Mac"}
	report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"gemini"}}, m, nil)
	require.NoError(t, err)
	assert.Equal(t, `run `+"`"+`reliant machine login gemini --machine 'Sean'\''s Mac'`+"`", report.Problems[0].Fix)

	m = &fakeMachine{out: out}
	report, err = Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"gemini"}}, m, nil)
	require.NoError(t, err)
	assert.Equal(t, "Gemini CLI isn't signed in on the run's machine", report.Problems[0].Problem)
	assert.Equal(t, "run `reliant machine login gemini` on that machine", report.Problems[0].Fix)
}

// A definition saved by a newer release can name a vendor this worker does
// not know: a problem, not a silent pass.
func TestCLILoginUnknownVendorAtCheckTime(t *testing.T) {
	m := &fakeMachine{out: cliProbe(), name: "ci-box"}
	report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{CliLogin: []string{"aider"}}, m, nil)
	require.NoError(t, err)
	require.Len(t, report.Problems, 1)
	assert.Contains(t, report.Error(), "cli aider: this release of Reliant does not know this vendor CLI")
}

func TestCLILoginNeedsTheMachine(t *testing.T) {
	req := &reliantv1.WorkflowRequirements{CliLogin: []string{"claude"}}
	assert.False(t, Empty(req))
	assert.True(t, NeedsMachine(req))
	_, err := Check(context.Background(), req, nil, nil)
	require.ErrorContains(t, err, "cli_login")
}

func TestValidateRejectsUnknownVendors(t *testing.T) {
	problems := Validate(&reliantv1.WorkflowRequirements{CliLogin: []string{"claude", "claud", "cursor"}}, nil)
	require.Len(t, problems, 1)
	assert.Equal(t, `requires.cli_login: "claud" is not a vendor CLI (claude, codex, gemini, cursor, copilot)`, problems[0])
}

// The whole probe, tools and CLI sign-ins together, run by bash (as a run
// step is) against fake vendor CLIs: every verdict line parses, and nothing a
// vendor printed comes back.
func TestProbeCommandWithCLILogins(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	write("claude", `echo '{"loggedIn": true, "email": "someone@example.com"}'`)
	write("codex", `echo "Not logged in" >&2; exit 1`)
	for _, tool := range []string{"grep", "timeout"} {
		if p, err := exec.LookPath(tool); err == nil {
			require.NoError(t, os.Symlink(p, filepath.Join(bin, tool)))
		}
	}
	req := &reliantv1.WorkflowRequirements{Tools: []string{"claude"}, CliLogin: []string{"claude", "codex", "gemini"}}
	cmd := exec.Command("bash", "-c", ProbeCommand(req))
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home}
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "someone@example.com")

	report, err := Check(context.Background(), req, &fakeMachine{out: string(out), name: "box"}, nil)
	require.NoError(t, err)
	msg := report.Error()
	assert.Contains(t, msg, "cli codex: Codex isn't signed in on machine box")
	assert.Contains(t, msg, "cli gemini: Gemini CLI (gemini) isn't installed on machine box")
	assert.NotContains(t, msg, "claude")
	assert.Len(t, report.Problems, 2, msg)
}
