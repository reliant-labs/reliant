// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

func dataEvent(s string) *toolexec.TerminalOutputEvent {
	return &toolexec.TerminalOutputEvent{Data: []byte(s)}
}

func TestCoalesceOutput_KeepsOrderAndStopsAtTheCapOrALifecycleEvent(t *testing.T) {
	ch := make(chan *toolexec.TerminalOutputEvent, 8)
	ch <- dataEvent("b")
	ch <- dataEvent("c")
	ch <- dataEvent("defg") // would pass the cap of 6 with "abc"
	ch <- dataEvent("h")

	frame, next, closed := coalesceOutput([]byte("a"), ch, 6)
	require.Equal(t, "abc", string(frame))
	require.Equal(t, "defg", string(next.Data), "the chunk that did not fit is handed back, not lost")
	require.False(t, closed)

	frame, next, closed = coalesceOutput(next.Data, ch, 6)
	require.Equal(t, "defgh", string(frame))
	require.Nil(t, next, "nothing more is queued")
	require.False(t, closed)

	exit := &toolexec.TerminalOutputEvent{Closed: true}
	ch <- dataEvent("j")
	ch <- exit
	ch <- dataEvent("k")
	frame, next, _ = coalesceOutput([]byte("i"), ch, 64)
	require.Equal(t, "ij", string(frame))
	require.Same(t, exit, next, "a lifecycle event ends the frame and is handed back")

	close(ch)
	frame, next, closed = coalesceOutput(nil, ch, 64)
	require.Equal(t, "k", string(frame))
	require.Nil(t, next)
	require.True(t, closed)
}

func TestSplitIncompleteUTF8(t *testing.T) {
	euro := "€" // 3 bytes: e2 82 ac
	for _, tc := range []struct {
		name, in, complete, rest string
	}{
		{"ascii", "abc", "abc", ""},
		{"whole multibyte", "a" + euro, "a" + euro, ""},
		{"one byte of three", "a" + euro[:1], "a", euro[:1]},
		{"two bytes of three", "a" + euro[:2], "a", euro[:2]},
		{"only a cut character", euro[:2], "", euro[:2]},
		{"invalid byte passes through", "a\xff", "a\xff", ""},
		{"stray continuation passes through", "a\x82", "a\x82", ""},
		{"empty", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete, rest := splitIncompleteUTF8([]byte(tc.in))
			require.Equal(t, tc.complete, string(complete))
			require.Equal(t, tc.rest, string(rest))
		})
	}
}

// dialOutputTest opens a terminal whose session's output is the events
// already queued on output, and returns the socket after "init".
func dialOutputTest(t *testing.T, output chan *toolexec.TerminalOutputEvent) *websocket.Conn {
	t.Helper()
	router := &outputTestRouter{
		pingTestRouter: &pingTestRouter{terminalCommandRouter: newTerminalCommandRouter(), inputs: make(chan []byte, 8)},
		output:         output,
	}
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?token=test&workingDir=/tmp", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)
	return conn
}

// readUntilEnd reads frames until a non-output frame, returning the output
// frames and that last frame.
func readUntilEnd(t *testing.T, conn *websocket.Conn) ([]string, wsMessage) {
	t.Helper()
	var frames []string
	for {
		var msg wsMessage
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Type != "output" {
			return frames, msg
		}
		frames = append(frames, msg.Data)
	}
}

// A burst of output already queued goes to the browser in a few frames, each
// within the cap, with every byte in order, and the exit after it.
func TestTerminalWS_CoalescesQueuedOutputInOrder(t *testing.T) {
	const chunks = 48
	output := make(chan *toolexec.TerminalOutputEvent, chunks+1)
	var want bytes.Buffer
	for i := 0; i < chunks; i++ {
		chunk := fmt.Sprintf("%05d:%s|", i, strings.Repeat("x", 4090))
		want.WriteString(chunk)
		output <- dataEvent(chunk)
	}
	output <- &toolexec.TerminalOutputEvent{Closed: true}

	frames, end := readUntilEnd(t, dialOutputTest(t, output))
	require.Equal(t, "exit", end.Type, "the exit follows every byte of output")
	require.Equal(t, want.String(), strings.Join(frames, ""), "every byte, in order")
	maxFrames := want.Len()/wsOutputFrameMax + 2
	require.LessOrEqual(t, len(frames), maxFrames, "queued chunks are coalesced, not sent one frame each")
	for _, f := range frames {
		require.LessOrEqual(t, len(f), wsOutputFrameMax)
	}
}

// A character the PTY read cut in two reaches the browser whole, not as two
// replacement characters.
func TestTerminalWS_CharacterSplitAcrossChunksArrivesWhole(t *testing.T) {
	euro := "€"
	output := make(chan *toolexec.TerminalOutputEvent, 4)
	output <- dataEvent("price: " + euro[:1])
	conn := dialOutputTest(t, output)
	go func() {
		// The second half arrives later, in its own read.
		time.Sleep(50 * time.Millisecond)
		output <- dataEvent(euro[1:] + " each")
		output <- &toolexec.TerminalOutputEvent{Closed: true}
	}()

	frames, end := readUntilEnd(t, conn)
	require.Equal(t, "exit", end.Type)
	require.Equal(t, "price: "+euro+" each", strings.Join(frames, ""))
	require.NotContains(t, strings.Join(frames, ""), "\uFFFD")
}

// An output hop that fell behind ends the session with its error, after the
// output that did arrive: the browser prints it and reconnects.
func TestTerminalWS_OverflowEndsSessionWithError(t *testing.T) {
	output := make(chan *toolexec.TerminalOutputEvent, 2)
	output <- dataEvent("partial")
	output <- &toolexec.TerminalOutputEvent{Error: toolexec.TerminalOutputOverflowMessage, Overflow: true}

	conn := dialOutputTest(t, output)
	frames, end := readUntilEnd(t, conn)
	require.Equal(t, "partial", strings.Join(frames, ""))
	require.Equal(t, "error", end.Type)
	require.Equal(t, toolexec.TerminalOutputOverflowMessage, end.Data)
	require.Empty(t, end.Code, "no code: the browser prints the message and reconnects")

	requireServerClose(t, conn, websocket.CloseInternalServerErr)
}

// The shell exiting closes the socket with 1000 at once, which the browser
// reads as "Session ended" — not after the pong timeout as a 1006 the browser
// answers with a new shell.
func TestTerminalWS_ShellExitClosesWithNormalClosure(t *testing.T) {
	output := make(chan *toolexec.TerminalOutputEvent, 1)
	output <- &toolexec.TerminalOutputEvent{Closed: true}

	conn := dialOutputTest(t, output)
	_, end := readUntilEnd(t, conn)
	require.Equal(t, "exit", end.Type)
	requireServerClose(t, conn, websocket.CloseNormalClosure)
}

// requireServerClose reads the server's close frame, which must come promptly
// (well inside the pong timeout) with code.
func requireServerClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr, "the server closes the websocket with a close frame")
	require.Equal(t, code, closeErr.Code)
	require.Less(t, time.Since(start), 3*time.Second)
}
