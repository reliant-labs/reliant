// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
)

// ErrDaemonPending means a daemon record exists for this user (it was
// created and is provisioning) but has not yet connected. Distinct from a
// resolution failure meaning no daemon exists at all: callers that see this
// error should tell the user to wait, not surface a terminal failure.
//
// errors.Is is the Go-level contract: any router path that discovers "a
// daemon exists but nothing is attached yet" wraps this sentinel so callers
// can test for it without parsing message text (IsDaemonPending below).
//
// The message text ALSO deliberately contains "no daemon connected" — the
// same marker daemonRequestError already produces for a daemon that
// registered but dropped its NATS subscription (see isDaemonConnectingError
// on the frontend). That reuse is intentional: it puts the "record exists,
// still starting" case into the exact wait/retry family the frontend
// already renders (classifyDaemonWait + useDaemonWait's poll loop), instead
// of inventing a second wire format the client would need new plumbing to
// understand.
//
// It is a USER error (svcerr.ClassUser): the user's machine is starting,
// asleep or suspended, and the system is behaving correctly by saying so. It
// logs at INFO and never reaches Sentry — even under the Unavailable code
// callers give it — while errors.Is(err, ErrDaemonPending) is unchanged.
//
// Its kind is Unavailable — retryable, what the web's daemon-wait machinery
// expects — so a handler's bare svcerr.Wrap answers CodeUnavailable with the
// marker in the message, never Internal "internal server error".
//
// Its reason is errreason.DaemonStarting (x-forge-error-reason), which the web
// routes on; the marker text stays for clients from before the reason.
var ErrDaemonPending = svcerr.WithReason(svcerr.WithClass(svcerr.Unavailable("no daemon connected: daemon record exists but has not registered yet (still starting)"), svcerr.ClassUser), errreason.DaemonStarting)

// IsDaemonPending reports whether err indicates a daemon exists for the user
// but has not connected yet (still provisioning) — as opposed to no daemon
// existing at all. Callers use this to choose between a retryable "still
// starting" state and a terminal error.
func IsDaemonPending(err error) bool {
	return errors.Is(err, ErrDaemonPending)
}

// ErrDaemonSuspended is ErrDaemonPending for a machine that is asleep rather
// than starting. errors.Is(err, ErrDaemonPending) holds for it, so everything
// that waits on a pending machine still does; only its text differs, and says
// what is true.
//
// Wrapping ErrDaemonPending itself described a suspended machine as starting:
// "...is suspended and will wake when you next message it: no daemon
// connected: daemon record exists but has not registered yet (still
// starting)". Agents whose machine was suspended mid-task read the second half
// and reported a daemon that "never re-registers" (prod 2026-10-11, GO-12 to
// GO-19) instead of a machine that was asleep and needed waking.
//
// The text keeps the "no daemon connected" marker (daemonoffline.ErrorSubstring)
// and the word "suspended", which together are what the web's machine-wait
// classification keys on (frontends/web/src/lib/daemon-errors.ts), even where a surface
// shows this sentinel without the router's wrapping.
//
// It is ErrDaemonPending under its own message (svcerr.WithMessage), so the
// text a client reads through svcerr is this one, not the pending sentinel's.
// Its reason, errreason.DaemonSuspended, is the nearest on its chain, so it
// wins over the pending sentinel's.
var ErrDaemonSuspended = svcerr.WithReason(svcerr.WithMessage(ErrDaemonPending, "no daemon connected: the machine is asleep (suspended)"), errreason.DaemonSuspended)

// IsDaemonSuspended reports whether err says the machine is suspended, as
// opposed to still starting. Both are IsDaemonPending.
func IsDaemonSuspended(err error) bool {
	return errors.Is(err, ErrDaemonSuspended)
}

// ErrNoDaemon means no machine for the request exists at all: the user has
// none registered, or the selector names none of theirs. Unlike
// ErrDaemonPending there is nothing to wait for, so a surface shows "no
// machine" rather than a spinner. Its text deliberately lacks
// daemonoffline.ErrorSubstring ("no daemon connected"): it is not a machine
// that went offline. A USER error (svcerr.ClassUser), like ErrDaemonPending,
// of kind FailedPrecondition: waiting cannot produce a machine the user has not
// connected.
// Its reason is errreason.NoDaemon.
var ErrNoDaemon = svcerr.WithReason(svcerr.WithClass(svcerr.FailedPrecondition("no daemon available"), svcerr.ClassUser), errreason.NoDaemon)

// errDaemonNotConnected is the cause daemonRequestError reports when NATS had
// no responder for the daemon's subject: the daemon is not connected. A USER
// error — the machine is closed or restarting — whose text carries
// daemonoffline.ErrorSubstring, the marker every surface keys on.
// Its reason is errreason.DaemonNotConnected.
var errDaemonNotConnected = svcerr.WithReason(svcerr.WithClass(svcerr.Unavailable("no daemon connected for user"), svcerr.ClassUser), errreason.DaemonNotConnected)

// machineState puts sentence, the router's account of a machine state, in
// front of state's own text as the message a client reads through svcerr —
// which publishes the nearest message on the chain, never an outer
// fmt.Errorf. Error() reads the same, and errors.Is(err, state) holds.
func machineState(sentence string, state error) error {
	return svcerr.WithMessage(state, sentence+": "+state.Error())
}

// noMachine is ErrNoDaemon followed by sentence, as machineState does it for
// a state whose marker leads.
func noMachine(sentence string) error {
	return svcerr.WithMessage(ErrNoDaemon, ErrNoDaemon.Error()+": "+sentence)
}

// IsNoDaemon reports whether err is ErrNoDaemon: the user has no machine for
// this request.
func IsNoDaemon(err error) bool {
	return errors.Is(err, ErrNoDaemon)
}

// ErrDaemonTimeout means a command reached a connected daemon (NATS had a
// responder, else it would be errDaemonNotConnected) and its deadline passed
// before the daemon answered: the machine is up but slow, saturated or
// wedged. Both ways a deadline ends a daemon command wrap it — the NATS
// request's own timeout and the caller's context deadline.
//
// It is a server error, deliberately NOT marked ClassUser: a machine that
// stops answering may be ours to fix (a managed cloud machine whose CPU or
// disk is exhausted), so it stays visible. But its kind is DeadlineExceeded
// (it wraps svcerr.ErrDeadlineExceeded), and a deadline says "try again", not
// "page someone": forge logs a DeadlineExceeded failure at WARN, which never
// reaches Sentry. Surfaced as Internal instead, one saturated 4-vCPU cloud
// machine (load average 40) turned its file-tree polls into ERRORs and the
// "[gRPC] Request error" Sentry page of 2026-10-10.
var ErrDaemonTimeout = svcerr.DeadlineExceeded("the machine did not answer in time")

// IsDaemonTimeout reports whether err is ErrDaemonTimeout: the daemon is
// connected but did not answer a command before its deadline.
func IsDaemonTimeout(err error) bool {
	return errors.Is(err, ErrDaemonTimeout)
}

// TerminalOutputEvent represents a terminal output message or session lifecycle event.
type TerminalOutputEvent struct {
	SessionID string
	Data      []byte
	Closed    bool   // true when the terminal session has closed
	ExitCode  int32  // meaningful when Closed is true
	Error     string // non-empty on error
}

// ProcessOutputEvent represents a chunk of process output.
type ProcessOutputEvent struct {
	ProcessID  string
	Data       string
	Stream     string // "stdout" or "stderr"
	Sequence   uint64
	IsComplete bool
	ExitCode   int32
}

// Transport-failure error codes. These say the request did not complete a
// round trip to a daemon, so no result of the tool's came back. They are
// emitted by the transport itself, except ErrorCodeDaemonRestarted:
//
//	ErrorCodeDaemonUnreached   — RemoteExecutor, when the router call failed
//	                             (no daemon connected, NATS timeout, ...)
//	ErrorCodeDaemonRoundTrip   — the NATS bridge, when the gateway could not
//	                             complete the round trip (overloaded, a request
//	                             that did not reassemble)
//	ErrorCodeDaemonInterrupted — the NATS bridge, when the daemon's connection
//	                             closed while the request was in flight: the
//	                             daemon may have run it, and may still be
//	ErrorCodeDaemonRestarted   — the daemon, once restarted, for a call it was
//	                             running when its previous process died
//
// Everything the TOOL reports about its own execution — including a command
// that exited non-zero — comes back Success=true / IsError=false with the exit
// code inside the payload, or with a tool-owned code (EXECUTION_ERROR,
// PARSE_ERROR, TOOL_NOT_FOUND, CANCELLED). That is the whole distinction
// IsTransportErrorCode draws, and it is what keeps "the daemon vanished" from
// being recorded as "your lint failed".
const (
	ErrorCodeDaemonUnreached   = "DAEMON_EXECUTION_ERROR"
	ErrorCodeDaemonRoundTrip   = "DAEMON_ERROR"
	ErrorCodeDaemonInterrupted = "DAEMON_INTERRUPTED"
	// ErrorCodeDaemonRestarted is the daemon's own answer, after it restarted,
	// for a call that was running when its previous process died. A running
	// call cannot be resumed, so the outcome is unknown. It comes from the
	// daemon (it persisted the fact), not from a transport.
	ErrorCodeDaemonRestarted = "DAEMON_RESTARTED"
)

// IsTransportErrorCode reports whether an ErrorCode means no result came back
// from the tool, as opposed to ran-and-failed. Callers that turn a tool result
// into a command verdict MUST branch on this: a lane whose result never came
// back is not a lane that failed, and must not consume a retry or be reported
// as a lint/test/build failure.
func IsTransportErrorCode(code string) bool {
	switch code {
	case ErrorCodeDaemonUnreached, ErrorCodeDaemonRoundTrip, ErrorCodeDaemonInterrupted, ErrorCodeDaemonRestarted:
		return true
	default:
		return false
	}
}

// ErrDaemonConnectionInterrupted means the daemon's connection closed while a
// request to it was in flight, so its outcome is unknown: the daemon runs each
// request independently of its stream and may finish it after it redials. The
// gateway wraps it; the bridge carries it to the requester as
// ErrorCodeDaemonInterrupted, and the router decides from the request whether
// asking again after the reconnect is safe (see daemon_reconnect.go).
var ErrDaemonConnectionInterrupted = errors.New("daemon connection interrupted")

// ErrNotDispatched marks a tool request failure that proves the request never
// reached a daemon: no machine resolved, or NATS had no responder for every
// round trip made. Nothing ran, so sending it again is safe for any tool,
// shell included. A failure without it — a timeout, an interrupted
// connection, a cancelled wait — says nothing about whether the request ran.
// Match it with errors.Is; the error's message is the underlying failure's.
var ErrNotDispatched = errors.New("the request never reached a machine")

type notDispatchedError struct{ err error }

func (e *notDispatchedError) Error() string        { return e.err.Error() }
func (e *notDispatchedError) Unwrap() error        { return e.err }
func (e *notDispatchedError) Is(target error) bool { return target == ErrNotDispatched }

func notDispatched(err error) error { return &notDispatchedError{err: err} }

// TransportError says a tool request never reached a daemon. It exists so
// callers that would otherwise flatten a failure into an exit code can tell
// "never ran" from "ran and failed" with errors.As rather than by scanning a
// message.
type TransportError struct {
	Code    string // one of the transport-failure codes above
	Command string // the command whose result never came back, for the operator
	Detail  string // the transport's own message
	// NotDispatched: the request provably never reached a daemon, so the
	// command never ran (ToolResult.NotDispatched). Otherwise the daemon may
	// have run it — a timeout or a dropped connection loses only the answer.
	NotDispatched bool
}

func (e *TransportError) Error() string {
	if e.NotDispatched {
		return "daemon transport failure (" + e.Code + "): the command never ran: " + e.Detail
	}
	return "daemon transport failure (" + e.Code + "): no result came back, the command may have run: " + e.Detail
}

// ToolExecutionResponse is the synchronous response from a daemon tool execution.
type ToolExecutionResponse struct {
	RequestID    string `json:"request_id"`
	Success      bool   `json:"success"`
	IsError      bool   `json:"is_error"`
	Content      string `json:"content"`
	Metadata     string `json:"metadata,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Backgrounded bool   `json:"backgrounded,omitempty"`
	// DaemonID is the daemon that answered, stamped by the gateway from the
	// connection the response arrived on. A backgrounded call records it: the
	// process lives in that daemon's memory, and a user can have several.
	DaemonID string `json:"daemon_id,omitempty"`
}

// DaemonRouter abstracts all daemon-bound operations behind a transport-agnostic interface.
// Implementation:
//   - NATSDaemonRouter: routes via NATS request-reply to the api-server holding the connection
type DaemonRouter interface {
	// IsDaemonOnline checks if a daemon is connected for the given user. A nil
	// selector asks about ANY of the user's daemons; a non-nil one asks about
	// the daemon(s) it selects, so a run pinned to a suspended daemon is not
	// reported online because a different one is up.
	// Returns (online, nil) for definitive results, or (false, err) for infrastructure failures.
	IsDaemonOnline(ctx context.Context, userID string, selector *DaemonSelector) (bool, error)

	// SendToolRequest routes a tool execution request to the user's daemon (fire-and-forget).
	SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error

	// SendToolRequestSync routes a tool execution request to the user's daemon and waits
	// for the result. Used for tools with Placement == PlacementDaemon.
	SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error)

	// SendToolRequestSyncWithSelector routes a tool execution request to a specific daemon
	// matching the given selector. Falls back to SendToolRequestSync behavior if selector is nil.
	SendToolRequestSyncWithSelector(ctx context.Context, userID string, request *ToolExecutionRequest, selector *DaemonSelector) (*ToolExecutionResponse, error)

	// SendToolExecutionCancel cancels a running tool execution.
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error

	// SendToolExecutionBackground detaches a running tool execution into a
	// background process. The execution lives in the daemon, so the request has
	// to cross the wire exactly as cancellation does — an in-memory signal in
	// the API server is invisible to the process actually running the command.
	SendToolExecutionBackground(ctx context.Context, userID, requestID, toolCallID string) error

	// SendKillProcess sends a kill signal to a background process on the user's daemon.
	SendKillProcess(ctx context.Context, userID, processID string) error

	// SendDaemonCommand sends a generic command to the user's daemon and waits for a response.
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

	// SendDaemonCommandToDaemon sends a generic command to a SPECIFIC daemon id,
	// bypassing default resolution. Use when the target daemon is already known
	// so a multi-command operation and any recorded owner id all agree on one
	// daemon (e.g. every worktree.create for a worktree, plus the daemon_id
	// persisted on the row).
	SendDaemonCommandToDaemon(ctx context.Context, userID string, daemonID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

	// ResolveDaemonID returns the daemon id that SendDaemonCommand would route
	// to for this user (connected/local preferred, then control plane, then DB
	// fallback). Returns an error when no daemon can be resolved. Callers use
	// this to record which daemon an operation ran against (e.g. a
	// project_daemons row) so resolution and routing stay consistent.
	ResolveDaemonID(ctx context.Context, userID string) (string, error)

	// EnqueueDaemonCommand persists a fire-and-forget command to the
	// DAEMON_PENDING_COMMANDS JetStream stream for each of the user's
	// daemons. The gateway drains the stream on the daemon's next connect
	// and replays the command. Use for writes that must eventually run on
	// the daemon's filesystem (e.g. ensure-dir) when the daemon may not be
	// online right now — synchronous responses are not available, so this
	// is unsuitable for reads.
	//
	// Returns the number of daemons the command was enqueued for. Zero is
	// not an error: it just means the user has no managed daemons yet
	// (caller should be tolerant — the directory will be created on the
	// next successful create flow).
	EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error)

	// SendLoadProjectConfigs asks the daemon to load and send project configs.
	SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error

	// SendWatchProjectConfigs asks the daemon to start watching a project for config changes.
	SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error

	// SendTerminalInput sends raw PTY input bytes to a daemon terminal session.
	SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error

	// SendTerminalResize sends a terminal resize request to a daemon terminal session.
	SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error

	// SubscribeTerminalOutput subscribes to terminal output for a session.
	// Returns a channel that receives output events, an unsubscribe function, and an error.
	SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error)

	// SubscribeProcessOutput subscribes to a process's output stream.
	// If newOnly is true, only new output is delivered (existing buffered output is skipped).
	// Returns a channel that receives output events, an unsubscribe function, and an error.
	SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error)

	// OpenLocalModelHTTP relays one HTTP request to a local model endpoint on
	// the named daemon's machine and returns the ordered response chunks.
	// Closing the stream (or cancelling ctx) aborts the request.
	OpenLocalModelHTTP(ctx context.Context, userID, daemonID string, req *reliantv1.LocalModelHTTPRequest) (*LocalModelHTTPStream, error)

	// RefreshLocalModels asks the daemon to re-probe its local model servers
	// and waits (about 10s) for the inventory it sends back.
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error)

	// Close cleans up resources.
	Close() error
}

// DaemonConnectionListener is notified when daemon connections change.
// Used by NATSToolBridge to manage per-daemon NATS subscriptions and by
// daemonevents.Publisher to mirror lifecycle to the control plane.
type DaemonConnectionListener interface {
	OnDaemonConnected(userID, daemonID string)
	OnDaemonDisconnected(userID, daemonID string)
}

// DaemonConnectionInfoListener is an optional extension of
// DaemonConnectionListener for listeners that need the identity the daemon
// asserted at registration time (name/hostname/platform), not just the ids.
// ToolsDaemonService type-asserts each registered listener against this
// interface and calls it alongside OnDaemonConnected when satisfied, so
// existing listeners (NATSToolBridge, daemonquery.Responder/UserResponder)
// that only care about ids need no change. daemonevents.Publisher is the
// only current implementor — it needs this to carry a usable machine name
// through to the control plane, which OnDaemonConnected's two bare ids
// cannot express.
type DaemonConnectionInfoListener interface {
	OnDaemonConnectedWithInfo(userID, daemonID, name, hostname, platform string)
}

// DaemonConnectionManager is the subset of ToolsDaemonService needed by NATSToolBridge.
// This avoids importing the services package from toolexec.
//
// Every method that takes a daemonID targets THAT daemon's connection and
// fails when it is not connected for userID: the NATS subject that carried the
// request names the daemon, so another of the user's daemons must never act on
// it. SendDaemonCommand is the one default-routing method left; no bridge
// subject uses it.
type DaemonConnectionManager interface {
	IsDaemonOnline(ctx context.Context, userID string) bool
	ListConnectedDaemons(userID string) []DaemonInfo
	SendToolRequest(ctx context.Context, userID, daemonID string, request *ToolExecutionRequest) error
	SendToolRequestSync(ctx context.Context, userID, daemonID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error)
	SendToolExecutionCancel(ctx context.Context, userID, daemonID, requestID, reason string) error
	SendToolExecutionBackground(ctx context.Context, userID, daemonID, requestID, toolCallID string) error
	SendKillProcess(userID, daemonID, processID string) error
	SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error)
	// SendDaemonCommandToDaemon runs a command on ONE named daemon of the
	// user, never on another of their daemons.
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error)
	SendLoadProjectConfigs(ctx context.Context, userID, daemonID string, projectPath string, requestID string) error
	SendWatchProjectConfigs(ctx context.Context, userID, daemonID string, projectPath string, includeInitial bool) error
	SendTerminalInput(userID, daemonID string, sessionID string, data []byte) error
	SendTerminalResize(userID, daemonID string, sessionID string, cols, rows uint32) error
	SubscribeTerminalOutput(userID, daemonID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error)
	SubscribeProcessOutput(userID, daemonID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error)
}
