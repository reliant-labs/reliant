// Copyright (c) 2025 Reliant Labs

package rpcclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

type plans struct {
	reliantv1connect.UnimplementedPlanServiceHandler
	err  error
	body string
}

func (p plans) GetPlan(context.Context, *reliantv1.GetPlanRequest) (*reliantv1.GetPlanResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &reliantv1.GetPlanResponse{Plan: &reliantv1.Plan{Description: p.body}}, nil
}

func serve(t *testing.T, p plans) string {
	t.Helper()
	server := connect.NewServer(svcerr.InterceptorV2())
	reliantv1connect.RegisterPlanServiceHandler(server, p)
	mux := http.NewServeMux()
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0), connecthttp.WithSendMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

var protocols = map[string][]connecthttp.Option{
	"connect":  nil,
	"grpc-web": {connecthttp.WithGRPCWeb()},
}

// TestReasonCrossesTheWire is the v1 contract a v2 client keeps only through
// rpcclient: the reason a server refused for, and its other metadata,
// readable from the error alone (svcerr.MetaOf).
func TestReasonCrossesTheWire(t *testing.T) {
	refusal := svcerr.WithMetadata(svcerr.WithReason(svcerr.PermissionDenied("not yours"), "not_owner"), "X-Scopes", "a b")
	url := serve(t, plans{err: refusal})
	for name, opts := range protocols {
		t.Run(name, func(t *testing.T) {
			client := reliantv1connect.NewPlanServiceClient(rpcclient.New(http.DefaultClient, url, opts))
			_, err := client.GetPlan(t.Context(), &reliantv1.GetPlanRequest{})
			if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
				t.Fatalf("code = %v, want permission_denied (err %v)", got, err)
			}
			var ce *connect.Error
			if !errors.As(err, &ce) {
				t.Fatalf("errors.As(*connect.Error) = false for %T", err)
			}
			meta := svcerr.MetaOf(err)
			if got := meta.Get(svcerr.ReasonHeader); got != "not_owner" {
				t.Errorf("reason = %q, want not_owner", got)
			}
			if got := meta.Get("X-Scopes"); got != "a b" {
				t.Errorf("X-Scopes = %q, want %q", got, "a b")
			}
			if !strings.Contains(err.Error(), "not yours") {
				t.Errorf("Error() = %q, want the server's message", err.Error())
			}
		})
	}
}

// TestNoReadLimit: connect-go v2 caps a received message at 4 MiB by default,
// and v1, which every reliant client ran on, capped nothing. A client from
// rpcclient reads past the cap, as before.
func TestNoReadLimit(t *testing.T) {
	big := strings.Repeat("x", 6<<20)
	url := serve(t, plans{body: big})
	for name, opts := range protocols {
		t.Run(name, func(t *testing.T) {
			client := reliantv1connect.NewPlanServiceClient(rpcclient.New(http.DefaultClient, url, opts))
			resp, err := client.GetPlan(t.Context(), &reliantv1.GetPlanRequest{})
			if err != nil {
				t.Fatalf("GetPlan of a 6 MiB plan: %v", err)
			}
			if got := len(resp.GetPlan().GetDescription()); got != len(big) {
				t.Fatalf("content length = %d, want %d", got, len(big))
			}
		})
	}
}
