package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type cancelLoginTerminal struct {
	cancel   context.CancelFunc
	restored bool
}

func (f *cancelLoginTerminal) MakeRaw() (func(), error) {
	f.cancel()
	return func() { f.restored = true }, nil
}
func (*cancelLoginTerminal) Size() (int, int)       { return 80, 24 }
func (*cancelLoginTerminal) OnResize(func()) func() { return func() {} }

func TestRemoteLoginCancellationInterruptsRead(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-init", true: "raw-terminal"}[initialized], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				if initialized {
					_ = ws.WriteJSON(loginFrame{Type: "init"})
				} else {
					cancel()
				}
				// No further frames: the client must close, not wait for output.
				_, _, _ = ws.ReadMessage()
			}))
			defer srv.Close()
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			require.NoError(t, err)
			defer ws.Close()
			terminal := &cancelLoginTerminal{cancel: cancel}
			saved := loginTerminal
			loginTerminal = terminal
			defer func() { loginTerminal = saved }()
			done := make(chan error, 1)
			go func() { done <- pumpRemoteLogin(ctx, ws, loginTarget{}, strings.NewReader(""), io.Discard) }()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("cancellation did not interrupt websocket read")
			}
			require.Equal(t, initialized, terminal.restored)
		})
	}
}
