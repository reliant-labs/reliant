// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

func init() {
	RegisterCommand("pkg.list_commands", handlePkgListCommands)
}

// =============================================================================
// pkg.list_commands
//
// Runs package-command discovery (Taskfile / Makefile / package.json) on the
// DAEMON's filesystem at the requested working directory. This exists because
// worktrees on a cloud daemon live on the daemon's disk, not the api-server's:
// running pkgmgr discovery on the api-server (as the un-proxied local service
// did) always found nothing. See PackageCommandsProxyService.
// =============================================================================

type pkgListCommandsRequest struct {
	// WorkingDir is an absolute path on the daemon's filesystem — the root the
	// discovery walk starts from (a worktree path, or a caller-supplied path).
	WorkingDir string `json:"working_dir"`
}

// pkgDirNotExistPrefix is the stable error prefix this handler emits when the
// requested working directory is absent. Daemon-command errors cross the
// transport as plain strings (not wrapped Go errors), so the proxy matches this
// prefix to map the failure onto a NotFound Connect code instead of a
// retryable "daemon unavailable". Keep it in sync with daemon_proxy_base.go.
const pkgDirNotExistPrefix = "working dir does not exist"

func handlePkgListCommands(ctx context.Context, payload []byte) ([]byte, error) {
	var req pkgListCommandsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.WorkingDir == "" {
		return nil, fmt.Errorf("working_dir is required")
	}

	// Stat the directory up front so a missing path is a loud, specific failure
	// (mapped to NotFound by the proxy) — NOT an empty command list that reads
	// like "this project simply has no runnable commands".
	info, err := os.Stat(req.WorkingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %s", pkgDirNotExistPrefix, req.WorkingDir)
		}
		return nil, fmt.Errorf("stat working dir %s: %w", req.WorkingDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: %s (not a directory)", pkgDirNotExistPrefix, req.WorkingDir)
	}

	// A directory that exists but has no Taskfile/Makefile/package.json is a
	// legitimately empty result, not an error: pkgmgr discovery returns an empty
	// command map and the proxy surfaces it as an empty (successful) response.
	result, err := pkgmgr.NewService().ListCommands(ctx, req.WorkingDir)
	if err != nil {
		return nil, fmt.Errorf("list commands: %w", err)
	}

	return json.Marshal(result)
}
