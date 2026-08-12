// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// serverName and serverVersion identify this server to MCP clients.
const (
	serverName    = "reliant-workspace"
	serverVersion = "0.1.0"
)

// defaultCommandTimeout bounds a single daemon command. Shell commands can
// legitimately run longer, which is what the background tools are for; a
// synchronous call that exceeds this should be restarted as a background one.
const defaultCommandTimeout = 120 * time.Second

// CommandSender dispatches a command to a SPECIFIC daemon and returns its JSON
// response. It mirrors the daemon router's method so the real router satisfies
// it directly, while tests can substitute a fake.
//
// Targeting an explicit daemon id is load-bearing, not incidental. A grant is
// bound to exactly one daemon, and the router's default resolution picks
// whichever daemon a user currently has connected — so routing by user alone
// would let a grant scoped to one workspace execute in another. The daemon id
// travels from the grant all the way to the wire.
type CommandSender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// WorkspaceWaker ensures a suspended workspace is running before a command is
// sent. A phone-initiated request is often the first traffic a workspace has
// seen in hours, so the common case is a cold one.
type WorkspaceWaker interface {
	// EnsureAwake blocks until the daemon is ready to accept commands, or
	// returns an error if it cannot be woken in time.
	EnsureAwake(ctx context.Context, userID, daemonID string) error
}

// AuditSink records what a connector did. Recording is not optional in
// practice: when a third-party model runs commands in someone's workspace,
// "what did it actually do" is the first question asked, and it cannot be
// answered retroactively.
//
// Recording is two-phase because a call that is only recorded after it returns
// is lost precisely when the server dies mid-command — the case where the
// record matters most. Begin writes durably BEFORE dispatch; Complete resolves
// it. A record left unresolved is the signal that a command was issued and
// never accounted for.
type AuditSink interface {
	// Begin records an attempt and returns an id for Complete. An empty id
	// means the attempt could not be recorded; Complete then does nothing.
	Begin(ctx context.Context, entry AuditEntry) string

	// Complete resolves a previously begun record.
	Complete(ctx context.Context, id string, denied bool, errMsg string, duration time.Duration)
}

// AuditEntry is one connector tool invocation.
type AuditEntry struct {
	GrantID   string
	UserID    string
	DaemonID  string
	ToolName  string
	Command   string
	Arguments json.RawMessage
	Denied    bool
	Error     string
	Duration  time.Duration
	At        time.Time

	// Status is the lifecycle state for this write; see connectorgrant's
	// Audit* constants.
	Status string

	// auditID correlates a begun record with its completion. Set by the sink,
	// not by callers.
	auditID string
}

// Session is the resolved connector context for one MCP connection: which
// user, which daemon, and under what grant.
type Session struct {
	GrantID  string
	UserID   string
	DaemonID string
	Policy   *daemonpolicy.Policy

	// ToolNames limits the catalog for this session. A grant that permits only
	// reading should not advertise write tools at all — a model shown a tool
	// will eventually call it, and a refusal it could have avoided is a wasted
	// turn and a confusing transcript.
	ToolNames []string
}

// GrantResolver re-reads a session's current grant.
//
// MCP sessions are long-lived and the SDK builds one server per session, so
// the Session captured when the connection opened would otherwise stay
// authoritative for its whole lifetime. Revocation is already immediate (the
// HTTP layer re-authenticates every request), but NARROWING a grant — removing
// a tool, tightening the path root — would not take effect until the client
// reconnected. Re-resolving per call closes that gap.
type GrantResolver interface {
	// Resolve returns the session's current policy and tool list, or an error
	// if the grant is no longer usable.
	Resolve(ctx context.Context, grantID string) (*Session, error)
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Sender   CommandSender
	Waker    WorkspaceWaker
	Audit    AuditSink
	Resolver GrantResolver
	Limiter  *Limiter
}

// NewServer builds an MCP server exposing sess's granted tools, backed by the
// daemon named in the session.
func NewServer(sess *Session, deps Deps) (*mcp.Server, error) {
	if sess == nil {
		return nil, errors.New("mcpserver: session is required")
	}
	if deps.Sender == nil {
		return nil, errors.New("mcpserver: command sender is required")
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Title:   "Reliant Workspace",
		Version: serverVersion,
	}, nil)

	granted := grantedSet(sess.ToolNames)
	for _, tool := range Catalog {
		if !granted[tool.Name] {
			continue
		}
		srv.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}, makeHandler(tool, sess, deps))
	}

	return srv, nil
}

// toolStillGranted reports whether name is in the session's current tool list.
// An empty list grants nothing, matching the policy's fail-closed stance.
func toolStillGranted(sess *Session, name string) bool {
	for _, granted := range sess.ToolNames {
		if granted == name {
			return true
		}
	}
	return false
}

// grantedSet indexes the session's tool names. An empty list grants nothing,
// consistent with the policy's fail-closed stance.
func grantedSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// makeHandler builds the MCP handler for one catalog tool.
func makeHandler(tool Tool, sess *Session, deps Deps) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()

		var args map[string]any
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return toolError(fmt.Sprintf("could not read the arguments for %s: %v", tool.Name, err)), nil
			}
		}
		if args == nil {
			args = map[string]any{}
		}

		// Rate and concurrency limits, applied before any lookup, wake, or
		// dispatch so a caller in a retry loop costs a map lookup rather than
		// a database read and a workspace wake.
		//
		// Not audited: a limited call never reached the workspace, and a model
		// looping at 10 requests a second would otherwise fill the audit log
		// with the very noise that makes it unreadable. The limiter's own
		// counters are the right place to see this.
		if deps.Limiter != nil {
			release, err := deps.Limiter.Acquire(sess.GrantID)
			if err != nil {
				return toolError(err.Error() +
					". Wait before trying again — retrying immediately will not help."), nil
			}
			defer release()
		}

		// active is the grant this call runs under: the session's copy until
		// re-resolution replaces it, so the audit closure below always has a
		// usable value.
		active := sess

		// auditID is set once the call is about to be dispatched. Failures
		// before that point are recorded in one shot, since there is no window
		// in which they could be lost.
		auditID := ""
		record := func(denied bool, errMsg string) {
			if deps.Audit == nil {
				return
			}
			if auditID != "" {
				deps.Audit.Complete(ctx, auditID, denied, errMsg, time.Since(start))
				auditID = ""
				return
			}
			status := connectorgrant.AuditCompleted
			if denied {
				status = connectorgrant.AuditDenied
			}
			deps.Audit.Begin(ctx, AuditEntry{
				// active is the re-resolved grant once resolution has run, and
				// falls back to the session's copy before that. Reading it
				// here keeps every audit row sourced from one variable.
				GrantID:   active.GrantID,
				UserID:    active.UserID,
				DaemonID:  active.DaemonID,
				ToolName:  tool.Name,
				Command:   tool.Command,
				Arguments: req.Params.Arguments,
				Denied:    denied,
				Error:     errMsg,
				Duration:  time.Since(start),
				At:        start,
				Status:    status,
			})
		}

		// Re-read the grant. The session's copy was captured when the
		// connection opened, and an MCP session can outlive a change to it.
		if deps.Resolver != nil {
			refreshed, err := deps.Resolver.Resolve(ctx, sess.GrantID)
			if err != nil {
				record(true, err.Error())
				return toolError(
					"this connector's access is no longer valid: " + err.Error() +
						". This is permanent for this connection — do not retry."), nil
			}
			active = refreshed
		}

		// A tool removed from the grant since the session opened is refused
		// even though the client still has it in its tool list.
		if !toolStillGranted(active, tool.Name) {
			msg := fmt.Sprintf("the %s tool is no longer allowed for this connector", tool.Name)
			record(true, msg)
			return toolError(msg + ". This is a permanent restriction, not a transient error — do not retry."), nil
		}

		payloadValue, err := tool.BuildPayload(args)
		if err != nil {
			record(false, err.Error())
			return toolError(err.Error()), nil
		}
		payload, err := json.Marshal(payloadValue)
		if err != nil {
			record(false, err.Error())
			return toolError(fmt.Sprintf("could not encode the request for %s: %v", tool.Name, err)), nil
		}

		// Check locally before waking a workspace. A denied call should not
		// cost the user 30 seconds of cold start and a compute charge.
		if err := active.Policy.Check(tool.Command, payload); err != nil {
			record(true, err.Error())
			return toolError(explainDenial(err)), nil
		}

		if deps.Waker != nil {
			if err := deps.Waker.EnsureAwake(ctx, active.UserID, active.DaemonID); err != nil {
				record(false, err.Error())
				return toolError(explainUnavailable(err)), nil
			}
		}

		timeout := defaultCommandTimeout
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
				timeout = remaining
			}
		}

		// Attach the policy so the daemon can enforce it at command dispatch.
		// The check above is a local fast path that avoids waking a workspace
		// for a call that will be refused; this is the one that actually
		// binds, because it travels with the request to the process that does
		// the work.
		// Record the attempt BEFORE dispatch. From here the command may run on
		// the daemon, so a crash after this point must still leave evidence
		// that it was issued.
		if deps.Audit != nil {
			auditID = deps.Audit.Begin(ctx, AuditEntry{
				GrantID:   active.GrantID,
				UserID:    active.UserID,
				DaemonID:  active.DaemonID,
				ToolName:  tool.Name,
				Command:   tool.Command,
				Arguments: req.Params.Arguments,
				At:        start,
				Status:    connectorgrant.AuditStarted,
			})
		}

		sendCtx := daemonpolicy.NewContext(ctx, active.Policy)

		respBytes, err := deps.Sender.SendDaemonCommandToDaemon(
			sendCtx, active.UserID, active.DaemonID, tool.Command, payload, int32(timeout.Milliseconds()))
		if err != nil {
			record(false, err.Error())
			return toolError(explainDenial(err)), nil
		}

		record(false, "")
		return toolSuccess(respBytes), nil
	}
}

// explainDenial turns an internal error into something a model can act on.
// The daemon returns policy refusals as opaque command errors, so the text is
// matched rather than the type; a refusal that reads like a transient failure
// invites an infinite retry loop.
func explainDenial(err error) string {
	msg := err.Error()
	if errors.Is(err, daemonpolicy.ErrDenied) || strings.Contains(msg, daemonpolicy.ErrDenied.Error()) {
		return msg + ". This is a permanent restriction of this connection, not a transient error — do not retry, and tell the user what access is missing."
	}
	return msg
}

// explainUnavailable turns a wake outcome into guidance a model can act on.
//
// The two cases must not read alike. "Starting" is worth coming back for, and
// naming a wait keeps a model from retrying in a tight loop and reporting the
// workspace as broken. "Cannot start" is not worth retrying at all, and saying
// so stops a model from burning the user's turn on attempts that will each
// fail the same way.
func explainUnavailable(err error) string {
	switch {
	case errors.Is(err, ErrWorkspaceStarting):
		return "The workspace was suspended and is now starting. This usually takes " +
			"under a minute. Do not retry immediately — wait about a minute, then try " +
			"the same call again, and tell the user their workspace is starting up."
	case errors.Is(err, ErrWorkspaceUnavailable):
		return err.Error() + ". This will not fix itself by retrying — tell the user " +
			"their workspace is not running and that they need to start it."
	default:
		return fmt.Sprintf("the workspace is not available: %v", err)
	}
}

// toolError returns an MCP-level tool error. Tool failures are reported in the
// result with IsError rather than as protocol errors, so the model sees them
// and can adapt.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// toolSuccess wraps a daemon response as MCP content.
func toolSuccess(payload []byte) *mcp.CallToolResult {
	text := string(payload)
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}
