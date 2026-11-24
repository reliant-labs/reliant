// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
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
// the machine holding it, by the rule the Files tab and terminal use
// (daemonplacement.WorkspaceDaemon): a branch workspace's owner, else the
// machine of the chat the panel is beside (chatID, "" for none), else the
// project-aware default.
func (s *Service) workspace(ctx context.Context, userID, worktreeID, chatID string) (path, daemonID string, err error) {
	worktree, err := s.deps.Placement.GetWorktree(ctx, worktreeID)
	if err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}
	// Another user's workspace reads like a missing one.
	if _, err := s.deps.Projects.GetProjectWithUserCheck(ctx, worktree.ProjectID, userID); err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	ws, err := daemonplacement.WorkspaceDaemon(ctx, s.workspaces(), userID, worktree.ProjectID, worktree.ID, chatID)
	if errors.Is(err, daemonplacement.ErrForeignChat) {
		return "", "", svcerr.NotFound("chat")
	}
	if errors.Is(err, daemonplacement.ErrForeignWorkspace) {
		return "", "", svcerr.NotFound("worktree")
	}
	if err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to resolve the workspace's machine"))
	}
	if ws.DaemonID != "" {
		return worktree.Path, ws.DaemonID, nil
	}
	// The main checkout (on every machine with the project) or a row no
	// machine settled: the project-aware default, never project-blind default
	// resolution, which may pick a machine without this project and run a
	// command in whatever sits at the same path there. The concrete machine is
	// carried through discovery, the start and every process lookup.
	// A failed lookup is reported, never routed around: falling back to
	// project-blind default resolution could run an unrelated command on a
	// machine without this project. Only "the user has no machine at all"
	// leaves the dispatch to report that, as default resolution would.
	if machines, ok := s.deps.Router.(daemonplacement.Machines); ok {
		daemonID, err := daemonplacement.ProjectDefaultDaemonID(ctx, s.deps.Placement, machines, userID, worktree.ProjectID)
		switch {
		case err == nil:
			return worktree.Path, daemonID, nil
		case errors.Is(err, toolexec.ErrNoDaemon):
			return worktree.Path, "", nil
		default:
			return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to resolve the project's machine"))
		}
	}
	return worktree.Path, "", nil
}

// requestedMachine checks a machine a request names (a process's daemon_id,
// from a list): it must be the caller's. "" is default resolution.
func (s *Service) requestedMachine(ctx context.Context, userID string, daemonID *string) (string, error) {
	if daemonID == nil || *daemonID == "" {
		return "", nil
	}
	d, err := s.deps.Placement.GetDaemon(ctx, *daemonID)
	if err != nil || !authz.CanWriteMachine(ctx, userID, d) {
		return "", svcerr.NotFound("machine")
	}
	return *daemonID, nil
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
		// An agent's own background commands are the Background panel's,
		// not this one's. A package command is marked as one, with or
		// without a workspace (a path-only RunCommand has none).
		if info == nil || (!info.PackageCommand && info.WorktreeID == "") {
			continue
		}
		out = append(out, packageProcessFromDaemon(info, daemonID))
	}
	return out, nil
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
