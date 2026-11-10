// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A run node's work_dir is the directory its command starts in: absolute as
// given, relative to the run's checkout, the checkout when unset. It used to
// be resolved and then dropped, so every command started in the checkout and,
// under the credentials:none sandbox, a hand-off written in the work_dir the
// workflow named was discarded (control-plane pr-regen, pr-fix).
func TestRunStep_StartsTheCommandInTheNodesWorkDir(t *testing.T) {
	f := newChatCheckoutFixture(t)
	checkout := t.TempDir()
	f.machine(t, "cloud-box", checkout)
	f.pin(t, "cloud-box")

	for _, tc := range []struct{ name, workDir, want string }{
		{"unset: the checkout", "", checkout},
		{"absolute", "/var/cache/workspace/xdg/pr-regen/run-1", "/var/cache/workspace/xdg/pr-regen/run-1"},
		{"relative: under the checkout", "control-plane/gen", filepath.Join(checkout, "control-plane", "gen")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
			act := NewExecuteRunStepActivity(f.h.Repo(), nil, executor)
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.RegisterActivity(act.Execute)
			_, err := env.ExecuteActivity(act.Execute, ExecuteRunStepInput{
				WorkflowID: "wf-1", ChatID: f.chat, StepID: "regen", Command: "bash pr-regen.sh regen", WorkDir: tc.workDir,
			})
			require.NoError(t, err)
			require.NotEmpty(t, executor.calls)
			assert.Equal(t, tc.want, executor.calls[0].workingDir)
		})
	}
}

// The directory reaches the daemon as the request's working directory, which
// the request's server-built context carries to the shell tool for a run step.
func TestRemoteRunExecutor_SendsTheCommandsDirectory(t *testing.T) {
	t.Parallel()
	cap := &capturingToolExecutor{}
	exec := NewRemoteRunExecutor(cap)
	exec.SetContext(RunExecutorContext{UserID: "u", ProjectPath: "/p", Credentials: "none"})
	_, err := exec.ExecuteCommand(context.Background(), "make gen", "/var/cache/x/run-1", 1000, nil)
	require.NoError(t, err)
	require.Len(t, cap.reqs, 1)
	assert.Equal(t, "/var/cache/x/run-1", cap.reqs[0].WorkingDir)
	assert.True(t, cap.reqs[0].RunStep)
}

func TestRunCommandDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "/c", runCommandDir("/c", "  "))
	assert.Equal(t, "/x/y", runCommandDir("/c", "/x/./y/"))
	assert.Equal(t, "/c/a", runCommandDir("/c", "a"))
	assert.Equal(t, "a", runCommandDir("", "a"))
}
