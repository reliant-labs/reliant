// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A reliant-temporal-worker rollout releases the run steps in flight, and the
// retry on another worker reattaches to the command the daemon still has
// instead of running it again (ReattachToolRequest). These run the real
// router and gateway bridge over an in-process NATS server; the daemon is a
// fake that answers tool.status the way each daemon version does.

// reattachDaemon answers tool.status with status (nil: a daemon too old to
// know the command) and, when it vouches for the request, delivers content
// as its result through the gateway after delay — what the real daemon's
// outbox does. It never expects a tool request: a reattach sends none.
type reattachDaemon struct {
	recordingDaemonMgr
	t       *testing.T
	nc      *nats.Conn
	status  *ToolStatusResponse
	content string
	delay   time.Duration

	mu       sync.Mutex
	asked    int
	sent     []*ToolExecutionRequest
	publish  sync.Once
	inFlight sync.WaitGroup
}

func (d *reattachDaemon) SendToolRequestSync(_ context.Context, _, _ string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	d.mu.Lock()
	d.sent = append(d.sent, req)
	d.mu.Unlock()
	return &ToolExecutionResponse{RequestID: req.RequestID, Success: true, Content: "ran again"}, nil
}

func (d *reattachDaemon) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if req.CommandType != ToolStatusCommand {
		return d.recordingDaemonMgr.SendDaemonCommandToDaemon(ctx, userID, daemonID, req)
	}
	d.mu.Lock()
	d.asked++
	d.mu.Unlock()
	if d.status == nil {
		return &reliantv1.DaemonCommandResponse{RequestId: req.RequestId, CommandType: req.CommandType,
			ErrorMessage: `unknown daemon command type: "tool.status"`}, nil
	}
	var q ToolStatusRequest
	if err := json.Unmarshal(req.Payload, &q); err != nil {
		return nil, err
	}
	vouches := d.status.State == ToolRequestRunning || d.status.State == ToolRequestCompleted || d.status.Resent
	if vouches && d.content != "" {
		d.publish.Do(func() {
			d.inFlight.Add(1)
			go func() {
				defer d.inFlight.Done()
				time.Sleep(d.delay)
				assert.NoError(d.t, NewToolResultPublisher(d.nc).PublishToolResult(context.Background(), reconnectUser, reconnectDaemon,
					&ToolExecutionResponse{RequestID: q.RequestID, Success: true, Content: d.content}))
			}()
		})
	}
	payload, err := json.Marshal(d.status)
	if err != nil {
		return nil, err
	}
	return &reliantv1.DaemonCommandResponse{RequestId: req.RequestId, CommandType: req.CommandType, Success: true, Payload: payload}, nil
}

func (d *reattachDaemon) questions() (asked int, sent []*ToolExecutionRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asked, append([]*ToolExecutionRequest(nil), d.sent...)
}

// reattachRouter is a router whose one daemon, connected, is d.
func reattachRouter(t *testing.T, d *reattachDaemon) *NATSDaemonRouter {
	t.Helper()
	nc := startBridgeTestNATS(t)
	d.t, d.nc = t, nc
	records := reconnectRecords(time.Now(), nil)
	records.attach()
	bridge := NewNATSToolBridge(nc, nil, d)
	t.Cleanup(func() {
		d.inFlight.Wait()
		_ = bridge.Close()
	})
	bridge.OnDaemonConnected(reconnectUser, reconnectDaemon)
	require.NoError(t, nc.Flush())
	return NewNATSDaemonRouter(nc, WithDatabase(records))
}

const reattachID = "runstep-0123456789abcdef0123456789abcdef"

func reattach(t *testing.T, router *NATSDaemonRouter, timeout time.Duration) (*ToolExecutionResponse, bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return router.ReattachToolRequest(ctx, reconnectUser, reattachID, &DaemonSelector{ID: reconnectDaemon})
}

// The command is still running: the retry waits for it and gets its result.
func TestReattach_WaitsForTheRunningCommandsResult(t *testing.T) {
	d := &reattachDaemon{status: &ToolStatusResponse{State: ToolRequestRunning}, content: "PASS", delay: 200 * time.Millisecond}
	router := reattachRouter(t, d)

	resp, attached, err := reattach(t, router, 10*time.Second)
	require.NoError(t, err)
	require.True(t, attached)
	assert.Equal(t, "PASS", resp.Content, "the result of the command the earlier attempt started")
	assert.Equal(t, reattachID, resp.RequestID)
	_, sent := d.questions()
	assert.Empty(t, sent, "a reattach never sends the command")
}

// The command finished and the daemon holds its result (completed), or kept
// it after a gateway took it for the worker that went away (delivered and
// resent): the result comes at once.
func TestReattach_ReturnsTheResultOfAFinishedCommand(t *testing.T) {
	for name, status := range map[string]ToolStatusResponse{
		"held":                                 {State: ToolRequestCompleted, Resent: true},
		"held, daemon before the resent field": {State: ToolRequestCompleted},
		"retained":                             {State: ToolRequestDelivered, Resent: true},
	} {
		t.Run(name, func(t *testing.T) {
			d := &reattachDaemon{status: &status, content: "exit 3"}
			router := reattachRouter(t, d)

			resp, attached, err := reattach(t, router, 10*time.Second)
			require.NoError(t, err)
			require.True(t, attached)
			assert.Equal(t, "exit 3", resp.Content)
			_, sent := d.questions()
			assert.Empty(t, sent)
		})
	}
}

// Nothing to reattach to: the daemon restarted (unknown), or its result went
// to a waiter that is gone and it kept no copy (delivered, not resent), or it
// is too old to answer tool.status. Not attached, at once, and nothing sent.
func TestReattach_NotAttachedWhenTheDaemonCannotVouchForTheRequest(t *testing.T) {
	for name, status := range map[string]*ToolStatusResponse{
		"unknown":                    {State: ToolRequestUnknown},
		"delivered, no copy kept":    {State: ToolRequestDelivered},
		"daemon without tool.status": nil,
	} {
		t.Run(name, func(t *testing.T) {
			d := &reattachDaemon{status: status, content: "must not arrive"}
			router := reattachRouter(t, d)

			start := time.Now()
			resp, attached, err := reattach(t, router, time.Minute)
			require.NoError(t, err)
			assert.False(t, attached)
			assert.Nil(t, resp)
			assert.Less(t, time.Since(start), 30*time.Second, "a daemon that cannot vouch is not waited on")
			asked, sent := d.questions()
			assert.Equal(t, 1, asked, "the daemon is asked once")
			assert.Empty(t, sent, "and the command is never sent")
		})
	}
}

// A request id that cannot be a result subject is never asked about.
func TestReattach_RejectsAnUnusableRequestID(t *testing.T) {
	d := &reattachDaemon{status: &ToolStatusResponse{State: ToolRequestRunning}}
	router := reattachRouter(t, d)

	_, attached, err := router.ReattachToolRequest(context.Background(), reconnectUser, "a.b", &DaemonSelector{ID: reconnectDaemon})
	require.NoError(t, err)
	assert.False(t, attached)
	asked, _ := d.questions()
	assert.Zero(t, asked)
}

// Attached, and the result does not come before the caller's deadline: the
// wait fails as attached — the outcome is unknown — and still sends nothing.
func TestReattach_AttachedWaitEndsAtTheDeadline(t *testing.T) {
	d := &reattachDaemon{status: &ToolStatusResponse{State: ToolRequestRunning}}
	router := reattachRouter(t, d)

	_, attached, err := reattach(t, router, 500*time.Millisecond)
	require.Error(t, err)
	assert.True(t, attached)
	_, sent := d.questions()
	assert.Empty(t, sent)
}

// RemoteExecutor.ReattachTool maps the reattached result as a sent call's,
// and a router that cannot reattach (or a call with no request id) is simply
// not attached.
func TestRemoteExecutor_ReattachTool(t *testing.T) {
	d := &reattachDaemon{status: &ToolStatusResponse{State: ToolRequestCompleted}, content: `{"stdout":"ok","exit_code":0}`}
	router := reattachRouter(t, d)
	req := &ToolRequest{ToolName: "shell", UserID: reconnectUser, RequestID: reattachID,
		DaemonSelector: &DaemonSelector{ID: reconnectDaemon}, RunStep: true}

	result, attached, err := NewRemoteExecutor(router).ReattachTool(context.Background(), req)
	require.NoError(t, err)
	require.True(t, attached)
	assert.True(t, result.Success)
	assert.Equal(t, `{"stdout":"ok","exit_code":0}`, result.Content)
	assert.True(t, result.RanOnDaemon)

	noID := *req
	noID.RequestID = ""
	_, attached, err = NewRemoteExecutor(router).ReattachTool(context.Background(), &noID)
	require.NoError(t, err)
	assert.False(t, attached, "a call with no request id has nothing to find")

	_, attached, err = NewRemoteExecutor(cannotReattachRouter{}).ReattachTool(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, attached, "a router that cannot ask is not attached")
}

// cannotReattachRouter is a DaemonRouter without ReattachToolRequest.
type cannotReattachRouter struct{ DaemonRouter }

// The request id a caller names is the one the daemon is sent; a missing or
// unusable one is replaced by a fresh id, as before.
func TestExecuteOnDaemon_SendsTheCallersRequestID(t *testing.T) {
	d := &reattachDaemon{}
	router := reattachRouter(t, d)
	exec := NewRemoteExecutor(router)
	for _, id := range []string{reattachID, "", "not/a-subject"} {
		_, err := exec.executeOnDaemon(context.Background(), &ToolRequest{ToolName: "shell", UserID: reconnectUser,
			RequestID: id, DaemonSelector: &DaemonSelector{ID: reconnectDaemon}}, time.Now())
		require.NoError(t, err)
	}
	_, sent := d.questions()
	require.Len(t, sent, 3)
	assert.Equal(t, reattachID, sent[0].RequestID)
	for _, req := range sent[1:] {
		assert.Regexp(t, resultRequestIDPattern, req.RequestID)
		assert.NotEqual(t, reattachID, req.RequestID)
	}
	assert.NotEqual(t, sent[1].RequestID, sent[2].RequestID)
}
