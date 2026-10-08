package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// machineRouter answers worktree commands the way a daemon does, and records
// which machine each one reached. A command left to default resolution is
// recorded against machineRouterDefault.
type machineRouter struct {
	routerStub
	home string

	mu   sync.Mutex
	sent []machineCommand
}

type machineCommand struct {
	daemonID    string
	commandType string
}

const machineRouterDefault = "daemon-default"

func (r *machineRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return machineRouterDefault, nil
}

// ResolveDaemonIDForSelector models the real router's ownership check: the
// fake user owns the default machine and daemon-b.
func (r *machineRouter) ResolveDaemonIDForSelector(_ context.Context, _ string, sel *DaemonSelector) (string, error) {
	switch sel.ID {
	case machineRouterDefault, "daemon-b":
		return sel.ID, nil
	}
	return "", errors.New("no daemon available")
}

func (r *machineRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.deliver(machineRouterDefault, commandType, payload)
}

func (r *machineRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.deliver(daemonID, commandType, payload)
}

func (r *machineRouter) deliver(daemonID, commandType string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	r.sent = append(r.sent, machineCommand{daemonID: daemonID, commandType: commandType})
	r.mu.Unlock()

	var req map[string]any
	_ = json.Unmarshal(payload, &req)
	switch commandType {
	case "worktree.generate_repo_id":
		return json.Marshal(map[string]string{"repo_id": "repo123"})
	case "skills.get_home_dir":
		return json.Marshal(map[string]string{"home_dir": r.home})
	case "worktree.create":
		return json.Marshal(map[string]any{
			"success":       true,
			"worktree_path": filepath.Join(r.home, ".reliant", "worktrees", req["repo_id"].(string), req["name"].(string)),
			"base_branch":   "main",
		})
	}
	return json.Marshal(map[string]any{})
}

func (r *machineRouter) commands() []machineCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]machineCommand(nil), r.sent...)
}

func TestRunMachine_FollowsTheRunsDaemonSelector(t *testing.T) {
	machine := NewRunMachine(&machineRouter{})

	pinned, err := machine.DaemonID(WithDaemonSelector(context.Background(), &DaemonSelector{ID: "daemon-b"}), "user-1")
	require.NoError(t, err)
	assert.Equal(t, "daemon-b", pinned, "a run with a selector executes on the machine it names")

	unpinned, err := machine.DaemonID(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, machineRouterDefault, unpinned, "a run with no selector keeps default resolution")
}

// TestWorktreeTool_RunsGitOnTheRunsMachine is the regression for "the worktree
// tool runs git on the worker". It drives the tool exactly as ExecuteTools
// does: a server-side call carrying the run's daemon selector. Every git and
// filesystem step must reach that machine as a daemon command, and nothing may
// touch the worker's own disk. The project path does not exist on the worker:
// the old tool failed here with "not a git repository", having looked locally.
func TestWorktreeTool_RunsGitOnTheRunsMachine(t *testing.T) {
	workerHome := t.TempDir()
	t.Setenv("HOME", workerHome)

	router := &machineRouter{home: "/home/machine-b"}
	executor := NewRemoteExecutor(router)
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{
		RunMachine: NewRunMachine(router),
	})))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	result, err := executor.ExecuteTool(ctx, &ToolRequest{
		ToolName:       tools.WorktreeToolName,
		ToolInput:      `{"action":"create","name":"feature-auth","copy_files":[".env"]}`,
		ToolCallID:     "call-1",
		UserID:         "user-1",
		ChatID:         "chat-1",
		ProjectID:      "project-1",
		ProjectPath:    "/home/machine-b/project",
		WorktreePath:   "/home/machine-b/project",
		DaemonSelector: &DaemonSelector{ID: "daemon-b"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)

	var types []string
	for _, cmd := range router.commands() {
		types = append(types, cmd.commandType)
		assert.Equal(t, "daemon-b", cmd.daemonID, "%s ran on the wrong machine", cmd.commandType)
	}
	assert.Contains(t, types, "worktree.create", "the checkout must be made by the machine's daemon")
	assert.Contains(t, types, "worktree.copy_paths", "copy_files must be copied by the machine's daemon")
	assert.Contains(t, result.Content, "/home/machine-b/.reliant/worktrees/repo123/feature-auth")

	_, statErr := os.Stat(filepath.Join(workerHome, ".reliant"))
	assert.True(t, os.IsNotExist(statErr), "the worker's own disk must not be touched")
}
