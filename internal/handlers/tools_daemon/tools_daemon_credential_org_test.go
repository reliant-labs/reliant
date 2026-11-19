package tools_daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// orgIntrospector is the token authority: every credential acts as userID in
// orgID, the org the person chose when they paired the machine.
type orgIntrospector struct{ userID, orgID string }

func (i orgIntrospector) IntrospectAccount(context.Context, string) (auth.AccessTokenAccount, error) {
	return auth.AccessTokenAccount{Principal: &fat.Principal{
		TokenID: "tok-1", OrgID: i.orgID, ActingUserID: i.userID,
		Scopes: fat.SetOf(fat.ScopeDaemonConnect, fat.ScopeReliantAPI),
	}}, nil
}

// linkReadyOrgs records the org each link-ready notification names: the org
// daemonevents.Publisher puts on daemon.v1.events.connected.
type linkReadyOrgs struct {
	mu   sync.Mutex
	orgs chan string
}

func (l *linkReadyOrgs) OnDaemonConnected(string, string)    {}
func (l *linkReadyOrgs) OnDaemonDisconnected(string, string) {}
func (l *linkReadyOrgs) OnDaemonLinkReady(_, _, orgID, _, _, _ string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.orgs <- orgID
}

// connectInOrg dials ConnectDaemon through the production daemon auth
// interceptor, with a credential the authority says acts in credentialOrg,
// registers a daemon that asserts the org "org-a" in its labels (its person's
// default org), answers the gateway's link probe, and returns the org the
// connected event names.
func connectInOrg(t *testing.T, credentialOrg string) string {
	t.Helper()
	repo := &MockRepository{
		UpsertDaemonFunc:           func(context.Context, *db.Daemon) error { return nil },
		UpsertDaemonAttachmentFunc: func(context.Context, *db.DaemonAttachment) error { return nil },
		ListProjectsFunc:           func(context.Context, db.ProjectFilters) ([]*db.Project, error) { return nil, nil },
		// The machine holds no checkouts yet.
		ListProjectDaemonsForDaemonFunc: func(context.Context, string) ([]*core.ProjectDaemon, error) { return nil, nil },
		ListProjectDaemonDetachmentsForDaemonFunc: func(context.Context, string) ([]*core.ProjectDaemonDetachment, error) {
			return nil, nil
		},
		ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return nil, nil },
	}
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	ready := &linkReadyOrgs{orgs: make(chan string, 4)}
	svc.AddConnectionListener(ready)

	daemonAuth, err := interceptors.NewDaemonAuthInterceptor(orgIntrospector{userID: "u-1", orgID: credentialOrg})
	require.NoError(t, err)
	routes := http.NewServeMux()
	svc.Register(routes, connect.WithInterceptors(svcerr.Interceptor(), daemonAuth))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(serverkit.NewStreams().Install(routes), &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC()).ConnectDaemon(ctx)
	credential, err := fat.Mint()
	require.NoError(t, err)
	stream.RequestHeader().Set("Authorization", "Bearer "+credential.Plaintext)
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{
			DaemonId: "laptop-1", Hostname: "h", Platform: "linux", DaemonType: "local",
			Labels: map[string]string{"org": "org-a", "org_id": "org-a"},
		},
	}}))

	// The daemon: answer every command (the link probe first) until the
	// gateway has told its listeners the link is ready.
	streamErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				streamErr <- err
				return
			}
			cmd := msg.GetDaemonCommand()
			if cmd == nil {
				continue
			}
			_ = stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
				DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
					RequestId: cmd.GetRequestId(), CommandType: cmd.GetCommandType(), Success: true,
					Payload: []byte(`{"discovered":[]}`),
				},
			}})
		}
	}()
	select {
	case org := <-ready.orgs:
		return org
	case err := <-streamErr:
		t.Fatalf("the gateway ended the daemon stream before the link was ready: %v", err)
		return ""
	case <-ctx.Done():
		t.Fatalf("the link never became ready (probe %q unanswered?)", toolexec.LinkProbeCommand)
		return ""
	}
}

// A self-paired machine is recorded in the org it was paired in. A credential
// of org B connecting for a person whose default org is A, from a daemon that
// asserts A, puts B on the connected event: the org comes from the
// authenticated credential, never from anything the daemon says.
func TestConnectDaemon_ConnectedEventNamesTheCredentialOrg(t *testing.T) {
	require.Equal(t, "org-b", connectInOrg(t, "org-b"))
}

// A credential the authority names no org for (self-hosted, an older control
// plane) names none on the event: the control plane then records the person's
// default org, as before the field existed.
func TestConnectDaemon_ConnectedEventNamesNoOrgWithoutOne(t *testing.T) {
	require.Equal(t, "", connectInOrg(t, ""))
}
