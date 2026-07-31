// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// pkgListCommandsTimeoutMs bounds the pkg.list_commands round trip. Discovery is
// a bounded-depth filesystem walk (pkgmgr.DefaultDiscoveryOptions caps depth at
// 10) plus manifest parsing, so it is comparable in cost to fs.get_tree, which
// uses the same budget.
const pkgListCommandsTimeoutMs = 30000

// PackageCommandsProxyService is the cloud-daemon implementation of the
// PackageCommandsService RPCs. On a cloud daemon the browser's worktree lives on
// the DAEMON's filesystem, so command discovery must run there — not on the
// api-server, where the path does not exist (the bug this type fixes).
//
// Only ListCommands is proxied to the daemon today. The remaining methods
// (RunCommand + the process lifecycle: ListProcesses/GetProcess/GetProcessLogs/
// KillProcess) are promoted unchanged from the embedded local PackageCommandsService.
// Those intersect a separately-paused effort to run and scope background
// processes on the daemon; wiring them through the daemon is a follow-up. The
// favorites RPCs (GetCommandFavorites/SetCommandFavorite) are DB-backed and
// correctly belong on the api-server, so delegating them is not a stopgap.
type PackageCommandsProxyService struct {
	daemonProxyBase
	// Embedded local service supplies every handler method by promotion; only
	// ListCommands below overrides it.
	*PackageCommandsService
	database db.Repository
}

// NewPackageCommandsProxyService creates a proxy that forwards ListCommands to
// the user's daemon and delegates all other RPCs to the local service.
func NewPackageCommandsProxyService(router toolexec.DaemonRouter, database db.Repository) *PackageCommandsProxyService {
	return &PackageCommandsProxyService{
		daemonProxyBase:        daemonProxyBase{router: router},
		PackageCommandsService: NewPackageCommandsService(database),
		database:               database,
	}
}

// pkgListCommandsCmd mirrors daemonruntime.pkgListCommandsRequest.
type pkgListCommandsCmd struct {
	WorkingDir string `json:"working_dir"`
}

// ListCommands forwards package-command discovery to the user's daemon and
// converts the result to proto. It resolves the working directory exactly as the
// local service does (worktree_id -> worktree.Path, else the explicit path), but
// runs discovery on the daemon's filesystem via the pkg.list_commands command.
func (s *PackageCommandsProxyService) ListCommands(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPackageCommandsRequest],
) (*connect.Response[reliantv1.ListPackageCommandsResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}

	workingDir, err := s.resolveWorkingDir(ctx, req.Msg)
	if err != nil {
		return nil, err
	}

	var result pkgmgr.CommandListResponse
	if err := s.dispatch(ctx, userID, "pkg.list_commands",
		pkgListCommandsCmd{WorkingDir: workingDir}, &result, pkgListCommandsTimeoutMs); err != nil {
		// dispatch already returns a loud Connect error (Unavailable for a dead
		// daemon / timeout / unknown command; NotFound for a missing dir). We
		// never fall through to an empty list here — that silent-empty result is
		// the bug this proxy exists to eliminate.
		return nil, err
	}

	return connect.NewResponse(packageCommandListToProto(&result)), nil
}

// resolveWorkingDir mirrors PackageCommandsService.ListCommands: prefer the
// worktree's stored path (a daemon-side path), else the caller-supplied path.
func (s *PackageCommandsProxyService) resolveWorkingDir(
	ctx context.Context,
	msg *reliantv1.ListPackageCommandsRequest,
) (string, error) {
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
		worktree, err := s.database.GetWorktree(ctx, worktreeID)
		if err != nil {
			return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found: %w", err))
		}
		return worktree.Path, nil
	case path != "":
		return path, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id or path is required"))
	}
}

// packageCommandListToProto converts a pkgmgr discovery result to the proto
// response, reusing convertCommandToProto so the proxy and the local service
// produce byte-identical output.
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
