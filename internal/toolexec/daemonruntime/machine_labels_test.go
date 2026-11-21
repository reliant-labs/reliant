// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// recordingGateway acknowledges every registration, keeps a copy, and ends
// the first session right away so the daemon has to reconnect.
type recordingGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	mu   sync.Mutex
	regs []*reliantv1.DaemonRegister
}

func (g *recordingGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	msg, err := stream.Receive()
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.regs = append(g.regs, proto.Clone(msg.GetRegister()).(*reliantv1.DaemonRegister))
	first := len(g.regs) == 1
	g.mu.Unlock()
	if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RegistrationAck{
		RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "d-1", UserId: "u-1"},
	}}); err != nil {
		return err
	}
	if first {
		return connect.NewError(connect.CodeUnavailable, nil)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (g *recordingGateway) registrations() []*reliantv1.DaemonRegister {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*reliantv1.DaemonRegister(nil), g.regs...)
}

// The labels `daemon start --label` declares go out on EVERY registration,
// reconnects included, as machine_labels (durable, merged server-side) and
// in labels (this connection's selection labels).
func TestDaemonDeclaresMachineLabelsOnEveryRegistration(t *testing.T) {
	t.Parallel()
	gw := &recordingGateway{}
	client, err := newDaemonClient(bootstrap.DaemonBootstrapConfig{
		AuthToken:     "rlnt_pat_test",
		GRPCURL:       startGateway(t, gw),
		TLSMode:       bootstrap.TLSModeH2C,
		DataDir:       t.TempDir(),
		MachineLabels: map[string]string{"pool": "pipeline", "gate": "yes"},
	}, Host{WorkingDir: t.TempDir()}, Guards{})
	require.NoError(t, err)
	client.reconnectMinDelay = 10 * time.Millisecond

	startDaemon(t, client)

	require.Eventually(t, func() bool { return len(gw.registrations()) >= 2 }, 8*time.Second, 10*time.Millisecond,
		"the daemon did not reconnect after its first session ended")
	for i, reg := range gw.registrations()[:2] {
		require.Equal(t, map[string]string{"pool": "pipeline", "gate": "yes"}, reg.GetMachineLabels(), "registration %d", i)
		require.Equal(t, map[string]string{"pool": "pipeline", "gate": "yes"}, reg.GetLabels(), "registration %d", i)
		// And the drain protocol's capability: without it a gateway never
		// sends this daemon RestartRequested (upgrade_drain.go).
		require.True(t, reg.GetDrainsOnRestartRequest(), "registration %d", i)
	}
}

// A daemon that declares nothing sends neither, so an old daemon and a new
// one without --label look the same to the server; the runtime type keeps
// its key and wins over a declared label of the same name.
func TestRegisterLabels(t *testing.T) {
	d := &daemonClient{}
	require.Nil(t, d.registerLabels())
	require.Nil(t, d.machineLabels())

	d.runtimeType = "kata"
	d.bootCfg.MachineLabels = map[string]string{"pool": "gate", config.DaemonRuntimeTypeLabelKey: "spoofed"}
	require.Equal(t, map[string]string{"pool": "gate", config.DaemonRuntimeTypeLabelKey: "kata"}, d.registerLabels())
	require.Equal(t, map[string]string{"pool": "gate", config.DaemonRuntimeTypeLabelKey: "spoofed"}, d.machineLabels())
}
