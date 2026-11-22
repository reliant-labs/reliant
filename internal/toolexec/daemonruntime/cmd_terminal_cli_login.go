// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// TerminalCreateCLILoginCommand opens a terminal session that runs one vendor
// agent CLI's own sign-in (`reliant machine login <vendor>`, run from another
// computer): vendorcli.LoginScript installs the CLI under $HOME when it is
// missing, then execs the vendor's login, on a PTY with no login shell. The
// session streams, resizes and closes as terminal.create's does.
//
// The request names only the vendor. The program is built here, from this
// daemon's own vendor table, so the server never sends a command line and this
// is no remote-exec primitive: an id the table does not know is refused.
//
// The PTY carries the vendor's URL or device code out and the person's
// keystrokes (possibly a pasted one-time code) in, like an SSH session. Nothing
// here logs, records or buffers those bytes, and nothing may: only the vendor
// CLI itself writes its credential, to its own file under $HOME.
//
// A daemon older than this command answers "unknown command"; the server tells
// the person to update Reliant on that machine rather than open a shell.
const TerminalCreateCLILoginCommand = toolexec.TerminalCreateCLILoginCommand

func init() {
	RegisterCommand(TerminalCreateCLILoginCommand, handleTerminalCreateCLILogin)
}

type terminalCreateCLILoginRequest struct {
	Vendor string `json:"vendor"`
	Cols   uint16 `json:"cols,omitempty"`
	Rows   uint16 `json:"rows,omitempty"`
}

// errCLILoginNeedsPOSIXShell: the vendor recipes are POSIX sh scripts.
var errCLILoginNeedsPOSIXShell = errors.New("signing a vendor CLI in needs a POSIX shell (sh), which this machine does not have")

// cliLoginShell finds the sh the login script runs under. A variable so a test
// can point it at a fake.
var cliLoginShell = func() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errCLILoginNeedsPOSIXShell
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return "", errCLILoginNeedsPOSIXShell
	}
	return sh, nil
}

// cliLoginScript is vendorcli.LoginScript; a test replaces it with a script
// that needs no network.
var cliLoginScript = vendorcli.LoginScript

func handleTerminalCreateCLILogin(_ context.Context, payload []byte) ([]byte, error) {
	var req terminalCreateCLILoginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if _, ok := vendorcli.Lookup(req.Vendor); !ok {
		return nil, fmt.Errorf("unknown vendor CLI %q: this machine knows %v", req.Vendor, vendorcli.IDs())
	}
	tm := terminalManager()
	if tm == nil {
		return nil, fmt.Errorf("terminal manager not initialized")
	}
	script, err := cliLoginScript(req.Vendor)
	if err != nil {
		return nil, fmt.Errorf("sign-in recipe for %s: %w", req.Vendor, err)
	}
	sh, err := cliLoginShell()
	if err != nil {
		return nil, err
	}
	// $HOME: where the vendor writes its credential and the installer puts
	// the CLI (cloud machines persist $HOME, nothing outside it).
	session, err := tm.CreateCommandSession(terminal.GetHomeDir(), sh, []string{"-c", script}, req.Cols, req.Rows)
	if err != nil {
		logging.Warn("[Terminal] CLI sign-in session not created", "vendor", req.Vendor, "error", err)
		return nil, fmt.Errorf("create session: %w", err)
	}
	var pid int
	if p := session.Process(); p != nil {
		pid = p.Pid
	}
	// The vendor and the session, never a byte of what it prints or reads.
	logging.Info("[Terminal] CLI sign-in session created", "session_id", session.ID, "pid", pid, "vendor", req.Vendor)
	return json.Marshal(terminalCreateResponse{SessionID: session.ID, PID: pid, WorkingDir: session.WorkingDir})
}
