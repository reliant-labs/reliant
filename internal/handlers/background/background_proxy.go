// Copyright (c) 2025 Reliant Labs
package background

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/processproto"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// bgListTimeoutMs bounds the daemon round trip for a process LIST.
//
// Listing reads the daemon's in-memory process table plus one batched port
// scan; its duration does not depend on the user's commands, so it does not
// need start/kill's 30s budget. A list that can pin an API request — and a
// database-free but pool-bound handler — for 30 seconds is a liveness problem,
// and the caller is a UI list that will simply ask again.
const bgListTimeoutMs int32 = 5_000

// Every rpc forwards its request to the caller's daemon through Deps.Router:
// a DaemonCommand (request/response), or for StreamProcessOutput the
// daemon's output subscription.

func (s *Service) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

// sendCommand sends one command to daemonID, or by default resolution when
// it is "".
func (s *Service) sendCommand(ctx context.Context, userID, daemonID, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	var respBytes []byte
	if daemonID == "" {
		respBytes, err = s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	} else {
		respBytes, err = s.deps.Router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
	}
	if err != nil {
		return daemonFailure("daemon command "+commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}
	return nil
}

// daemonFailure is a failed daemon round trip as the svcerr kind the rpc
// returns. A machine that is starting, asleep or absent is not a server
// fault: Unavailable while it is on its way, FailedPrecondition when the user
// has none (daemonerr.MachineState; ListProcesses used to return the second as
// Internal, ELECTRON-A6). A daemon older than this server is FailedPrecondition
// in the daemon's own words (daemonerr.Outdated). Anything else is Internal,
// saying what was being done: the daemon's own words reach the user, the
// router's and the transport's stay on the error as its logged cause.
func daemonFailure(doing string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState("", err); ok {
		return serr
	}
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg += ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}

// processNotRunningMarker is the phrase the daemon's BackgroundManager.KillProcess
// writes for a process that already exited ("process <id> is not running
// (status: …)"). Daemon-command errors cross NATS as text, so the text is the
// wire contract between the two (as in daemonerr's markers).
const processNotRunningMarker = "is not running"

// processNotRunning reclassifies a kill of a process that had already exited:
// not a server fault (it answered Internal) but FailedPrecondition with the
// process_not_running reason, which the web treats as "already stopped" and
// refreshes. The message is unchanged — older clients match on it.
func processNotRunning(err error) error {
	text, ok := toolexec.DaemonReportedText(err)
	if !ok || !strings.Contains(text, processNotRunningMarker) {
		return err
	}
	return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(svcerr.ToConnect(err).Message()), err),
		errreason.ProcessNotRunning)
}

// ListProcesses returns the caller's background processes.
//
// With chat_id it is the processes that chat's agents started, read from the
// machine the chat runs on. Without it, every connected machine of the
// caller's is asked at once (as the package-command list does), so the one
// list a client keeps covers every machine and each row names the machine to
// send its output and kill requests to.
func (s *Service) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBackgroundProcessesRequest],
) (*connect.Response[reliantv1.ListBackgroundProcessesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	var processes []*reliantv1.BackgroundProcess
	if chatID := req.Msg.GetChatId(); chatID != "" {
		processes, err = s.listForChat(ctx, userID, chatID)
		if err != nil {
			return nil, err
		}
	} else {
		processes, err = s.listEverywhere(ctx, userID)
		if err != nil {
			return nil, err
		}
	}
	if worktreeID := req.Msg.GetWorktreeId(); worktreeID != "" {
		kept := processes[:0]
		for _, p := range processes {
			if p.GetWorktreeId() == worktreeID {
				kept = append(kept, p)
			}
		}
		processes = kept
	}

	return connect.NewResponse(&reliantv1.ListBackgroundProcessesResponse{
		Processes: processes,
	}), nil
}

// listForChat lists the processes chatID's agents started, on the machine
// the chat runs on. Another user's chat reads like a missing one, a shared
// chat's viewer included: the processes run on the owner's machine.
func (s *Service) listForChat(ctx context.Context, userID, chatID string) ([]*reliantv1.BackgroundProcess, error) {
	chat, err := s.deps.Repo.GetChat(ctx, chatID)
	if errors.Is(err, core.ErrChatNotFound) || (err == nil && !authz.CanWriteChat(ctx, userID, chat)) {
		return nil, svcerr.NotFound("chat")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	daemonID, err := daemonplacement.ChatDaemonID(ctx, s.deps.Repo, chat)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the chat's machine"))
	}
	// A list never wakes a machine: one that is asleep has no running process.
	if daemonID != "" && !s.online(ctx, userID, daemonID) {
		return nil, nil
	}
	listed, err := s.listOn(ctx, userID, daemonID)
	if err != nil {
		return nil, err
	}
	var out []*reliantv1.BackgroundProcess
	for _, p := range listed {
		if p.GetChatId() == chatID {
			out = append(out, p)
		}
	}
	return out, nil
}

// listOn lists every background process of one machine ("" for default
// resolution).
func (s *Service) listOn(ctx context.Context, userID, daemonID string) ([]*reliantv1.BackgroundProcess, error) {
	var infos []*daemon.ProcessInfo
	if err := s.sendCommand(ctx, userID, daemonID, "exec.bg_list", struct{}{}, &infos, bgListTimeoutMs); err != nil {
		return nil, err
	}
	out := make([]*reliantv1.BackgroundProcess, 0, len(infos))
	for _, info := range infos {
		if info != nil {
			out = append(out, backgroundProcessFromDaemon(info, daemonID))
		}
	}
	return out, nil
}

// livenessProbe is the router's answer to "is this machine connected".
// *toolexec.NATSDaemonRouter implements it; a router that does not (a test
// double) is treated as having every machine connected, and an unscoped list
// then asks the default machine alone.
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

// listEverywhere lists the processes of every connected machine of the
// caller's, asking them at once. An asleep machine is skipped, and one that
// fails is left out and logged: the list shows what it can. Only when no
// machine could be asked at all does the default machine's answer (and its
// error, which says why) stand, as it did before lists spanned machines.
func (s *Service) listEverywhere(ctx context.Context, userID string) ([]*reliantv1.BackgroundProcess, error) {
	if _, ok := s.deps.Router.(livenessProbe); !ok {
		return s.listOn(ctx, userID, "")
	}
	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[Background] could not list the user's machines; asking the default one", "error", err)
		return s.listOn(ctx, userID, "")
	}
	var targets []string
	for _, d := range daemons {
		if d != nil && authz.CanReadMachine(ctx, userID, d) && s.online(ctx, userID, d.ID) {
			targets = append(targets, d.ID)
		}
	}
	if len(targets) == 0 {
		return s.listOn(ctx, userID, "")
	}
	var (
		mu  sync.Mutex
		all []*reliantv1.BackgroundProcess
		wg  sync.WaitGroup
	)
	for _, daemonID := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			listed, err := s.listOn(ctx, userID, daemonID)
			if err != nil {
				logging.WarnContext(ctx, "[Background] could not list a machine's processes", "daemonID", daemonID, "error", err)
				return
			}
			mu.Lock()
			all = append(all, listed...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return all, nil
}

// backgroundProcessFromDaemon is a daemon's process as the rpc returns it.
// asked is the machine the list was asked of; the daemon's own stamp wins.
func backgroundProcessFromDaemon(p *daemon.ProcessInfo, asked string) *reliantv1.BackgroundProcess {
	proc := &reliantv1.BackgroundProcess{
		Id:         p.ID,
		Command:    p.Command,
		Status:     processproto.Status(p.Status),
		StartTime:  p.StartTime.Format(time.RFC3339),
		WorkingDir: p.WorkingDir,
		KeepAlive:  p.KeepAlive,
		Ports:      portsFromDaemon(p.Ports),
	}
	if p.ExitCode != nil {
		proc.ExitCode = proto.Int32(int32(*p.ExitCode))
	}
	if p.EndTime != nil {
		proc.EndTime = proto.String(p.EndTime.Format(time.RFC3339))
	}
	if daemonID := cmp.Or(p.DaemonID, asked); daemonID != "" {
		proc.DaemonId = proto.String(daemonID)
	}
	if p.WorktreeID != "" {
		proc.WorktreeId = proto.String(p.WorktreeID)
	}
	if p.ChatID != "" {
		proc.ChatId = proto.String(p.ChatID)
	}
	if p.ThreadID != "" {
		proc.ThreadId = proto.String(p.ThreadID)
	}
	if p.StopsAfter != nil {
		proc.StopsAfter = proto.String(p.StopsAfter.Format(time.RFC3339))
	}
	if p.StopExempt != "" {
		proc.StopExempt = proto.String(p.StopExempt)
	}
	if !p.LastActive.IsZero() {
		proc.LastActive = proto.String(p.LastActive.Format(time.RFC3339))
	}
	return proc
}

func portsFromDaemon(ports []daemon.PortInfo) []*reliantv1.PortInfo {
	if len(ports) == 0 {
		return nil
	}
	out := make([]*reliantv1.PortInfo, len(ports))
	for i, p := range ports {
		out[i] = &reliantv1.PortInfo{Port: int32(p.Port), Protocol: p.Protocol, State: p.State, Address: p.Address}
	}
	return out
}

// requestedMachine checks a machine a request names (a process's daemon_id,
// from a list): it must be one the caller may act on (write) or see (read).
// "" is default resolution.
func (s *Service) requestedMachine(ctx context.Context, userID, daemonID string, write bool) (string, error) {
	if daemonID == "" {
		return "", nil
	}
	d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil || d == nil {
		return "", svcerr.NotFound("machine")
	}
	allowed := authz.CanReadMachine(ctx, userID, d)
	if write {
		allowed = authz.CanWriteMachine(ctx, userID, d)
	}
	if !allowed {
		return "", svcerr.NotFound("machine")
	}
	return daemonID, nil
}

// GetProcessOutput returns the stdout/stderr output of a process.
func (s *Service) GetProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProcessOutputRequest],
) (*connect.Response[reliantv1.GetProcessOutputResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.GetDaemonId(), false)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"process_id": req.Msg.ProcessId,
	}

	var cmdResp struct {
		Output     string `json:"output"`
		HasMore    bool   `json:"has_more"`
		NextOffset int    `json:"next_offset"`
		TotalBytes int    `json:"total_bytes"`
	}
	if err := s.sendCommand(ctx, userID, daemonID, "exec.bg_output", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetProcessOutputResponse{
		Stdout: cmdResp.Output,
	}), nil
}

// KillProcess terminates a background process.
func (s *Service) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillProcessRequest],
) (*connect.Response[reliantv1.KillProcessResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.GetDaemonId(), true)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"process_id": req.Msg.ProcessId,
	}

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, daemonID, "exec.bg_kill", cmdReq, &cmdResp, 30000); err != nil {
		return nil, processNotRunning(err)
	}

	return connect.NewResponse(&reliantv1.KillProcessResponse{
		Message: "Process killed successfully",
	}), nil
}

// StreamProcessOutput streams real-time process output by subscribing to the
// daemon's output channel via the router.
func (s *Service) StreamProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamProcessOutputRequest],
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
) error {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return err
	}

	if req.Msg.ProcessId == "" {
		return svcerr.InvalidArgument("process_id is required")
	}

	// A process lives on one machine, which must be one the caller may see.
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.GetDaemonId(), false)
	if err != nil {
		return err
	}
	if daemonID != "" {
		ctx = toolexec.WithDaemonSelector(ctx, &toolexec.DaemonSelector{ID: daemonID})
	}
	outputCh, unsub, err := s.deps.Router.SubscribeProcessOutput(ctx, userID, req.Msg.ProcessId, req.Msg.NewOnly)
	if err != nil {
		return daemonFailure("subscribe process output", err)
	}
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-outputCh:
			if !ok {
				// Channel closed — subscription ended.
				return nil
			}

			if evt.IsComplete {
				exitCode := evt.ExitCode
				if err := stream.Send(&reliantv1.ProcessOutputEvent{
					Event: &reliantv1.ProcessOutputEvent_Complete{
						Complete: &reliantv1.ProcessOutputComplete{
							ExitCode: &exitCode,
						},
					},
				}); err != nil {
					return err
				}
				return nil
			}

			streamType := reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDOUT
			if evt.Stream == "stderr" {
				streamType = reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR
			}

			if err := stream.Send(&reliantv1.ProcessOutputEvent{
				Event: &reliantv1.ProcessOutputEvent_Line{
					Line: &reliantv1.ProcessOutputLine{
						Type:     streamType,
						Text:     evt.Data,
						Sequence: int64(evt.Sequence),
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}
