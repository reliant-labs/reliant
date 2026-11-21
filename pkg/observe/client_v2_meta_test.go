package observe

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A client built from ClientStackV2 reads the reason a server refused for
// from the error alone (svcerr.MetaOf), as a v1 client read ce.Meta().
func TestClientStackV2_ErrorCarriesServerMetadata(t *testing.T) {
	s := serveV2(t, v2Handlers{
		unary: func(context.Context) error {
			return svcerr.WithReason(svcerr.PermissionDenied("not yours"), "not_owner")
		},
		stream: func(_ context.Context, send func() error) error {
			if err := send(); err != nil {
				return err
			}
			return svcerr.WithReason(svcerr.Unavailable("try later"), "busy")
		},
	}, svcerr.InterceptorV2())
	stack, err := NewClientStackV2(ClientStackDepsV2{})
	if err != nil {
		t.Fatal(err)
	}
	client := stack.NewClient(s.url)

	err = client.CallUnary(context.Background(), v2UnarySpec, &structpb.Struct{}, &structpb.Struct{})
	if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "not_owner" {
		t.Errorf("unary reason = %q, want not_owner (err %v)", got, err)
	}

	cs, err := client.CallServerStream(context.Background(), v2StreamSpec, &structpb.Struct{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	if err := cs.Receive(&structpb.Struct{}); err != nil {
		t.Fatalf("first message: %v", err)
	}
	err = cs.Receive(&structpb.Struct{})
	if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "busy" {
		t.Errorf("stream reason = %q, want busy (err %v)", got, err)
	}
}
