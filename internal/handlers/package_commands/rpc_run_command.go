package package_commands

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// RunCommand runs a package command on the machine holding its workspace.
//
// The command line is never the client's: the command is discovered on that
// machine (pkg.list_commands in the working directory) and run by name, so
// the request can only start what the project's Makefile, package.json or
// Taskfile declares.
func (s *Service) RunCommand(
	ctx context.Context,
	req *connect.Request[reliantv1.RunPackageCommandRequest],
) (*connect.Response[reliantv1.RunPackageCommandResponse], error) {
	if req.Msg.CommandName == "" {
		return nil, svcerr.InvalidArgument("command_name is required")
	}
	if req.Msg.PackageType == reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		return nil, svcerr.InvalidArgument("package_type is required")
	}
	packageType, ok := pkgmgrPackageTypeFromProto(req.Msg.PackageType)
	if !ok {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("package_type %q is not supported", req.Msg.PackageType.String()))
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}

	worktreeID := req.Msg.GetWorktreeId()
	var rootDir, daemonID string
	switch {
	case worktreeID != "":
		rootDir, daemonID, err = s.workspace(ctx, userID, worktreeID, req.Msg.GetChatId())
		if err != nil {
			return nil, err
		}
	case req.Msg.GetPath() != "":
		rootDir = req.Msg.GetPath()
	default:
		return nil, svcerr.InvalidArgument("worktree_id or path is required")
	}
	workingDir := rootDir
	if req.Msg.GetWorkingDir() != "" {
		workingDir = req.Msg.GetWorkingDir()
	}

	var discovered pkgmgr.CommandListResponse
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "pkg.list_commands",
		pkgListCommandsCmd{WorkingDir: workingDir}, &discovered, pkgListCommandsTimeoutMs); err != nil {
		return nil, err
	}
	command, ok := findCommand(&discovered, packageType, req.Msg.CommandName)
	if !ok {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound,
			fmt.Sprintf("%s command %q not found in %s", packageType, req.Msg.CommandName, workingDir))
	}

	var started struct {
		ProcessID string `json:"process_id"`
	}
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_start", daemon.RunCommandRequest{
		Command:        command.Command,
		WorkingDir:     workingDir,
		Env:            req.Msg.Env,
		WorktreeID:     worktreeID,
		PackageCommand: true,
	}, &started, pkgProcessTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.RunPackageCommandResponse{
		ProcessId: started.ProcessID,
		Command:   command.Command,
		StartTime: time.Now().UTC().Format(time.RFC3339),
		DaemonId:  daemonID,
	}), nil
}

// findCommand is the command named name of packageType declared in the
// discovery root itself (not a subdirectory, which has its own working_dir).
func findCommand(list *pkgmgr.CommandListResponse, packageType pkgmgr.PackageType, name string) (pkgmgr.Command, bool) {
	for _, cmd := range list.Commands[packageType] {
		if cmd.Name == name && cmd.RelativePath == "" {
			return cmd, true
		}
	}
	return pkgmgr.Command{}, false
}
