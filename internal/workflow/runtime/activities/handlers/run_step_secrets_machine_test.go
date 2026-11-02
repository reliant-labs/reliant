package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// maskingDaemonID is the machine a test chat's commands run on (its project's
// checkout, installTestCheckout).
const maskingDaemonID = testMachine

// fakeMachines serves daemons by id, each with the capabilities it registered.
type fakeMachines map[string][]string

func (f fakeMachines) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	caps, ok := f[id]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		return nil, err
	}
	s := string(raw)
	return &db.Daemon{ID: id, Capabilities: &s}, nil
}

// maskingMachine is a current daemon: it advertises the masking capability.
var maskingMachine = fakeMachines{maskingDaemonID: {"shell", toolexec.CapabilitySecretMask}}

// A daemon that does not advertise masking (an older build, which ignores the
// mask and writes an echoed secret to its result outbox) receives neither the
// command nor the secret: nothing is resolved and nothing is sent.
func TestRunStepWithSecretsRefusesAMachineThatDoesNotMaskThem(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	for name, machines := range map[string]fakeMachines{
		"an older daemon":   {maskingDaemonID: {"shell", "read_file"}},
		"an unknown daemon": {},
	} {
		t.Run(name, func(t *testing.T) {
			exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
			resolver := traderResolver()
			a := NewExecuteRunStepActivity(repo, nil, exec).WithMachineCapabilities(machines).WithSecretResolver(resolver)
			out := executeRun(t, a, runSecretInput(chatID, `./deploy.sh --key "{{ secrets.TRADER_KEY }}"`))

			assert.Equal(t, -1, out.ExitCode)
			assert.Contains(t, out.Output, "too old to keep them out of its stored results")
			assert.Empty(t, exec.calls, "the command reached a machine that cannot mask its secrets")
			assert.Empty(t, resolver.calls, "a secret was resolved for a machine that cannot mask it")
		})
	}

	t.Run("a worker that cannot read capabilities", func(t *testing.T) {
		exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
		resolver := traderResolver()
		a := NewExecuteRunStepActivity(repo, nil, exec).WithSecretResolver(resolver)
		out := executeRun(t, a, runSecretInput(chatID, `./deploy.sh --key "{{ secrets.TRADER_KEY }}"`))
		assert.Equal(t, -1, out.ExitCode)
		assert.Empty(t, exec.calls)
		assert.Empty(t, resolver.calls)
	})
}

// A step without secrets needs no capability: the gate is for secrets only,
// so a worker with no capability reader still runs it.
func TestRunStepWithoutSecretsDoesNotCheckTheMachine(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	exec := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
	a := NewExecuteRunStepActivity(repo, nil, exec)
	out := executeRun(t, a, ExecuteRunStepInput{WorkflowID: "wf-1", ChatID: chatID, StepID: "build", Command: "make"})
	assert.Equal(t, 0, out.ExitCode)
	require.Len(t, exec.calls, 1)
}

func TestAdvertises(t *testing.T) {
	caps := `["shell","` + toolexec.CapabilitySecretMask + `"]`
	assert.True(t, advertises(&caps, toolexec.CapabilitySecretMask))
	old := `["shell"]`
	assert.False(t, advertises(&old, toolexec.CapabilitySecretMask))
	garbage := `not json`
	assert.False(t, advertises(&garbage, toolexec.CapabilitySecretMask))
	assert.False(t, advertises(nil, toolexec.CapabilitySecretMask))
}
