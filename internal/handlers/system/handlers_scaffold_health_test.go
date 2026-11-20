package system_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestHealth_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.HealthRequest, pb.HealthResponse]{
		{
			Name: "ok",
			Req:  &pb.HealthRequest{},
			Check: func(t *testing.T, resp *pb.HealthResponse) {
				if resp.Status != "ok" || resp.Version != "v2" {
					t.Fatalf("Health = %+v, want status ok, version v2", resp)
				}
			},
		},
	}, svc.Health)
}

// TestHealth_OverConnect calls Health through a real Connect client and
// server, anonymously: Health is a public procedure.
func TestHealth_OverConnect(t *testing.T) {
	t.Parallel()

	_, client := system.NewTestSystemServer(t, system.WithoutPrincipal())
	resp, err := client.Health(context.Background(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("Health status = %q, want ok", resp.Status)
	}
}
