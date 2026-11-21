package testkit_test

import (
	"context"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
)

var refuseSpecV2 = connectv2.Spec{Procedure: "/testkit.v1.Meta/Refuse", StreamType: connectv2.StreamTypeUnary}

// NewConnectClientV2's client reads the reason and metadata a handler
// refused with from the error alone, as a v1 test read ce.Meta().
func TestNewConnectClientV2_ErrorCarriesServerMetadata(t *testing.T) {
	t.Parallel()
	_, client := testkit.NewConnectClientV2(t, func(server *connectv2.Server) {
		server.Register(connectv2.Method{Spec: refuseSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			if err := stream.Receive(&structpb.Struct{}); err != nil {
				return err
			}
			return svcerr.WithMetadata(svcerr.WithReason(svcerr.PermissionDenied("not yours"), "scope_missing"),
				"X-Forge-Error-Scopes", "deploy:write")
		}})
	}, clientV2)

	err := client.CallUnary(context.Background(), refuseSpecV2, &structpb.Struct{}, &structpb.Struct{})
	meta := svcerr.MetaOf(err)
	if got := meta.Get(svcerr.ReasonHeader); got != "scope_missing" {
		t.Errorf("reason = %q, want scope_missing (err %v)", got, err)
	}
	if got := meta.Get("X-Forge-Error-Scopes"); got != "deploy:write" {
		t.Errorf("scopes = %q, want deploy:write", got)
	}
}
