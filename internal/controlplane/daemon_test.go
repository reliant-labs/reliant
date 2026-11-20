package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
)

type resumeRecorder struct {
	daemonv1connect.UnimplementedDaemonServiceHandler
	gotAuth string
	gotID   string
	err     error
}

func (r *resumeRecorder) ResumeDaemon(ctx context.Context, req *daemonv1.ResumeDaemonRequest) (*daemonv1.ResumeDaemonResponse, error) {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		r.gotAuth = info.RequestHeader().Get("Authorization")
	}
	r.gotID = req.GetDaemonId()
	if r.err != nil {
		return nil, r.err
	}
	return &daemonv1.ResumeDaemonResponse{}, nil
}

func serveDaemonService(t *testing.T, rec *resumeRecorder) string {
	t.Helper()
	mux := http.NewServeMux()
	server := connect.NewServer(svcerr.InterceptorV2())
	daemonv1connect.RegisterDaemonServiceHandler(server, rec)
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// The resume goes to controlplane.v1.DaemonService — the route control-plane
// serves — carrying the caller's token as the Bearer it derives the owner from.
func TestResumeDaemon_CallsControlPlaneDaemonServiceAsTheCaller(t *testing.T) {
	rec := &resumeRecorder{}
	url := serveDaemonService(t, rec)

	if err := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: url}}).ResumeDaemon(context.Background(), " rlat_bound ", "daemon-1"); err != nil {
		t.Fatalf("ResumeDaemon: %v", err)
	}
	if rec.gotAuth != "Bearer rlat_bound" {
		t.Fatalf("authorization = %q, want %q", rec.gotAuth, "Bearer rlat_bound")
	}
	if rec.gotID != "daemon-1" {
		t.Fatalf("daemon_id = %q, want daemon-1", rec.gotID)
	}
}

// Callers branch on the Connect code (FailedPrecondition: not suspended), so
// it must survive the client unwrapped.
func TestResumeDaemon_KeepsTheControlPlaneCode(t *testing.T) {
	rec := &resumeRecorder{err: connect.NewError(connect.CodeFailedPrecondition, "")}
	url := serveDaemonService(t, rec)

	err := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: url}}).ResumeDaemon(context.Background(), "jwt", "daemon-1")
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v (err %v), want FailedPrecondition", got, err)
	}
}
