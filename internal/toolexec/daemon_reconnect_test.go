// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// Prod, 2026-10-10: every daemon-gateway rollout ended every daemon's stream,
// and each daemon redialled 15-25s later while it, and everything it ran,
// carried on. Tool calls in between failed at once with "no daemon connected
// for user" — one chat burned 25 of them in 16s — and calls in flight came
// back "outcome unknown", shell_wait on a still-running process included.
//
// These run the real router and the real gateway bridge over an in-process
// NATS server. The daemon is a fake DaemonConnectionManager behind the bridge;
// the registry is a fake that says when the daemon was last seen.

const (
	reconnectUser   = "user-1"
	reconnectDaemon = "daemon-1"
)

// lastSeenRecords is the registry as the router reads it: one daemon record,
// no fresh lease (the drained gateway released it) until the daemon
// re-attaches, and when the daemon was last heard from.
type lastSeenRecords struct {
	fakeDaemonRecords
	lastSeen time.Time

	mu       sync.Mutex
	attached bool
}

func (r *lastSeenRecords) DaemonLastSeenAt(_ context.Context, userID, daemonID string) (time.Time, error) {
	if userID != reconnectUser || daemonID != reconnectDaemon {
		return time.Time{}, nil
	}
	return r.lastSeen, nil
}

// attach writes the daemon's lease, as the gateway does on connect before it
// announces the connect.
func (r *lastSeenRecords) attach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attached = true
}

func (r *lastSeenRecords) ListAttachedDaemonIDsForUser(context.Context, string, time.Duration) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attached {
		return []string{reconnectDaemon}, nil
	}
	return nil, nil
}

func reconnectRecords(lastSeen time.Time, phase *string) *lastSeenRecords {
	return &lastSeenRecords{
		fakeDaemonRecords: fakeDaemonRecords{daemons: []*db.Daemon{
			{ID: reconnectDaemon, UserID: reconnectUser, LifecyclePhase: phase},
		}},
		lastSeen: lastSeen,
	}
}

// reconnectingDaemon is a daemon behind the real NATS bridge that can drop and
// come back the way a gateway rollout makes it: the bridge unsubscribes its
// subjects on disconnect and resubscribes on connect, and the gateway then
// announces the connect on daemon.v1.state.<id>.connected.
type reconnectingDaemon struct {
	t       *testing.T
	nc      *nats.Conn
	bridge  *NATSToolBridge
	records *lastSeenRecords
	pending sync.WaitGroup // scheduled reconnects; drained before NATS closes

	mu       sync.Mutex
	tools    []*ToolExecutionRequest
	commands []*reliantv1.DaemonCommandRequest
	// interruptNext makes the next request fail the way a stream that ends
	// under it does: the daemon drops, and the gateway reports the request
	// interrupted. It comes back after the given delay; zero leaves it gone.
	interruptNext time.Duration
	interrupting  bool
	// finishInterrupted, when set, is the result the daemon delivers for an
	// interrupted call after it reconnects (see toolStatus).
	finishInterrupted string
	statusQuestions   int
}

func newReconnectingDaemon(t *testing.T, nc *nats.Conn, records *lastSeenRecords) *reconnectingDaemon {
	d := &reconnectingDaemon{t: t, nc: nc, records: records}
	d.bridge = NewNATSToolBridge(nc, nil, &reconnectingDaemonMgr{d: d})
	t.Cleanup(func() {
		d.pending.Wait()
		_ = d.bridge.Close()
	})
	return d
}

// connect attaches the daemon the way the gateway does: the lease, then the
// bridge's subjects, then the announcement. Safe off the test goroutine.
func (d *reconnectingDaemon) connect() {
	d.records.attach()
	d.bridge.OnDaemonConnected(reconnectUser, reconnectDaemon)
	assert.NoError(d.t, d.nc.Flush())
	data, err := json.Marshal(daemonwire.Event{
		DaemonID: reconnectDaemon, UserID: reconnectUser,
		Type: daemonwire.EventConnected, At: time.Now().UTC(),
	})
	assert.NoError(d.t, err)
	assert.NoError(d.t, d.nc.Publish(daemonwire.Subject(reconnectDaemon, daemonwire.EventConnected), data))
	assert.NoError(d.t, d.nc.Flush())
}

// connectAfter reconnects the daemon after delay. The test does not end
// before it has.
func (d *reconnectingDaemon) connectAfter(delay time.Duration) {
	d.pending.Add(1)
	go func() {
		defer d.pending.Done()
		time.Sleep(delay)
		d.connect()
	}()
}

func (d *reconnectingDaemon) disconnect() {
	d.bridge.OnDaemonDisconnected(reconnectUser, reconnectDaemon)
	d.records.mu.Lock()
	d.records.attached = false
	d.records.mu.Unlock()
}

func (d *reconnectingDaemon) toolRequests() []*ToolExecutionRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*ToolExecutionRequest(nil), d.tools...)
}

func (d *reconnectingDaemon) commandRequests() []*reliantv1.DaemonCommandRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*reliantv1.DaemonCommandRequest(nil), d.commands...)
}

// interruption drops the daemon if the next request is to be interrupted,
// reporting whether it did. The drop happens before the gateway answers, as
// teardownConnection does it: the subjects are gone by the time the requester
// hears "interrupted".
func (d *reconnectingDaemon) interruption() bool {
	d.mu.Lock()
	back := d.interruptNext
	interrupt := d.interrupting
	d.interrupting = false
	d.mu.Unlock()
	if !interrupt {
		return false
	}
	d.disconnect()
	if back > 0 {
		d.connectAfter(back)
	}
	return true
}

// finishInterruptedWith makes the daemon finish an interrupted tool call after
// all, and deliver content as its result through the gateway it reconnected
// to — what the real daemon's result outbox does.
func (d *reconnectingDaemon) finishInterruptedWith(content string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finishInterrupted = content
}

// toolStatus answers ToolStatusCommand. By default the daemon has no record
// of the request, as one that restarted has none; with finishInterruptedWith
// it is still running, and its result follows shortly through the gateway.
func (d *reconnectingDaemon) toolStatus(req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	var q ToolStatusRequest
	if err := json.Unmarshal(req.Payload, &q); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.statusQuestions++
	content := d.finishInterrupted
	d.mu.Unlock()
	state := ToolRequestUnknown
	if content != "" {
		state = ToolRequestRunning
		d.pending.Add(1)
		go func() {
			defer d.pending.Done()
			time.Sleep(50 * time.Millisecond)
			assert.NoError(d.t, NewToolResultPublisher(d.nc).PublishToolResult(context.Background(), reconnectUser, reconnectDaemon,
				&ToolExecutionResponse{RequestID: q.RequestID, Success: true, Content: content}))
		}()
	}
	payload, err := json.Marshal(ToolStatusResponse{State: state})
	if err != nil {
		return nil, err
	}
	return &reliantv1.DaemonCommandResponse{RequestId: req.RequestId, CommandType: req.CommandType, Success: true, Payload: payload}, nil
}

func (d *reconnectingDaemon) interruptNextRequest(comeBackAfter time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.interrupting = true
	d.interruptNext = comeBackAfter
}

type reconnectingDaemonMgr struct {
	recordingDaemonMgr
	d *reconnectingDaemon
}

func (m *reconnectingDaemonMgr) SendToolRequestSync(_ context.Context, _, _ string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	m.d.mu.Lock()
	m.d.tools = append(m.d.tools, req)
	m.d.mu.Unlock()
	if m.d.interruption() {
		return nil, fmt.Errorf("%w while waiting for tool request %q response; the request outcome is unknown and the daemon may still complete it after reconnect",
			ErrDaemonConnectionInterrupted, req.RequestID)
	}
	return &ToolExecutionResponse{RequestID: req.RequestID, Success: true, Content: "ran " + req.ToolName}, nil
}

func (m *reconnectingDaemonMgr) SendDaemonCommandToDaemon(_ context.Context, _, _ string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if req.CommandType == ToolStatusCommand {
		return m.d.toolStatus(req)
	}
	m.d.mu.Lock()
	m.d.commands = append(m.d.commands, req)
	m.d.mu.Unlock()
	if m.d.interruption() {
		return nil, fmt.Errorf("%w while waiting for command %q response; the command may still complete after reconnect",
			ErrDaemonConnectionInterrupted, req.CommandType)
	}
	return &reliantv1.DaemonCommandResponse{RequestId: req.RequestId, Success: true, Payload: []byte(`"ok"`)}, nil
}

// waitRecorder records what a waiting call reports, as execute_tools hears it.
type waitRecorder struct {
	mu      sync.Mutex
	started []string
	ended   []bool
}

func (w *waitRecorder) observe(daemonID string) func(bool) {
	w.mu.Lock()
	w.started = append(w.started, daemonID)
	w.mu.Unlock()
	return func(reconnected bool) {
		w.mu.Lock()
		w.ended = append(w.ended, reconnected)
		w.mu.Unlock()
	}
}

func (w *waitRecorder) snapshot() (started []string, ended []bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.started...), append([]bool(nil), w.ended...)
}

func reconnectTestRouter(t *testing.T, records *lastSeenRecords) (*NATSDaemonRouter, *reconnectingDaemon) {
	t.Helper()
	nc := startBridgeTestNATS(t)
	return NewNATSDaemonRouter(nc, WithDatabase(records)), newReconnectingDaemon(t, nc, records)
}

func shellRequest(tool, input string) *ToolExecutionRequest {
	return &ToolExecutionRequest{RequestID: newRequestID(), ToolName: tool, ToolInput: input, ToolCallID: "toolu_" + tool}
}

// The 2026-10-10 failure itself: the daemon dropped seconds ago and redials
// while the call is out. The call waits for it, the request reaches it once,
// and the run is told it was waiting and that the machine came back.
func TestToolRequest_WaitsForADaemonThatJustDropped(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now().Add(-5*time.Second), nil))
	daemon.connectAfter(300 * time.Millisecond)
	waits := &waitRecorder{}
	ctx := WithDaemonReconnectWait(context.Background(), waits.observe)

	start := time.Now()
	resp, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"go test ./..."}`))
	require.NoError(t, err, "a daemon gone for seconds must be waited for, not reported as not connected")
	assert.True(t, resp.Success)
	assert.Less(t, time.Since(start), 3*time.Second, "the connect event ends the wait; it is not a fixed sleep")
	assert.Len(t, daemon.toolRequests(), 1, "an undelivered request is delivered once, after the reconnect")

	started, ended := waits.snapshot()
	assert.Equal(t, []string{reconnectDaemon}, started, "the run hears that it is waiting for its machine")
	assert.Equal(t, []bool{true}, ended, "and that the machine came back")
}

// The selector path (a worktree-pinned chat) waits the same way.
func TestToolRequestWithSelector_WaitsForADaemonThatJustDropped(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now().Add(-5*time.Second), nil))
	daemon.connectAfter(300 * time.Millisecond)
	ctx := WithDaemonReconnectWait(context.Background(), nil)

	resp, err := router.SendToolRequestSyncWithSelector(ctx, reconnectUser,
		shellRequest(tools.ShellToolName, `{"command":"ls"}`), &DaemonSelector{ID: reconnectDaemon})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// A machine that is starting again after being seen moments ago (its
// lifecycle mirror says provisioning) is waited for through the same "still
// starting" error the run already understands.
func TestToolRequest_WaitsForARecentlySeenMachineThatIsStarting(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now().Add(-5*time.Second), strPtr("provisioning")))
	daemon.connectAfter(300 * time.Millisecond)
	ctx := WithDaemonReconnectWait(context.Background(), nil)

	resp, err := router.SendToolRequestSyncWithSelector(ctx, reconnectUser,
		shellRequest(tools.ShellToolName, `{"command":"ls"}`), &DaemonSelector{ID: reconnectDaemon})
	require.NoError(t, err, "a machine starting again seconds after it was seen is waited for")
	assert.True(t, resp.Success)
}

// The wait is bounded by when the daemon was last seen. A daemon that comes
// back after the bound finds the call already failed, with the same "no
// daemon connected" marker the workflow's offline pause keys on, and with
// nothing delivered.
func TestToolRequest_GivesUpWhenTheDaemonOutstaysTheGrace(t *testing.T) {
	const remaining = 400 * time.Millisecond
	records := reconnectRecords(time.Time{}, nil)
	router, daemon := reconnectTestRouter(t, records)
	waits := &waitRecorder{}
	ctx := WithDaemonReconnectWait(context.Background(), waits.observe)

	// Set last, so starting NATS on a busy machine does not eat the grace.
	records.lastSeen = time.Now().Add(remaining - DaemonReconnectGrace)
	start := time.Now()
	_, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, daemonoffline.IsError(err), "the offline marker survives: %v", err)
	assert.ErrorIs(t, err, ErrNotDispatched, "nothing reached a daemon: a run step may send it again")
	assert.Contains(t, err.Error(), "waited", "the failure says the call already waited")
	assert.GreaterOrEqual(t, elapsed, remaining-100*time.Millisecond, "the call waits out what is left of the grace")
	assert.Less(t, elapsed, remaining+1500*time.Millisecond, "and not a moment more")

	// The daemon comes back after the bound. Nothing was queued for it: a
	// command the caller gave up on must never run late.
	daemon.connect()
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, daemon.toolRequests(), "nothing reaches a daemon that came back after the call gave up")
	started, ended := waits.snapshot()
	assert.Len(t, started, 1)
	assert.Equal(t, []bool{false}, ended)
}

// The caller's deadline bounds the wait as well.
func TestToolRequest_WaitEndsAtTheCallersDeadline(t *testing.T) {
	router, _ := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	ctx, cancel := context.WithTimeout(WithDaemonReconnectWait(context.Background(), nil), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	require.Error(t, err)
	assert.True(t, daemonoffline.IsError(err))
	assert.Less(t, time.Since(start), 2*time.Second)
}

// A daemon away for longer than a reconnect takes — a laptop closed an hour
// ago, or one never seen — fails at once, as it always did, so the offline
// pause and WAITING_FOR_DAEMON still take over without a 90s stall per call.
func TestToolRequest_DaemonGoneLongFailsFast(t *testing.T) {
	for name, lastSeen := range map[string]time.Time{
		"seen an hour ago": time.Now().Add(-time.Hour),
		"never seen":       {},
	} {
		t.Run(name, func(t *testing.T) {
			router, _ := reconnectTestRouter(t, reconnectRecords(lastSeen, nil))
			waits := &waitRecorder{}
			ctx := WithDaemonReconnectWait(context.Background(), waits.observe)

			start := time.Now()
			_, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
			require.Error(t, err)
			assert.True(t, daemonoffline.IsError(err))
			assert.ErrorIs(t, err, ErrNotDispatched)
			assert.NotContains(t, err.Error(), "waited")
			assert.Less(t, time.Since(start), 500*time.Millisecond)
			started, _ := waits.snapshot()
			assert.Empty(t, started, "nothing to say about a wait that never started")
		})
	}
}

// Without the opt-in (a file-tree poll, any rpc) nothing waits: those surfaces
// show the machine as reconnecting at once.
func TestToolRequest_DoesNotWaitWithoutOptingIn(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	daemon.connectAfter(300 * time.Millisecond)

	start := time.Now()
	_, err := router.SendToolRequestSync(context.Background(), reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	require.Error(t, err)
	assert.True(t, daemonoffline.IsError(err))
	assert.Less(t, time.Since(start), 250*time.Millisecond)
}

// An observation of state the daemon kept across the drop — shell_wait on a
// background process that is still running — is asked again after the
// reconnect, as a new request, and its budget is what is left of the first.
func TestToolRequest_InterruptedObservationIsAskedAgainAfterReconnect(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now().Add(-time.Hour), nil))
	daemon.connect()
	daemon.interruptNextRequest(300 * time.Millisecond)
	waits := &waitRecorder{}
	ctx := WithDaemonReconnectWait(context.Background(), waits.observe)

	first := shellRequest(tools.ShellWaitToolName, `{"process_id":"proc-1","timeout_seconds":600,"tail_lines":20}`)
	resp, err := router.SendToolRequestSync(ctx, reconnectUser, first)
	require.NoError(t, err)
	require.True(t, resp.Success, "the re-asked wait answers: %+v", resp)

	sent := daemon.toolRequests()
	require.Len(t, sent, 2, "asked once, interrupted, asked again")
	assert.NotEqual(t, sent[0].RequestID, sent[1].RequestID, "the second ask is a new request")
	assert.Equal(t, first.ToolCallID, sent[1].ToolCallID)
	var params tools.ShellWaitParams
	require.NoError(t, json.Unmarshal([]byte(sent[1].ToolInput), &params))
	assert.Equal(t, "proc-1", params.ProcessID)
	assert.Equal(t, 20, params.TailLines, "the rest of the input is untouched")
	assert.LessOrEqual(t, params.TimeoutSeconds, 600)
	assert.Greater(t, params.TimeoutSeconds, 590)

	started, ended := waits.snapshot()
	assert.Len(t, started, 1)
	assert.Equal(t, []bool{true}, ended)
}

// A command that may already have run — shell — is never sent twice. Its
// outcome stays unknown, typed so the run can tell it from a failed command.
func TestToolRequest_InterruptedCommandIsNotSentAgain(t *testing.T) {
	for _, tool := range []string{tools.ShellToolName, tools.ToolShellKill, tools.ToolProveTest, "mcp__github__create_issue"} {
		t.Run(tool, func(t *testing.T) {
			router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
			daemon.connect()
			daemon.interruptNextRequest(100 * time.Millisecond)
			ctx := WithDaemonReconnectWait(context.Background(), nil)

			resp, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tool, `{"command":"git push"}`))
			require.NoError(t, err, "the gateway's answer comes back as a response")
			assert.Equal(t, ErrorCodeDaemonInterrupted, resp.ErrorCode)
			assert.Contains(t, resp.ErrorMessage, "outcome is unknown")
			assert.True(t, IsTransportErrorCode(resp.ErrorCode))
			time.Sleep(300 * time.Millisecond)
			assert.Len(t, daemon.toolRequests(), 1, "sent exactly once")
		})
	}
}

// What a gateway rollout used to destroy: a command in flight when the
// daemon's stream dropped. The daemon keeps running it, reconnects, and
// delivers the result through the gateway it reconnected to; the call gets
// that result, and the command ran exactly once.
func TestToolRequest_InterruptedCallGetsItsResultAfterTheReconnect(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	daemon.connect()
	daemon.interruptNextRequest(100 * time.Millisecond)
	daemon.finishInterruptedWith("PASS ok github.com/acme/app 41.2s")

	resp, err := router.SendToolRequestSync(context.Background(), reconnectUser,
		shellRequest(tools.ShellToolName, `{"command":"go test ./..."}`))
	require.NoError(t, err)
	assert.Empty(t, resp.ErrorCode, "a dropped stream must not fail the call: %s", resp.ErrorMessage)
	assert.Equal(t, "PASS ok github.com/acme/app 41.2s", resp.Content)
	assert.Len(t, daemon.toolRequests(), 1, "the command ran once; its result was delivered, not re-requested")
}

// A daemon that comes back without the request — it restarted, and the call
// died with it — fails the call at once, saying so, rather than leaving it to
// wait out its ten-minute timeout.
func TestToolRequest_InterruptedCallFailsCleanlyWhenTheDaemonRestarted(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	daemon.connect()
	daemon.interruptNextRequest(100 * time.Millisecond)

	start := time.Now()
	resp, err := router.SendToolRequestSync(context.Background(), reconnectUser,
		shellRequest(tools.ShellToolName, `{"command":"go test ./..."}`))
	require.NoError(t, err)
	assert.Equal(t, ErrorCodeDaemonInterrupted, resp.ErrorCode)
	assert.Contains(t, resp.ErrorMessage, "no longer knows the request")
	assert.Less(t, time.Since(start), 5*time.Second, "a lost call is reported promptly")
	daemon.mu.Lock()
	assert.Equal(t, 1, daemon.statusQuestions, "the daemon is asked once")
	daemon.mu.Unlock()
}

// A daemon that does not come back leaves the call to its own deadline: the
// wait for a redelivered result is bounded like any request.
func TestToolRequest_InterruptedCallWaitIsBoundedByTheCallersDeadline(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	daemon.connect()
	daemon.interruptNextRequest(0) // gone for good
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// The worker-side tools (view, and the rest that read the user's files
// through daemon.RemoteClient) wait for an undelivered command too.
func TestDaemonCommand_WaitsForADaemonThatJustDropped(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now().Add(-5*time.Second), nil))
	daemon.connectAfter(300 * time.Millisecond)
	ctx := WithDaemonReconnectWait(context.Background(), nil)

	for _, send := range map[string]func() ([]byte, error){
		"default": func() ([]byte, error) {
			return router.SendDaemonCommand(ctx, reconnectUser, "fs.write_file", []byte(`{}`), 5000)
		},
		"selector": func() ([]byte, error) {
			return SendDaemonCommandForSelector(ctx, router, reconnectUser, &DaemonSelector{ID: reconnectDaemon}, "fs.write_file", []byte(`{}`), 5000)
		},
		"pinned": func() ([]byte, error) {
			return router.SendDaemonCommandToDaemon(ctx, reconnectUser, reconnectDaemon, "fs.write_file", []byte(`{}`), 5000)
		},
	} {
		out, err := send()
		require.NoError(t, err, "an undelivered write is safe to deliver after the reconnect: nothing ran")
		assert.Equal(t, `"ok"`, string(out))
	}
}

// An interrupted read is asked again; an interrupted write is not.
func TestDaemonCommand_OnlyInterruptedReadsAreAskedAgain(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
		daemon.connect()
		daemon.interruptNextRequest(300 * time.Millisecond)
		ctx := WithDaemonReconnectWait(context.Background(), nil)

		out, err := router.SendDaemonCommand(ctx, reconnectUser, "fs.read_file", []byte(`{"path":"/w/main.go"}`), 5000)
		require.NoError(t, err)
		assert.Equal(t, `"ok"`, string(out))
		sent := daemon.commandRequests()
		require.Len(t, sent, 2)
		assert.NotEqual(t, sent[0].RequestId, sent[1].RequestId, "the second ask is a new request")
	})
	t.Run("write", func(t *testing.T) {
		router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
		daemon.connect()
		daemon.interruptNextRequest(100 * time.Millisecond)
		ctx := WithDaemonReconnectWait(context.Background(), nil)

		_, err := router.SendDaemonCommand(ctx, reconnectUser, "fs.write_file", []byte(`{"path":"/w/main.go"}`), 5000)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDaemonConnectionInterrupted), "typed across NATS: %v", err)
		time.Sleep(300 * time.Millisecond)
		assert.Len(t, daemon.commandRequests(), 1, "a write is sent exactly once")
	})
}

// A transport failure the gateway answered for is a reply, but not the
// daemon's: it proves nothing about the machine being reachable, so it must
// not clear the run's WAITING_FOR_DAEMON (trackDaemonPending reads
// RanOnDaemon as "the machine answered").
func TestExecuteOnDaemon_AGatewayAnsweredFailureDidNotRunOnTheDaemon(t *testing.T) {
	for _, code := range []string{ErrorCodeDaemonInterrupted, ErrorCodeDaemonRoundTrip} {
		executor := &RemoteExecutor{router: &gatewayFailureRouter{code: code}}
		res, err := executor.executeOnDaemon(context.Background(),
			&ToolRequest{UserID: "u", ChatID: "c", ProjectID: "p", ToolName: tools.ShellToolName, ToolCallID: "tc"}, time.Now())
		require.NoError(t, err)
		assert.False(t, res.RanOnDaemon, code)
		assert.True(t, res.IsError, code)
	}
}

type gatewayFailureRouter struct {
	routerStub
	code string
}

func (r *gatewayFailureRouter) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return &ToolExecutionResponse{IsError: true, ErrorCode: r.code, ErrorMessage: "daemon connection interrupted"}, nil
}
