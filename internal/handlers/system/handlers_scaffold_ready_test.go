package system_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

type temporalHealth struct{ err error }

func (h temporalHealth) CheckHealth(context.Context, *client.CheckHealthRequest) (*client.CheckHealthResponse, error) {
	return &client.CheckHealthResponse{}, h.err
}

type connection bool

func (c connection) IsConnected() bool { return bool(c) }

func TestReady_Generated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		deps       system.Deps
		wantStatus string
	}{
		{
			name:       "no dependencies in use",
			wantStatus: "ready",
		},
		{
			name: "every dependency up",
			deps: system.Deps{
				Repo:          pinger{},
				Temporal:      temporalHealth{},
				NATSConnected: func() bool { return true },
				StreamingHub:  connection(true),
			},
			wantStatus: "ready",
		},
		{
			name: "every dependency down",
			deps: system.Deps{
				Repo:          pinger{err: errors.New("connection refused")},
				Temporal:      temporalHealth{err: errors.New("deadline exceeded")},
				NATSConnected: func() bool { return false },
				StreamingHub:  connection(false),
			},
			wantStatus: "not_ready: db: connection refused; temporal: deadline exceeded; nats: disconnected; nats-streaming: disconnected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := system.NewTestSystem(t, system.WithSystemDeps(tt.deps))
			tdd.RunRPCCases(t, []tdd.RPCCase[pb.ReadyRequest, pb.ReadyResponse]{
				{
					Name: "status",
					Req:  connect.NewRequest(&pb.ReadyRequest{}),
					Check: func(t *testing.T, resp *connect.Response[pb.ReadyResponse]) {
						if resp.Msg.Status != tt.wantStatus {
							t.Fatalf("Ready status = %q, want %q", resp.Msg.Status, tt.wantStatus)
						}
						if resp.Msg.Timestamp == 0 {
							t.Fatal("Ready timestamp is unset")
						}
					},
				},
			}, svc.Ready)
		})
	}
}
