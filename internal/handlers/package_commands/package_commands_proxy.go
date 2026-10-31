// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
)

// pkgListCommandsTimeoutMs bounds the pkg.list_commands round trip. Discovery is
// a bounded-depth filesystem walk (pkgmgr.DefaultDiscoveryOptions caps depth at
// 10) plus manifest parsing, so it is comparable in cost to fs.get_tree, which
// uses the same budget.
const pkgListCommandsTimeoutMs = 30000

// daemon is the round trip ListCommands makes to the machine holding the
// workspace (daemonID, "" for default resolution). The worktree lives on the
// DAEMON's filesystem, so command discovery must run there — not on the
// api-server, where the path does not exist and discovery silently found
// nothing, and not on the user's default machine when the workspace is a
// checkout on another one.
func (s *Service) daemon(daemonID string) daemoncmd.Service {
	return daemoncmd.NewInstrumented(daemoncmd.Deps{Sender: repopkg.OnDaemon(s.deps.Router, daemonID)})
}

// pkgListCommandsCmd mirrors daemonruntime.pkgListCommandsRequest.
type pkgListCommandsCmd struct {
	WorkingDir string `json:"working_dir"`
}

// ListCommands forwards package-command discovery to the user's daemon and
// converts the result to proto. It resolves the working directory
// (worktree_id -> worktree.Path, else the explicit path) and runs discovery on
// the daemon's filesystem via the pkg.list_commands command.
func (s *Service) ListCommands(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPackageCommandsRequest],
) (*connect.Response[reliantv1.ListPackageCommandsResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}

	workingDir, daemonID, err := s.resolveWorkingDir(ctx, userID, req.Msg)
	if err != nil {
		return nil, err
	}

	var result pkgmgr.CommandListResponse
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "pkg.list_commands",
		pkgListCommandsCmd{WorkingDir: workingDir}, &result, pkgListCommandsTimeoutMs); err != nil {
		// dispatch already returns a loud Connect error (Unavailable for a dead
		// daemon / timeout / unknown command; NotFound for a missing dir). We
		// never fall through to an empty list here — that silent-empty result is
		// the bug this proxy exists to eliminate.
		return nil, err
	}

	return connect.NewResponse(packageCommandListToProto(&result)), nil
}

// resolveWorkingDir prefers the worktree's stored path (a daemon-side path),
// else the caller-supplied path, and returns the machine that holds it
// (Service.workspace), "" for default resolution.
func (s *Service) resolveWorkingDir(
	ctx context.Context,
	userID string,
	msg *reliantv1.ListPackageCommandsRequest,
) (string, string, error) {
	worktreeID := ""
	if msg.WorktreeId != nil {
		worktreeID = *msg.WorktreeId
	}
	path := ""
	if msg.Path != nil {
		path = *msg.Path
	}

	switch {
	case worktreeID != "":
		return s.workspace(ctx, userID, worktreeID, msg.GetChatId())
	case path != "":
		return path, "", nil
	default:
		return "", "", svcerr.InvalidArgument("worktree_id or path is required")
	}
}

// packageCommandListToProto converts a pkgmgr discovery result to the proto
// response.
func packageCommandListToProto(result *pkgmgr.CommandListResponse) *reliantv1.ListPackageCommandsResponse {
	commands := make(map[string]*reliantv1.CommandsByType, len(result.Commands))
	for pkgType, cmds := range result.Commands {
		protoCommands := make([]*reliantv1.PackageCommand, len(cmds))
		for i, cmd := range cmds {
			protoCommands[i] = convertCommandToProto(cmd)
		}
		commands[string(pkgType)] = &reliantv1.CommandsByType{Commands: protoCommands}
	}

	detectedTypes := make([]string, len(result.DetectedTypes))
	for i, t := range result.DetectedTypes {
		detectedTypes[i] = string(t)
	}

	return &reliantv1.ListPackageCommandsResponse{
		Commands:      commands,
		DetectedTypes: detectedTypes,
	}
}
