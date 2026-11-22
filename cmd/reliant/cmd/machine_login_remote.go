// Copyright (c) 2026 Reliant Labs
package cmd

import (
	crypto_tls "crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// loginFrame is the websocket's server-to-client message
// (internal/handlers/terminal wsMessage).
type loginFrame struct {
	Type        string `json:"type"`
	Data        string `json:"data,omitempty"`
	Code        string `json:"code,omitempty"`
	MachineName string `json:"machine_name,omitempty"`
}

// loginResize is the resize message the websocket reads.
type loginResize struct {
	Type string `json:"type"`
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// loginTerminal is the person's terminal: raw mode for the session, its size,
// and its resize notifications. A variable so tests can run without a TTY.
var loginTerminal terminalControl = realTerminal{}

type terminalControl interface {
	// MakeRaw puts the terminal in raw mode and returns how to restore it.
	MakeRaw() (restore func(), err error)
	Size() (cols, rows int)
	// OnResize calls fn on every size change until stop is called.
	OnResize(fn func()) (stop func())
}

type realTerminal struct{}

func (realTerminal) MakeRaw() (func(), error) {
	fd := int(os.Stdin.Fd())
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, saved) }, nil
}

func (realTerminal) Size() (int, int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0, 0
	}
	return cols, rows
}

func (realTerminal) OnResize(fn func()) func() { return onTerminalResize(fn) }

// cliLoginURL is the websocket URL for a sign-in on daemonID.
func cliLoginURL(server, daemonID, vendor string, cols, rows int) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/") + terminal.TerminalCLILoginWSPath)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := url.Values{"daemonId": {daemonID}, "vendor": {vendor}}
	if cols > 0 && rows > 0 {
		q.Set("cols", strconv.Itoa(cols))
		q.Set("rows", strconv.Itoa(rows))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// runRemoteCLILogin runs the vendor's sign-in on another machine through the
// API, in this terminal, like an SSH session: the machine's output is written
// here as it comes, keystrokes go there as typed, and the terminal is in raw
// mode until the session ends (restored on every path out). Nothing here
// records or logs a byte of it.
//
// The CLI login is sent as the Authorization header, never in the URL.
func runRemoteCLILogin(cmd *cobra.Command, target loginTarget, vendor vendorcli.Vendor) error {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return err
	}
	cols, rows := loginTerminal.Size()
	wsURL, err := cliLoginURL(conn.ServerURL, target.id, vendor.ID, cols, rows)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{NetDialContext: transport.LocalhostDialContext, Proxy: http.ProxyFromEnvironment}
	if conn.skipTLSVerify || shouldSkipTLSVerify(conn.ServerURL) {
		dialer.TLSClientConfig = &crypto_tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev only, as connection.httpClient
	}
	header := http.Header{"Authorization": {"Bearer " + conn.Token}}
	ws, resp, err := dialer.DialContext(cmd.Context(), wsURL, header)
	if err != nil {
		return explainLoginDial(conn, target, err, resp)
	}
	defer ws.Close()
	return pumpRemoteLogin(ws, target, cmd.InOrStdin(), cmd.OutOrStdout())
}

// explainLoginDial turns a refused websocket handshake into what to do.
func explainLoginDial(conn *connection, target loginTarget, err error, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("reaching %s: %w", conn.describeServer(), err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusNotFound && !strings.Contains(msg, "no such machine"):
		return fmt.Errorf("the Reliant server at %s predates 'reliant machine login' for another machine; sign in on %s itself instead (run 'reliant machine login' in its terminal)",
			conn.ServerURL, target.display())
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("no machine %q of yours: 'reliant machine list' shows yours", target.display())
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s\n  hint: run 'reliant auth login'", msg)
	}
	return fmt.Errorf("starting the sign-in on %s: %s (HTTP %d)", target.display(), msg, resp.StatusCode)
}

// pumpRemoteLogin carries the session until it ends.
func pumpRemoteLogin(ws *websocket.Conn, target loginTarget, stdin io.Reader, stdout io.Writer) error {
	var first loginFrame
	if err := ws.ReadJSON(&first); err != nil {
		return fmt.Errorf("starting the sign-in on %s: %w", target.display(), err)
	}
	if first.Type == "error" {
		return loginFrameError(target, first)
	}
	if first.Type != "init" {
		return fmt.Errorf("starting the sign-in on %s: unexpected %q", target.display(), first.Type)
	}

	restore, err := loginTerminal.MakeRaw()
	if err != nil {
		return fmt.Errorf("putting the terminal in raw mode: %w", err)
	}
	defer restore()

	// gorilla allows one concurrent writer: input and resizes share it.
	var writeMu sync.Mutex
	write := func(kind int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteMessage(kind, data)
	}
	sendSize := func() {
		cols, rows := loginTerminal.Size()
		if cols <= 0 || rows <= 0 {
			return
		}
		b, _ := json.Marshal(loginResize{Type: "resize", Cols: uint32(cols), Rows: uint32(rows)})
		_ = write(websocket.TextMessage, b)
	}
	stopResize := loginTerminal.OnResize(sendSize)
	defer stopResize()

	// Input: the person's keystrokes, as typed. The goroutine stays blocked
	// in Read after the session ends; the command exits right after.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				if werr := write(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		var f loginFrame
		if err := ws.ReadJSON(&f); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			return fmt.Errorf("the sign-in session on %s ended: %w", target.display(), err)
		}
		switch f.Type {
		case "output":
			_, _ = io.WriteString(stdout, f.Data)
		case "exit":
			return nil
		case "error":
			restore()
			return loginFrameError(target, f)
		}
	}
}

// loginFrameError is an "error" frame as the person reads it.
func loginFrameError(target loginTarget, f loginFrame) error {
	// The name a --machine hint takes (daemonplacement.MachineArg), not the
	// server's display label.
	name := target.display()
	switch f.Code {
	case terminal.WSErrorDaemonOutdated:
		return fmt.Errorf("machine %s runs a Reliant too old for 'reliant machine login': update Reliant on it, then run this again", name)
	case "daemon_unavailable", "no_machine":
		return fmt.Errorf("machine %s is not running or not connected: start it ('reliant machine start %s') and try again", name, name)
	case "machine_not_answering":
		return fmt.Errorf("machine %s is connected but did not answer: try again in a moment", name)
	}
	if f.Data == "" {
		return errors.New("the sign-in session failed")
	}
	return errors.New(f.Data)
}
