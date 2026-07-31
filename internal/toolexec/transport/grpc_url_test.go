package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// stubToolsDaemonService accepts a ConnectDaemon stream and records the first
// message it receives, so a test can assert the daemon's registration actually
// reached the gateway rather than dying in the client transport.
type stubToolsDaemonService struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	got chan *reliantv1.DaemonMessage
}

func (s *stubToolsDaemonService) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	msg, err := stream.Receive()
	if err != nil {
		return err
	}
	s.got <- msg
	return stream.Send(&reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{DaemonId: "d-1"},
		},
	})
}

// startStubGateway starts an h2c ToolsDaemonService on loopback and returns its
// base URL (always http://) plus the channel of received registrations.
func startStubGateway(t *testing.T) (string, chan *reliantv1.DaemonMessage) {
	t.Helper()
	stub := &stubToolsDaemonService{got: make(chan *reliantv1.DaemonMessage, 1)}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(stub))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL, stub.got
}

// TestDaemonDialsGatewayByURLScheme pins the schemes a daemon may be pointed
// at. `forge cluster urls` prints the gateway as grpc://host:port, so that form
// must connect — silently failing to connect while `daemon status` still claims
// the daemon is up is the worst possible outcome.
func TestDaemonDialsGatewayByURLScheme(t *testing.T) {
	for _, scheme := range []string{"http", "grpc"} {
		t.Run(scheme, func(t *testing.T) {
			baseURL, got := startStubGateway(t)
			dialURL := scheme + "://" + baseURL[len("http://"):]

			httpClient, resolvedURL, err := transport.NewDaemonHTTPClient(bootstrap.DaemonBootstrapConfig{
				AuthToken: "daemon-token",
				GRPCURL:   dialURL,
				TLSMode:   bootstrap.TLSModeH2C,
			})
			require.NoError(t, err)

			client := reliantv1connect.NewToolsDaemonServiceClient(httpClient, resolvedURL, connect.WithGRPC())
			stream := client.ConnectDaemon(context.Background())
			t.Cleanup(func() { _ = stream.CloseRequest() })

			err = stream.Send(&reliantv1.DaemonMessage{
				Message: &reliantv1.DaemonMessage_Register{
					Register: &reliantv1.DaemonRegister{Hostname: "test-host"},
				},
			})
			require.NoError(t, err, "sending daemon registration over %s:// must reach the gateway", scheme)

			select {
			case msg := <-got:
				require.Equal(t, "test-host", msg.GetRegister().GetHostname())
			case <-context.Background().Done():
			}
		})
	}
}
