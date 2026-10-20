// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// pingTestRouter reports the PTY input a connection forwards.
type pingTestRouter struct {
	*terminalCommandRouter
	inputs chan []byte
}

func (r *pingTestRouter) SendTerminalInput(_ context.Context, _, _ string, data []byte) error {
	r.inputs <- data
	return nil
}

// After a laptop sleeps, the browser can hold a dead socket in OPEN state.
// It pings on wake and replaces the socket when nothing answers, so the
// server must answer an app-level ping with a pong — and must not forward
// the ping to the shell as keystrokes.
func TestTerminalWS_AnswersAppLevelPing(t *testing.T) {
	router := &pingTestRouter{terminalCommandRouter: newTerminalCommandRouter(), inputs: make(chan []byte, 8)}
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?token=test&workingDir=/tmp", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)

	require.NoError(t, conn.WriteJSON(wsResizeMessage{Type: wsClientPing}))

	var msg wsMessage
	require.NoError(t, conn.ReadJSON(&msg))
	require.Equal(t, wsServerPong, msg.Type)

	// A keystroke after the ping still reaches the shell, and the ping did not.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ls\n")))
	select {
	case got := <-router.inputs:
		require.Equal(t, "ls\n", string(got), "a ping is a control message, not PTY input")
	case <-time.After(5 * time.Second):
		t.Fatal("input was not forwarded")
	}
}
