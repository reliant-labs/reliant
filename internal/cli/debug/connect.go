package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

func newConnectCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "connect <host:port>",
		Short: "Drive a dlv server forge did not start (e.g. an env's delve runner)",
		Long: `Make a running headless dlv server (API v2, --accept-multiclient) the
debug session, so break / continue / eval / ... drive it.

Use it for a dlv someone else started: a workload declared with
forge.OnHost { runner = "delve" } (dlv on 127.0.0.1:<delve_port>, default
2345), the scaffolded .air-debug.toml (127.0.0.1:2345), or one you launched by
hand. forge owns neither that server nor its target: 'forge debug stop' clears
the breakpoints this session set, resumes the target and disconnects, and both
keep running.

Examples:
  forge debug connect 127.0.0.1:2345
  forge debug connect :2345`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// normalizeAddr turns ":2345" and "2345" into "127.0.0.1:2345".
func normalizeAddr(addr string) string {
	switch {
	case strings.HasPrefix(addr, ":"):
		return "127.0.0.1" + addr
	case !strings.Contains(addr, ":"):
		return "127.0.0.1:" + addr
	}
	return addr
}

func runConnect(addr string, jsonOutput bool) error {
	addr = normalizeAddr(addr)
	existing, _ := debugSvc().LoadSession(".")
	if existing != nil && (!existing.Connected || existing.Addr != addr) {
		return fmt.Errorf("a debug session is already active (dlv at %s); run 'forge debug stop' first", existing.Addr)
	}
	d := dbgsvc.NewDelveDebugger()
	if err := d.Connect(addr); err != nil {
		return fmt.Errorf("connecting to dlv at %s: %w", addr, err)
	}
	defer d.Disconnect()
	st, stErr := d.State()
	if st == dbgsvc.TargetExited {
		return fmt.Errorf("the dlv server at %s is serving an exited process: %w", addr, stErr)
	}
	if stErr != nil {
		return fmt.Errorf("reading target state: %w", stErr)
	}
	pid, err := d.ResolvePID()
	if err != nil {
		return fmt.Errorf("reading target pid: %w", err)
	}

	session := &dbgsvc.SessionInfo{
		Type:      "delve",
		Addr:      addr,
		PID:       pid,
		Binary:    fmt.Sprintf("pid:%d", pid),
		Owned:     false,
		Connected: true,
		// A running target may already be on its way to a stop nobody asked
		// for; let the next continue wait for it rather than resume past it.
		Resumed: st == dbgsvc.TargetRunning,
		Started: time.Now(),
	}
	session.BreakpointIDs = carriedBreakpoints(existing, pid)
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(session)
		return nil
	}
	fmt.Printf("Connected to dlv at %s (target pid %d; forge owns neither — 'stop' clears breakpoints and leaves both running)\n", addr, pid)
	printRunState(st == dbgsvc.TargetHalted, "as found")
	return nil
}

// carriedBreakpoints is what a repeated connect to the same dlv server keeps
// of the session it replaces: the breakpoints that session created are still
// on the server, and stop must clear them and only them. existing is the
// saved session (nil, or a connected one to this address: runConnect refuses
// anything else). A server now serving a different target (it was restarted)
// holds none of them, so they are dropped rather than cleared by a stale id.
func carriedBreakpoints(existing *dbgsvc.SessionInfo, pid int) []int {
	if existing == nil || existing.PID != pid {
		return nil
	}
	return existing.BreakpointIDs
}
