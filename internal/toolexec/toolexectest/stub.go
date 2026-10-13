// Copyright (c) 2025 Reliant Labs

// Package toolexectest holds test doubles of toolexec.DaemonRouter that
// several handler services' tests share, so each service's tests can move to
// its own package (research/forge-migration/05-p3a-handler-map.md). Only
// _test.go files import it.
//
//forge:exclude-contract: test doubles of toolexec.DaemonRouter, imported only by tests; nothing to fake behind them
package toolexectest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Stub is a toolexec.DaemonRouter whose every call succeeds with a default
// answer. It resolves every user to "test-daemon-id", and answers the two
// worktree commands that act on the local disk the way a daemon on this
// machine would: worktree.validate_path (os.Stat) and worktree.revert (git
// checkout). Any other daemon command is an "unhandled command" error.
//
// Tests embed it and override the methods their scenario needs.
type Stub struct{}

func (r *Stub) IsDaemonOnline(_ context.Context, _ string, _ *toolexec.DaemonSelector) (bool, error) {
	return true, nil
}

func (r *Stub) SendToolRequest(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) error {
	return nil
}

func (r *Stub) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}

func (r *Stub) SendToolRequestSyncWithSelector(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}

func (r *Stub) SendToolExecutionBackground(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *Stub) SendToolExecutionCancel(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *Stub) SendKillProcess(_ context.Context, _, _ string) error {
	return nil
}

func (r *Stub) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *Stub) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "worktree.validate_path":
		var req map[string]string
		_ = json.Unmarshal(payload, &req)
		path := req["path"]
		if _, err := os.Stat(path); err != nil {
			return json.Marshal(map[string]interface{}{"exists": false, "error": "not_found"})
		}
		return json.Marshal(map[string]interface{}{"exists": true})
	case "worktree.revert":
		var req struct {
			WorktreePath string   `json:"worktree_path"`
			Files        []string `json:"files"`
		}
		_ = json.Unmarshal(payload, &req)

		type revertResult struct {
			File    string `json:"file"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		var results []revertResult
		for _, file := range req.Files {
			absPath := filepath.Join(req.WorktreePath, file)
			cmd := exec.Command("git", "checkout", "--", file)
			cmd.Dir = req.WorktreePath
			if err := cmd.Run(); err != nil {
				if _, statErr := os.Stat(absPath); statErr == nil {
					_ = os.RemoveAll(absPath)
					results = append(results, revertResult{File: file, Success: true})
				} else {
					results = append(results, revertResult{File: file, Success: false, Error: "file not in changed state"})
				}
			} else {
				results = append(results, revertResult{File: file, Success: true})
			}
		}
		return json.Marshal(map[string]interface{}{"results": results})
	}
	return nil, fmt.Errorf("unhandled command: %s", commandType)
}

func (r *Stub) SendLoadProjectConfigs(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *Stub) SendWatchProjectConfigs(_ context.Context, _, _ string, _ bool) error {
	return nil
}

func (r *Stub) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}

func (r *Stub) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}

func (r *Stub) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}

func (r *Stub) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}

func (r *Stub) Close() error { return nil }

func (r *Stub) EnqueueDaemonCommand(_ context.Context, _, _ string, _ []byte, _ int32) (int, error) {
	return 0, nil
}

func (r *Stub) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	return "test-daemon-id", nil
}

func (r *Stub) OpenLocalModelHTTP(context.Context, string, string, *reliantv1.LocalModelHTTPRequest) (*toolexec.LocalModelHTTPStream, error) {
	return nil, nil
}

func (r *Stub) RefreshLocalModels(context.Context, string, string) (*reliantv1.LocalModelInventory, error) {
	return nil, nil
}

var _ toolexec.DaemonRouter = (*Stub)(nil)
