// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/processproto"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A package command runs on the machine that holds its workspace, as a
// background process of that machine's daemon (exec.bg_start), and every
// process rpc asks that machine about it. They used to run on THIS server's
// own shell, which has none of the user's files: discovery found nothing, and
// a command that did resolve would have run on the api-server.

const (
	// pkgProcessTimeoutMs bounds a start, a kill or a log read.
	pkgProcessTimeoutMs = 30_000
	// pkgListTimeoutMs bounds a process list: an in-memory table, so a list
	// that takes longer is a machine in trouble, not a long list.
	pkgListTimeoutMs = 5_000
)

// workspace is the caller's worktree a request names: its checkout path and
// the machine holding it ("" for default resolution — the main checkout, or a
// row no connected machine could settle).
func (s *Service) workspace(ctx context.Context, userID, worktreeID string) (path, daemonID string, err error) {
	worktree, err := s.deps.Repo.GetWorktree(ctx, worktreeID)
	if err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}
	// Another user's workspace reads like a missing one.
	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, worktree.ProjectID, userID); err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	ws, err := daemonplacement.WorkspaceDaemon(ctx, s.deps.Repo, s.owners, userID, worktree.ProjectID, worktree.ID, "")
	if err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to resolve the workspace's machine"))
	}
	return worktree.Path, ws.DaemonID, nil
}

// requestedMachine checks a machine a request names (a process's daemon_id,
// from a list): it must be the caller's. "" is default resolution.
func (s *Service) requestedMachine(ctx context.Context, userID string, daemonID *string) (string, error) {
	if daemonID == nil || *daemonID == "" {
		return "", nil
	}
	d, err := s.deps.Repo.GetDaemon(ctx, *daemonID)
	if err != nil || d == nil || d.UserID != userID {
		return "", svcerr.NotFound("machine")
	}
	return *daemonID, nil
}

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
		rootDir, daemonID, err = s.workspace(ctx, userID, worktreeID)
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
		Command:    command.Command,
		WorkingDir: workingDir,
		Env:        req.Msg.Env,
		WorktreeID: worktreeID,
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

// ListProcesses lists package-command processes: one workspace's, read from
// the machine holding it, or every connected machine's.
func (s *Service) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPackageProcessesRequest],
) (*connect.Response[reliantv1.ListPackageProcessesResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}

	worktreeID := req.Msg.GetWorktreeId()
	var processes []*reliantv1.PackageProcess
	if worktreeID != "" {
		_, daemonID, err := s.workspace(ctx, userID, worktreeID)
		if err != nil {
			return nil, err
		}
		// A list never wakes a machine: one that is asleep has no running
		// process to show.
		if daemonID != "" && !s.online(ctx, userID, daemonID) {
			return connect.NewResponse(&reliantv1.ListPackageProcessesResponse{}), nil
		}
		listed, err := s.listOn(ctx, userID, daemonID)
		if err != nil {
			return nil, err
		}
		for _, p := range listed {
			if p.GetWorktreeId() == worktreeID {
				processes = append(processes, p)
			}
		}
	} else {
		processes = s.listEverywhere(ctx, userID)
	}
	return connect.NewResponse(&reliantv1.ListPackageProcessesResponse{Processes: processes}), nil
}

// livenessProbe is the router's answer to "is this machine connected".
// *toolexec.NATSDaemonRouter implements it; a router that does not (a test
// double) is treated as having every machine connected.
type livenessProbe interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
}

func (s *Service) online(ctx context.Context, userID, daemonID string) bool {
	probe, ok := s.deps.Router.(livenessProbe)
	if !ok {
		return true
	}
	online, err := probe.IsDaemonOnline(ctx, userID, &toolexec.DaemonSelector{ID: daemonID})
	return err == nil && online
}

// listOn lists the package-command processes of one machine ("" for default
// resolution).
func (s *Service) listOn(ctx context.Context, userID, daemonID string) ([]*reliantv1.PackageProcess, error) {
	var infos []*daemon.ProcessInfo
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_list", struct{}{}, &infos, pkgListTimeoutMs); err != nil {
		return nil, err
	}
	var out []*reliantv1.PackageProcess
	for _, info := range infos {
		// An agent's own background commands carry no workspace; they are
		// the Background panel's, not this one's.
		if info == nil || info.WorktreeID == "" {
			continue
		}
		out = append(out, packageProcessFromDaemon(info, daemonID))
	}
	return out, nil
}

// listEverywhere lists the package-command processes of every connected
// machine of the user's, asking them at once. An asleep machine is skipped,
// and one that fails is left out and logged: the panel shows what it can.
// Without a router that reports liveness it lists the default machine.
func (s *Service) listEverywhere(ctx context.Context, userID string) []*reliantv1.PackageProcess {
	if _, ok := s.deps.Router.(livenessProbe); !ok {
		listed, err := s.listOn(ctx, userID, "")
		if err != nil {
			logging.WarnContext(ctx, "[PackageCommands] could not list processes", "error", err)
		}
		return listed
	}
	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[PackageCommands] could not list the user's machines", "error", err)
		return nil
	}
	var (
		mu  sync.Mutex
		all []*reliantv1.PackageProcess
		wg  sync.WaitGroup
	)
	for _, d := range daemons {
		if d == nil || d.UserID != userID || !s.online(ctx, userID, d.ID) {
			continue
		}
		wg.Add(1)
		go func(daemonID string) {
			defer wg.Done()
			listed, err := s.listOn(ctx, userID, daemonID)
			if err != nil {
				logging.WarnContext(ctx, "[PackageCommands] could not list a machine's processes", "daemonID", daemonID, "error", err)
				return
			}
			mu.Lock()
			all = append(all, listed...)
			mu.Unlock()
		}(d.ID)
	}
	wg.Wait()
	return all
}

// packageProcessFromDaemon is a daemon's process as the rpc returns it.
// asked is the machine the list was asked of; the daemon's own stamp wins.
func packageProcessFromDaemon(p *daemon.ProcessInfo, asked string) *reliantv1.PackageProcess {
	result := &reliantv1.PackageProcess{
		Id:         p.ID,
		Command:    p.Command,
		Status:     processproto.Status(p.Status),
		WorkingDir: p.WorkingDir,
		StartTime:  p.StartTime.Format(time.RFC3339),
		DaemonId:   asked,
	}
	if p.DaemonID != "" {
		result.DaemonId = p.DaemonID
	}
	if p.WorktreeID != "" {
		result.WorktreeId = proto.String(p.WorktreeID)
	}
	if p.EndTime != nil {
		result.EndTime = proto.String(p.EndTime.Format(time.RFC3339))
	}
	if p.ExitCode != nil {
		result.ExitCode = proto.Int32(int32(*p.ExitCode))
	}
	return result
}

// GetProcess returns one package-command process from its machine.
func (s *Service) GetProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessRequest],
) (*connect.Response[reliantv1.GetPackageProcessResponse], error) {
	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	listed, err := s.listOn(ctx, userID, daemonID)
	if err != nil {
		return nil, err
	}
	for _, p := range listed {
		if p.Id == req.Msg.ProcessId {
			return connect.NewResponse(&reliantv1.GetPackageProcessResponse{Process: p}), nil
		}
	}
	return nil, svcerr.NotFound("process")
}

// GetProcessLogs returns a package-command process's output, line by line
// with the stream each line came from.
func (s *Service) GetProcessLogs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessLogsRequest],
) (*connect.Response[reliantv1.GetPackageProcessLogsResponse], error) {
	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	var output daemon.ProcessOutput
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_output", struct {
		ProcessID string             `json:"process_id"`
		Opts      *daemon.OutputOpts `json:"opts"`
	}{ProcessID: req.Msg.ProcessId, Opts: &daemon.OutputOpts{Lines: true}}, &output, pkgProcessTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(processLogs(&output)), nil
}

// processLogs splits a daemon's output into the rpc's three views. A daemon
// that predates per-line streams sends one text, read as stdout.
func processLogs(output *daemon.ProcessOutput) *reliantv1.GetPackageProcessLogsResponse {
	lines := output.Lines
	if lines == nil && output.Output != "" {
		for _, text := range strings.Split(strings.TrimRight(output.Output, "\n"), "\n") {
			lines = append(lines, daemon.ProcessOutputLine{Stream: "stdout", Text: text})
		}
	}
	var stdout, stderr strings.Builder
	combined := make([]*reliantv1.OutputLine, len(lines))
	for i, line := range lines {
		combined[i] = &reliantv1.OutputLine{Type: processproto.OutputStream(line.Stream), Text: line.Text}
		target := &stdout
		if line.Stream == "stderr" {
			target = &stderr
		}
		target.WriteString(line.Text)
		target.WriteByte('\n')
	}
	return &reliantv1.GetPackageProcessLogsResponse{Stdout: stdout.String(), Stderr: stderr.String(), Combined: combined}
}

// KillProcess stops a package-command process on its machine. One that has
// already stopped is success: that is what was asked for.
func (s *Service) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillPackageProcessRequest],
) (*connect.Response[reliantv1.KillPackageProcessResponse], error) {
	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	var ignored struct{}
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_kill", struct {
		ProcessID string `json:"process_id"`
	}{ProcessID: req.Msg.ProcessId}, &ignored, pkgProcessTimeoutMs); err != nil {
		if strings.Contains(err.Error(), "is not running") {
			return connect.NewResponse(&reliantv1.KillPackageProcessResponse{Message: "Process was already stopped"}), nil
		}
		return nil, err
	}
	return connect.NewResponse(&reliantv1.KillPackageProcessResponse{Message: "Process killed successfully"}), nil
}
