// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/processproto"
)

// ============================================================================
// Helper Methods
// ============================================================================

// convertCommandToProto converts a pkgmgr.Command to proto format
func convertCommandToProto(cmd pkgmgr.Command) *reliantv1.PackageCommand {
	return &reliantv1.PackageCommand{
		Name:         cmd.Name,
		Description:  cmd.Description,
		Command:      cmd.Command,
		PackageType:  packageTypeFromString(string(cmd.PackageType)),
		Source:       cmd.Source,
		Category:     cmd.Category,
		Dependencies: cmd.Dependencies,
		WorkingDir:   cmd.WorkingDir,
		RelativePath: cmd.RelativePath,
	}
}

// convertProcessToPackageProcess converts a shell.BackgroundProcess to proto PackageProcess
func convertProcessToPackageProcess(p *shell.BackgroundProcess) *reliantv1.PackageProcess {
	result := &reliantv1.PackageProcess{
		Id:         p.ID,
		Command:    p.Command,
		Status:     processproto.Status(p.Status),
		WorkingDir: p.WorkingDir,
		StartTime:  p.StartTime.Format("2006-01-02T15:04:05Z07:00"),
	}

	if p.WorktreeID != "" {
		result.WorktreeId = proto.String(p.WorktreeID)
	}

	if p.EndTime != nil {
		endTime := p.EndTime.Format("2006-01-02T15:04:05Z07:00")
		result.EndTime = proto.String(endTime)
	}

	if p.ExitCode != nil {
		result.ExitCode = proto.Int32(int32(*p.ExitCode))
	}

	return result
}

// ============================================================================
// RPC Handlers
// ============================================================================

// RunCommand executes a package command.
//
// It runs the command on THIS server through Deps.Processes, as do the
// process rpcs below. Running and scoping them on the daemon is a separate,
// paused effort; the favorites rpcs are DB-backed and belong here.
func (s *Service) RunCommand(
	ctx context.Context,
	req *connect.Request[reliantv1.RunPackageCommandRequest],
) (*connect.Response[reliantv1.RunPackageCommandResponse], error) {
	if req.Msg.CommandName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("command_name is required"))
	}

	if req.Msg.PackageType == reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("package_type is required"))
	}

	// Determine working directory and worktree ID
	var workingDir string
	var rootDir string
	var worktreeID string

	if req.Msg.WorktreeId != nil && *req.Msg.WorktreeId != "" {
		worktreeID = *req.Msg.WorktreeId
		// Look up worktree to get its path
		worktree, err := s.deps.Repo.GetWorktree(ctx, worktreeID)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("worktree not found: %w", err))
		}
		rootDir = worktree.Path
	} else if req.Msg.Path != nil && *req.Msg.Path != "" {
		rootDir = *req.Msg.Path
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("worktree_id or path is required"))
	}

	// Use explicit working_dir if provided (for subdirectory commands)
	// Otherwise fall back to root directory
	if req.Msg.WorkingDir != nil && *req.Msg.WorkingDir != "" {
		workingDir = *req.Msg.WorkingDir
	} else {
		workingDir = rootDir
	}

	packageType, ok := pkgmgrPackageTypeFromProto(req.Msg.PackageType)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("package_type %q is not supported", req.Msg.PackageType.String()))
	}

	// Run the command
	result, err := s.deps.Processes.RunCommand(ctx, pkgmgr.RunRequest{
		WorktreeID:  worktreeID,
		WorkingDir:  workingDir,
		CommandName: req.Msg.CommandName,
		PackageType: packageType,
		Env:         req.Msg.Env,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("failed to run command: %w", err))
	}

	return connect.NewResponse(&reliantv1.RunPackageCommandResponse{
		ProcessId: result.ProcessID,
		Command:   result.Command,
		StartTime: result.StartTime.Format("2006-01-02T15:04:05Z07:00"),
	}), nil
}

// ListProcesses returns processes for a worktree, or all processes if no filter is provided
func (s *Service) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPackageProcessesRequest],
) (*connect.Response[reliantv1.ListPackageProcessesResponse], error) {
	worktreeID := req.Msg.GetWorktreeId()

	var processes []*shell.BackgroundProcess
	if worktreeID != "" {
		processes = s.deps.Processes.GetProcessesByWorktree(worktreeID)
	} else {
		// No filter - return all processes
		processes = s.deps.Processes.GetAllProcesses()
	}

	protoProcesses := make([]*reliantv1.PackageProcess, len(processes))
	for i, p := range processes {
		protoProcesses[i] = convertProcessToPackageProcess(p)
	}

	return connect.NewResponse(&reliantv1.ListPackageProcessesResponse{
		Processes: protoProcesses,
	}), nil
}

// GetProcess returns a specific process
func (s *Service) GetProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessRequest],
) (*connect.Response[reliantv1.GetPackageProcessResponse], error) {
	processID := req.Msg.ProcessId
	if processID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	process, err := s.deps.Processes.GetProcess(processID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(&reliantv1.GetPackageProcessResponse{
		Process: convertProcessToPackageProcess(process),
	}), nil
}

// GetProcessLogs returns the logs for a process
func (s *Service) GetProcessLogs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessLogsRequest],
) (*connect.Response[reliantv1.GetPackageProcessLogsResponse], error) {
	processID := req.Msg.ProcessId
	if processID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Get separate stdout/stderr for backwards compatibility
	stdout, stderr, err := s.deps.Processes.GetProcessOutput(processID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Get combined interleaved output
	combinedOutput, err := s.deps.Processes.GetCombinedOutput(processID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Convert to proto format
	protoCombined := make([]*reliantv1.OutputLine, len(combinedOutput))
	for i, line := range combinedOutput {
		protoCombined[i] = &reliantv1.OutputLine{
			Type: processproto.OutputStream(line.Type),
			Text: line.Text,
		}
	}

	return connect.NewResponse(&reliantv1.GetPackageProcessLogsResponse{
		Stdout:   stdout,
		Stderr:   stderr,
		Combined: protoCombined,
	}), nil
}

// KillProcess terminates a process
func (s *Service) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillPackageProcessRequest],
) (*connect.Response[reliantv1.KillPackageProcessResponse], error) {
	processID := req.Msg.ProcessId
	if processID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	err := s.deps.Processes.KillProcess(processID)
	if err != nil {
		// If process is already dead, treat as success - the desired state is achieved
		if strings.Contains(err.Error(), "is not running") {
			return connect.NewResponse(&reliantv1.KillPackageProcessResponse{
				Message: "Process was already stopped",
			}), nil
		}
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("failed to kill process: %w", err))
	}

	return connect.NewResponse(&reliantv1.KillPackageProcessResponse{
		Message: "Process killed successfully",
	}), nil
}

// GetCommandFavorites returns the list of favorited command keys for a project
func (s *Service) GetCommandFavorites(
	ctx context.Context,
	req *connect.Request[reliantv1.GetCommandFavoritesRequest],
) (*connect.Response[reliantv1.GetCommandFavoritesResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("project_id is required"))
	}

	// Verify project exists and belongs to user
	_, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("project not found: %w", err))
	}

	// Get favorites from database
	commandKeys, err := s.deps.Repo.ListCommandFavorites(ctx, userID, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("failed to list command favorites: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetCommandFavoritesResponse{
		CommandKeys: commandKeys,
	}), nil
}

// SetCommandFavorite adds or removes a command from favorites
func (s *Service) SetCommandFavorite(
	ctx context.Context,
	req *connect.Request[reliantv1.SetCommandFavoriteRequest],
) (*connect.Response[reliantv1.SetCommandFavoriteResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("project_id is required"))
	}

	commandKey := req.Msg.CommandKey
	if commandKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("command_key is required"))
	}

	// Verify project exists and belongs to user
	_, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("project not found: %w", err))
	}

	// Add or remove favorite based on is_favorite flag
	if req.Msg.IsFavorite {
		err = s.deps.Repo.AddCommandFavorite(ctx, userID, projectID, commandKey)
	} else {
		err = s.deps.Repo.RemoveCommandFavorite(ctx, userID, projectID, commandKey)
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("failed to update command favorite: %w", err))
	}

	return connect.NewResponse(&reliantv1.SetCommandFavoriteResponse{
		Success: true,
	}), nil
}
