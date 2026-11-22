package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

// connectHintEnv is how the process that launched forge (the reliant CLI)
// names the command that connects this machine as an agent sandbox. It is
// set only when the machine is not connected; forge never runs it and never
// registers anything, it only prints it after an interactive hosted deploy.
const connectHintEnv = "FORGE_HOST_CONNECT_HINT"

// connectHintMaxLen bounds the host-supplied hint printed to the terminal.
const connectHintMaxLen = 200

// Seams, vars so tests can replace them.
var (
	connectHintGetenv      = os.Getenv
	connectHintInteractive = func(w io.Writer) bool { return cliutil.StdinIsTTY() && cliutil.IsTerminal(w) }
)

// printConnectHint prints the host's connect hint after a successful hosted
// deploy, when a person ran it at a terminal outside CI.
func printConnectHint(w io.Writer, app string, f deployCmdFlags) {
	hint := sanitizeConnectHint(connectHintGetenv(connectHintEnv))
	if hint == "" || f.jsonOut || !deployWrites(f) || cliev.InCI(connectHintGetenv) || !connectHintInteractive(w) {
		return
	}
	runID := ""
	if run, err := f.promote.run.resolve(connectHintGetenv); err == nil {
		runID = run.ID
	}
	if deploySource(connectHintGetenv(deploySourceEnv), runID) != "terminal" {
		return
	}
	if app = sanitizeConnectHint(app); app == "" {
		app = "this project"
	}
	fmt.Fprintf(w, "\nConnect this machine so agents can work on %s: %s\n", app, hint)
}

// sanitizeConnectHint makes host-supplied text safe for one terminal line:
// control characters dropped, whitespace trimmed, length capped.
func sanitizeConnectHint(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > connectHintMaxLen {
		s = string(r[:connectHintMaxLen])
	}
	return s
}
