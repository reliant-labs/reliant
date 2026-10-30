// Copyright (c) 2025 Reliant Labs
package requirements

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMachine answers the probe with canned output, and records it.
type fakeMachine struct {
	out     string
	code    int
	err     error
	command string
}

func (m *fakeMachine) Run(_ context.Context, command string) (string, int, error) {
	m.command = command
	return m.out, m.code, m.err
}

// fakeCredentials is a connection store: integrations it holds resolve;
// reauth ones fail as an expired connection does.
type fakeCredentials struct {
	connected map[string]bool
	reauth    map[string]bool
}

func (c fakeCredentials) Check(_ context.Context, integration string) error {
	switch {
	case c.reauth[integration]:
		return errors.New("the " + integration + " connection needs to be reconnected")
	case c.connected[integration]:
		return nil
	default:
		return errors.New(integration + " is not connected")
	}
}

func TestEverythingPresentIsOK(t *testing.T) {
	m := &fakeMachine{out: probeDone + "\n"}
	req := &reliantv1.WorkflowRequirements{Tools: []string{"git", "gh"}, Repos: []string{"control-plane"}, Integrations: []string{"github"}}
	report, err := Check(context.Background(), req, m, fakeCredentials{connected: map[string]bool{"github": true}})
	require.NoError(t, err)
	assert.True(t, report.OK(), report.Error())
	assert.Contains(t, m.command, "command -v 'gh'")
	assert.Contains(t, m.command, "ls-remote --tags origin")
	assert.NotContains(t, m.command, "fetch", "the probe writes nothing into the checkout")
}

// Every missing item is named, with its fix, in one report: the owner fixes
// them all at once rather than one per failed run.
func TestEveryProblemIsReportedTogether(t *testing.T) {
	m := &fakeMachine{out: strings.Join([]string{
		"TOOL_MISSING\ttask",
		"REPO_MISSING\treliant",
		"REPO_UNREACHABLE\tforge\tfatal: could not read Username for 'https://github.com'",
		"REPO_TAG_DIVERGED\tcontrol-plane\tv0.9.1",
		probeDone,
	}, "\n")}
	req := &reliantv1.WorkflowRequirements{Tools: []string{"git", "task"}, Repos: []string{"control-plane", "forge", "reliant"}, Integrations: []string{"github", "slack"}}
	report, err := Check(context.Background(), req, m, fakeCredentials{reauth: map[string]bool{"github": true}})
	require.NoError(t, err)
	require.False(t, report.OK())

	assert.Len(t, report.Problems, 6)
	msg := report.Error()
	for _, want := range []string{
		"tool task: not found on the machine's PATH",
		"repo reliant: no git checkout",
		"repo forge: cannot reach origin: fatal: could not read Username",
		"repo control-plane: local tag(s) v0.9.1 differ from origin's",
		"git -C control-plane fetch origin '+refs/tags/v0.9.1:refs/tags/v0.9.1'",
		"integration github: the github connection needs to be reconnected",
		"integration slack: slack is not connected",
	} {
		assert.Contains(t, msg, want)
	}
}

// A probe that did not finish is not "nothing missing".
func TestUnfinishedProbeIsAnErrorNotAPass(t *testing.T) {
	m := &fakeMachine{out: "bash: line 3: git: command not found\n", code: 127}
	_, err := Check(context.Background(), &reliantv1.WorkflowRequirements{Repos: []string{"."}}, m, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not finish")
}

func TestMachineTransportFailureIsAnError(t *testing.T) {
	m := &fakeMachine{err: errors.New("daemon disconnected")}
	_, err := Check(context.Background(), &reliantv1.WorkflowRequirements{Tools: []string{"git"}}, m, nil)
	require.ErrorContains(t, err, "daemon disconnected")
}

// Integrations alone need no machine.
func TestIntegrationsOnlyDoNotTouchTheMachine(t *testing.T) {
	report, err := Check(context.Background(), &reliantv1.WorkflowRequirements{Integrations: []string{"github"}}, nil, fakeCredentials{connected: map[string]bool{"github": true}})
	require.NoError(t, err)
	assert.True(t, report.OK())
}

func TestValidateRejectsNamesThatAreNotNames(t *testing.T) {
	problems := Validate(&reliantv1.WorkflowRequirements{
		Tools:        []string{"git", "rm -rf /", "./bin/x"},
		Repos:        []string{".", "control-plane", "../elsewhere", "/abs"},
		Integrations: []string{"github", "gitlub"},
	}, func(id string) bool { return id == "github" })
	joined := strings.Join(problems, "\n")
	assert.Len(t, problems, 5, joined)
	for _, bad := range []string{`"rm -rf /"`, `"./bin/x"`, `"../elsewhere"`, `"/abs"`, `"gitlub"`} {
		assert.Contains(t, joined, bad)
	}
}

// The probe itself, run by bash against real checkouts: a present tool, a
// missing one, a missing checkout, and a checkout whose local tag differs
// from origin's (the "cannot fetch" the release gate hit).
func TestProbeCommandAgainstRealCheckouts(t *testing.T) {
	testkit.Slow(t, "runs bash and git against temporary repositories")
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	origin := filepath.Join(root, "origin")
	require.NoError(t, os.MkdirAll(origin, 0o755))
	git(origin, "init", "-q", "-b", "main")
	git(origin, "commit", "-q", "--allow-empty", "-m", "one")
	git(origin, "tag", "v1")
	git(root, "clone", "-q", origin, filepath.Join(root, "app"))
	// origin moves v1; the clone keeps the old one.
	git(origin, "commit", "-q", "--allow-empty", "-m", "two")
	git(origin, "tag", "-f", "v1")

	req := &reliantv1.WorkflowRequirements{Tools: []string{"git", "definitely-not-a-tool-xyz"}, Repos: []string{"app", "missing"}}
	cmd := exec.Command("bash", "-c", ProbeCommand(req))
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)

	report, err := Check(context.Background(), req, &fakeMachine{out: string(out)}, nil)
	require.NoError(t, err)
	msg := report.Error()
	assert.Contains(t, msg, "tool definitely-not-a-tool-xyz")
	assert.NotContains(t, msg, "tool git:")
	assert.Contains(t, msg, "repo missing: no git checkout")
	assert.Contains(t, msg, "repo app: local tag(s) v1 differ")
	assert.Len(t, report.Problems, 3, msg)
}
