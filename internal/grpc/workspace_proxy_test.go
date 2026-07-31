// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Compile-time guard: every browser-facing workspace service that NewServer
// swaps for a daemon proxy when router != nil must have a proxy type that
// actually satisfies the corresponding Connect handler interface. If a future
// service is added to that branch without a working proxy, this block fails to
// compile — the failure mode we are defending against is a service silently
// left un-proxied (its filesystem work runs on the api-server and returns
// empty), which is exactly the PackageCommands bug.
var (
	_ reliantv1connect.FileSystemServiceHandler      = (*services.FileSystemProxyService)(nil)
	_ reliantv1connect.BackgroundServiceHandler      = (*services.BackgroundProxyService)(nil)
	_ reliantv1connect.TerminalServiceHandler        = (*services.TerminalProxyService)(nil)
	_ reliantv1connect.PackageCommandsServiceHandler = (*services.PackageCommandsProxyService)(nil)
)

// stubDaemonRouter is a non-nil, do-nothing DaemonRouter for wiring tests. Its
// only job is to be a distinguishable non-nil value; the picker never calls it.
type stubDaemonRouter struct{}

func (stubDaemonRouter) IsDaemonOnline(context.Context, string) (bool, error) { return true, nil }
func (stubDaemonRouter) SendToolRequest(context.Context, string, *toolexec.ToolExecutionRequest) error {
	return nil
}
func (stubDaemonRouter) SendToolRequestSync(context.Context, string, *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return nil, nil
}
func (stubDaemonRouter) SendToolRequestSyncWithSelector(context.Context, string, *toolexec.ToolExecutionRequest, *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return nil, nil
}
func (stubDaemonRouter) SendToolExecutionCancel(context.Context, string, string, string) error {
	return nil
}
func (stubDaemonRouter) SendKillProcess(context.Context, string, string) error { return nil }
func (stubDaemonRouter) SendDaemonCommandToDaemon(context.Context, string, string, string, []byte, int32) ([]byte, error) {
	return nil, nil
}

func (stubDaemonRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return nil, nil
}
func (stubDaemonRouter) ResolveDaemonID(context.Context, string) (string, error) { return "", nil }
func (stubDaemonRouter) EnqueueDaemonCommand(context.Context, string, string, []byte, int32) (int, error) {
	return 0, nil
}
func (stubDaemonRouter) SendLoadProjectConfigs(context.Context, string, string, string) error {
	return nil
}
func (stubDaemonRouter) SendWatchProjectConfigs(context.Context, string, string, bool) error {
	return nil
}
func (stubDaemonRouter) SendTerminalInput(context.Context, string, string, []byte) error { return nil }
func (stubDaemonRouter) SendTerminalResize(context.Context, string, string, uint32, uint32) error {
	return nil
}
func (stubDaemonRouter) SubscribeTerminalOutput(context.Context, string, string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	return nil, func() {}, nil
}
func (stubDaemonRouter) SubscribeProcessOutput(context.Context, string, string, bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	return nil, func() {}, nil
}
func (stubDaemonRouter) Close() error { return nil }

// The wiring seam: a cloud daemon (router != nil) gets the proxy; a local server
// (router == nil) keeps the DB/local service. This is the guard against the
// PackageCommands regression — it must never resolve to the local service when a
// daemon router is present.
func TestPickPackageCommandsService(t *testing.T) {
	if got := pickPackageCommandsService(nil, nil); got == nil {
		t.Fatal("router == nil: expected a service, got nil")
	} else if _, ok := got.(*services.PackageCommandsService); !ok {
		t.Fatalf("router == nil: expected *services.PackageCommandsService, got %T", got)
	}

	if got := pickPackageCommandsService(stubDaemonRouter{}, nil); got == nil {
		t.Fatal("router != nil: expected a service, got nil")
	} else if _, ok := got.(*services.PackageCommandsProxyService); !ok {
		t.Fatalf("router != nil: expected *services.PackageCommandsProxyService, got %T", got)
	}
}