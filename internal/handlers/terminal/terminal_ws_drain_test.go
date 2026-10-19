// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"
)

// When the API server shuts down it ends every terminal websocket (the mount
// is serverkit.LongLived, bound to the listener's drain). The websocket must close
// with 1012, service restart — the browser reconnects on anything but 1000,
// which it reads as "the shell exited" — and the daemon's session must still
// be closed.
//
// Before, nothing ended the websocket: it was hijacked, so Shutdown did not
// wait for it, and the process exit dropped the TCP connection. The browser
// saw 1006, and the handler's deferred terminal.close never ran, leaking the
// shell and its PTY on the user's machine on every rollout.
func TestTerminalWS_ServerDrainClosesWithServiceRestartAndClosesTheSession(t *testing.T) {
	router := newTerminalCommandRouter()
	// Mounted and served as the API server mounts and serves it (internal/grpc
	// NewServer): a LongLived route on a serverkit listener, whose drain
	// (serverkit.DrainHTTP, what Stop runs) ends it.
	streams := serverkit.NewStreams()
	handler := serverkit.LongLived(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = serverkit.NewHTTPServer(serverkit.Config{}, handler, streams)
	srv.Start()
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?token=test&workingDir=/tmp"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- serverkit.DrainHTTP(ctx, srv.Config, streams) }()

	// Reading on drives the client's half of the close handshake: gorilla
	// answers the server's close frame and then reports it.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.True(t, errors.As(err, &closeErr), "the server must close the websocket with a close frame, got %v", err)
	require.Equal(t, websocket.CloseServiceRestart, closeErr.Code,
		"1012 is the reconnect signal; 1000 would tell the browser the shell exited")

	require.NoError(t, <-drained, "the drain waits for the handler, which returns once the close handshake is done")
	requireSingleClose(t, router)
}
