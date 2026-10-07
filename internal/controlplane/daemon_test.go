package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
)

type resumeRecorder struct {
	daemonv1connect.UnimplementedDaemonServiceHandler
	gotAuth string
	gotID   string
	err     error
}

func (r *resumeRecorder) ResumeDaemon(_ context.Context, req *connect.Request[daemonv1.ResumeDaemonRequest]) (*connect.Response[daemonv1.ResumeDaemonResponse], error) {
	r.gotAuth = req.Header().Get("Authorization")
	r.gotID = req.Msg.GetDaemonId()
	if r.err != nil {
		return nil, r.err
	}
	return connect.NewResponse(&daemonv1.ResumeDaemonResponse{}), nil
}

func serveDaemonService(t *testing.T, rec *resumeRecorder) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(daemonv1connect.NewDaemonServiceHandler(rec))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// The resume goes to controlplane.v1.DaemonService — the route control-plane
// serves — carrying the caller's token as the Bearer it derives the owner from.
func TestDaemonClient_ResumeDaemonCallsControlPlaneDaemonServiceAsTheCaller(t *testing.T) {
	rec := &resumeRecorder{}
	url := serveDaemonService(t, rec)

	if err := NewDaemonClient(url).ResumeDaemon(context.Background(), " rlat_bound ", "daemon-1"); err != nil {
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
func TestDaemonClient_ResumeDaemonKeepsTheControlPlaneCode(t *testing.T) {
	rec := &resumeRecorder{err: connect.NewError(connect.CodeFailedPrecondition, nil)}
	url := serveDaemonService(t, rec)

	err := NewDaemonClient(url).ResumeDaemon(context.Background(), "jwt", "daemon-1")
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v (err %v), want FailedPrecondition", got, err)
	}
}
