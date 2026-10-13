// Copyright (c) 2025 Reliant Labs
package toolexectest

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Recorder is a toolexec.DaemonRouter that records the tool-execution
// cancels and backgroundings it is sent, and answers everything else with a
// default: every daemon online, every command an empty success, every user
// resolved to "test-daemon-id".
type Recorder struct {
	CancelCalls     []CancelCall
	BackgroundCalls []BackgroundCall
	BackgroundErr   error
}

// CancelCall is one SendToolExecutionCancel.
type CancelCall struct {
	UserID    string
	RequestID string
	Reason    string
}

// BackgroundCall is one SendToolExecutionBackground.
type BackgroundCall struct {
	UserID     string
	RequestID  string
	ToolCallID string
}

func (f *Recorder) IsDaemonOnline(_ context.Context, userID string, _ *toolexec.DaemonSelector) (bool, error) {
	return true, nil
}

func (f *Recorder) SendToolRequest(ctx context.Context, userID string, request *toolexec.ToolExecutionRequest) error {
	return nil
}

func (f *Recorder) SendToolExecutionCancel(_ context.Context, userID, requestID, reason string) error {
	f.CancelCalls = append(f.CancelCalls, CancelCall{UserID: userID, RequestID: requestID, Reason: reason})
	return nil
}

func (f *Recorder) SendToolExecutionBackground(_ context.Context, userID, requestID, toolCallID string) error {
	f.BackgroundCalls = append(f.BackgroundCalls, BackgroundCall{UserID: userID, RequestID: requestID, ToolCallID: toolCallID})
	return f.BackgroundErr
}

func (f *Recorder) SendKillProcess(ctx context.Context, userID, processID string) error {
	return nil
}

func (f *Recorder) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return f.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (f *Recorder) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return nil, nil
}

func (f *Recorder) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}

func (f *Recorder) SendToolRequestSyncWithSelector(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}

func (f *Recorder) SendLoadProjectConfigs(_ context.Context, _, _ string, _ string) error {
	return nil
}

func (f *Recorder) SendWatchProjectConfigs(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}

func (f *Recorder) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}

func (f *Recorder) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}

func (f *Recorder) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}

func (f *Recorder) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}

func (f *Recorder) Close() error { return nil }

func (f *Recorder) EnqueueDaemonCommand(_ context.Context, _, _ string, _ []byte, _ int32) (int, error) {
	return 0, nil
}

func (f *Recorder) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	return "test-daemon-id", nil
}

func (f *Recorder) OpenLocalModelHTTP(context.Context, string, string, *reliantv1.LocalModelHTTPRequest) (*toolexec.LocalModelHTTPStream, error) {
	return nil, nil
}

func (f *Recorder) RefreshLocalModels(context.Context, string, string) (*reliantv1.LocalModelInventory, error) {
	return nil, nil
}

var _ toolexec.DaemonRouter = (*Recorder)(nil)
