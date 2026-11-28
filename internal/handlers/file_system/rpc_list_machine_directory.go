// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// listMachineDirCommand is the daemon command ListMachineDirectory sends
// (daemonruntime's cmd_fs_machine.go).
const listMachineDirCommand = "fs.list_machine_dir"

// ListMachineDirectory lists a directory anywhere on the machine that holds
// the request's workspace: the Files tab's machine and chat-scratch views.
//
// Scope: the machine is resolveScope's, so the listing and the viewer reads
// that open what it lists (GetFileContent, GetFilePreview, DownloadFile, which
// accept an absolute path, resolveForView) reach the same disk, and the
// caller may write the project (a viewer of a shared chat may not:
// viewertest.Matrix). On top of that the machine must be the caller's own
// (authz.CanWriteMachine): a project's files are its owner's to browse, but a
// whole machine is browsed only by whoever owns it.
//
// Guard: the daemon refuses to list a credential store, as the viewer refuses
// to open one, and marks its reply guarded; a reply without the mark is never
// shown (requireViewerGuard). The daemon command is new, so a daemon that
// predates it answers with the registry's "update it" refusal, reported here
// as FailedPrecondition with errreason.MachineBrowseUnsupported.
func (s *Service) ListMachineDirectory(
	ctx context.Context,
	req *connect.Request[pb.ListMachineDirectoryRequest],
) (*connect.Response[pb.ListMachineDirectoryResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnMachine(ctx, userID, scope); err != nil {
		return nil, err
	}

	var resp struct {
		Path      string `json:"path"`
		HomeDir   string `json:"home_dir"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Name      string `json:"name"`
			Path      string `json:"path"`
			IsDir     bool   `json:"is_dir"`
			IsSymlink bool   `json:"is_symlink"`
			Size      int64  `json:"size"`
			Modified  string `json:"modified"`
		} `json:"entries"`
		ViewerGuarded bool `json:"viewer_guarded"`
	}
	cmdReq := map[string]any{"path": machinePath(scope.basePath, req.Msg.GetPath()), "viewer_root": scope.basePath}
	if err := s.sendCommand(ctx, userID, scope.target, listMachineDirCommand, cmdReq, &resp, machineListTimeoutMs); err != nil {
		return nil, machineListError(err)
	}
	if err := requireViewerGuard(resp.ViewerGuarded); err != nil {
		return nil, err
	}

	entries := make([]*pb.MachineDirectoryEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		entries = append(entries, &pb.MachineDirectoryEntry{
			Name:        e.Name,
			Path:        e.Path,
			IsDirectory: e.IsDir,
			IsSymlink:   e.IsSymlink,
			Size:        e.Size,
			Modified:    e.Modified,
		})
	}
	return connect.NewResponse(&pb.ListMachineDirectoryResponse{
		Path:          resp.Path,
		Entries:       entries,
		HomeDir:       resp.HomeDir,
		WorkspaceRoot: scope.basePath,
		Truncated:     resp.Truncated,
	}), nil
}

// requireOwnMachine refuses a machine-wide listing on a machine the caller
// does not own. A scope pinned to a machine (a chat's, a worktree's) names it;
// one left to default resolution reaches the caller's own default machine,
// which the router resolves among their daemons alone.
func (s *Service) requireOwnMachine(ctx context.Context, userID string, scope workspaceScope) error {
	if scope.target.DaemonID == "" {
		return nil
	}
	d, err := s.deps.Placement.GetDaemon(ctx, scope.target.DaemonID)
	if err != nil || !authz.CanWriteMachine(ctx, userID, d) {
		return svcerr.NotFound("machine")
	}
	return nil
}

// machinePath is the path the daemon lists for a requested one: "", "~" and
// "~/…" pass through (the daemon knows its home directory), an absolute path
// is cleaned, and a relative one is joined onto the workspace root, so ".."
// is the folder the workspace is in. Nothing confines it: listing anywhere is
// this rpc's purpose, and the daemon's guard is what refuses credential
// stores.
func machinePath(basePath, p string) string {
	p = strings.TrimSpace(p)
	if _, home := homeRelative(p); home || p == "" {
		return p
	}
	if ospath.IsAbs(p) {
		return ospath.Clean(p)
	}
	return ospath.Join(basePath, p)
}

// machineListError classifies a failed listing beyond fsProxyDaemonError:
// a daemon without the command is told to update, naming what for; a file
// is not a directory, which the client opens instead; and a directory the
// machine's user cannot read is a refusal by that disk, not a server failure.
func machineListError(err error) error {
	switch {
	case daemonerr.IsOutdated(err):
		return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(
			"this machine's Reliant is too old to browse the whole machine: update it"), err),
			errreason.MachineBrowseUnsupported)
	case strings.Contains(err.Error(), "not a directory"):
		return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition("path is not a directory"), err),
			errreason.NotADirectory)
	case svcerr.Code(err) == connect.CodeInternal && strings.Contains(err.Error(), "permission denied"):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrPermissionDenied,
			"this machine's user cannot read that directory (permission denied)"), err)
	}
	return err
}
