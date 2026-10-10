// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// foreignIDGateway plays the gateway's rejectForeignDaemon: a registration
// asserting foreignID is refused as owned by another user, and any other
// registration is acknowledged with mintedID — the gateway minting this
// account its own id.
type foreignIDGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	foreignID string
	mintedID  string
	// refusal is the error the gateway answers a foreign id with.
	refusal func() error
	// refuseEverything also refuses an empty id: the credential itself pins a
	// daemon owned by someone else, so asserting nothing cannot help.
	refuseEverything bool

	mu       sync.Mutex
	asserted []string
}

func (g *foreignIDGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	msg, err := stream.Receive()
	if err != nil {
		return err
	}
	asserted := msg.GetRegister().GetDaemonId()
	g.mu.Lock()
	g.asserted = append(g.asserted, asserted)
	g.mu.Unlock()

	if g.refuseEverything || asserted == g.foreignID {
		return g.refusal()
	}
	if err := stream.Send(&reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: g.mintedID, UserId: "user-b"},
		},
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (g *foreignIDGateway) registrations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.asserted...)
}

func serveGateway(t *testing.T, gw reliantv1connect.ToolsDaemonServiceHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(gw))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

// legacyForeignRefusal is exactly what the gateway answered before it tagged
// the refusal with a reason — what production was returning when a laptop
// wedged on another account's saved id.
func legacyForeignRefusal() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("daemon id is owned by another user"))
}

// taggedForeignRefusal is the refusal with its machine-readable reason.
func taggedForeignRefusal() error {
	err := connect.NewError(connect.CodePermissionDenied, errors.New("daemon id is owned by another user"))
	err.Meta().Set("x-forge-error-reason", "daemon_id_owned_by_another_user")
	return err
}

func setAsideFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, bootstrap.DaemonIDFileName+".foreign-*"))
	require.NoError(t, err)
	return matches
}

// newDaemonClientForInstance is a client the way `daemon start` builds one:
// with a server origin, which is what makes it persist the id the gateway
// assigns.
func newDaemonClientForInstance(t *testing.T, dataDir, gatewayURL, daemonID string) *daemonClient {
	t.Helper()
	t.Setenv("DAEMON_WORKING_DIR", t.TempDir())
	client, err := newDaemonClient(bootstrap.DaemonBootstrapConfig{
		AuthToken: "rlat_test",
		GRPCURL:   gatewayURL,
		TLSMode:   bootstrap.TLSModeH2C,
		DataDir:   dataDir,
		ServerURL: "https://api.example.com",
		DaemonID:  daemonID,
	})
	require.NoError(t, err)
	return client
}

// runUntilDone runs the client and returns run's error, failing the test if
// it does not return within the deadline.
func runUntilDone(t *testing.T, client *daemonClient) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- client.run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("daemon kept running after the gateway refused its identity")
		return nil
	}
}

// THE regression. Two accounts on one laptop shared one saved daemon id; the
// second account's daemon asserted the first account's id, the gateway refused
// it as owned by another user, and the daemon stopped for good — on every
// start, forever, until a human found and renamed the file.
//
// The saved id came from this instance's own file, so the daemon can fix this
// itself: move the file aside and register as new, which the gateway answers
// with this account's own id.
func TestForeignSavedDaemonIDIsSetAsideAndReRegistered(t *testing.T) {
	for name, refusal := range map[string]func() error{
		"gateway tags the refusal with a reason":     taggedForeignRefusal,
		"gateway predates the reason (prod 2026-10)": legacyForeignRefusal,
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			require.NoError(t, bootstrap.WriteDaemonID(dataDir, "account-a-daemon"))

			gw := &foreignIDGateway{foreignID: "account-a-daemon", mintedID: "account-b-daemon", refusal: refusal}
			client := newDaemonClientForInstance(t, dataDir, serveGateway(t, gw), "")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = client.run(ctx) }()

			waitForStream(t, dataDir, daemonstate.StreamConnected)
			require.Equal(t, []string{"account-a-daemon", ""}, gw.registrations(),
				"refused once for the foreign id, then exactly one re-registration asserting no id")

			require.Eventually(t, func() bool {
				return bootstrap.ReadDaemonID(dataDir) == "account-b-daemon"
			}, 5*time.Second, 20*time.Millisecond, "the gateway-minted id must become this instance's saved identity")

			moved := setAsideFiles(t, dataDir)
			require.Len(t, moved, 1, "the foreign id is moved aside, never deleted")
			content, err := os.ReadFile(moved[0])
			require.NoError(t, err)
			require.Equal(t, "account-a-daemon", strings.TrimSpace(string(content)),
				"the set-aside file keeps the other account's id intact")
		})
	}
}

// The recovery must not loop. If registering with no id is refused as well,
// the credential itself names a daemon another account owns, and redialing
// cannot change that — one retry, then stop and say so.
func TestForeignDaemonIDRecoveryRetriesAtMostOnce(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, bootstrap.WriteDaemonID(dataDir, "account-a-daemon"))

	gw := &foreignIDGateway{foreignID: "account-a-daemon", refusal: taggedForeignRefusal, refuseEverything: true}
	client := newDaemonClientForInstance(t, dataDir, serveGateway(t, gw), "")

	err := runUntilDone(t, client)

	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.True(t, bootstrap.IsForeignDaemonIDError(err), "the cause survives wrapping for the CLI to classify")
	require.Equal(t, []string{"account-a-daemon", ""}, gw.registrations(), "exactly one retry per start")
	require.Len(t, setAsideFiles(t, dataDir), 1)
	message := strings.ToLower(err.Error())
	require.Contains(t, message, "another reliant account")
	require.NotContains(t, message, "token rejected")
	require.NotContains(t, message, "verify the pat")
}

// Only an id read from this instance's file is this daemon's to move. An id a
// launcher supplied explicitly is left exactly where it was found.
func TestForeignDaemonIDNotFromLocalFileIsLeftAlone(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, bootstrap.WriteDaemonID(dataDir, "saved-id"))

	gw := &foreignIDGateway{foreignID: "launcher-id", refusal: taggedForeignRefusal}
	client := newDaemonClientForInstance(t, dataDir, serveGateway(t, gw), "launcher-id")

	runErr := runUntilDone(t, client)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(runErr))
	require.Equal(t, []string{"launcher-id"}, gw.registrations())
	require.Equal(t, "saved-id", bootstrap.ReadDaemonID(dataDir))
	require.Empty(t, setAsideFiles(t, dataDir))
}
