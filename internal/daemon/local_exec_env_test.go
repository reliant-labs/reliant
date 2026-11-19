// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// #2823: an agent's foreground command ran with the daemon's raw environment,
// so a `reliant daemon start` it ran inherited the hosting daemon's
// DAEMON_DATA_DIR and wrote that daemon's log and runtime record. A command
// gets the environment daemonpolicy.ChildEnv builds, as the daemon's exec and
// background paths already did: no daemon identity, and the agent-shell marker.
func TestRunCommand_EnvOmitsTheDaemonsIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printenv is POSIX")
	}
	// A credentialed command prepares the credentialed scratch under HOME;
	// keep it off the real one, which this machine's daemon owns.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DAEMON_DATA_DIR", "/host/daemon-data")
	t.Setenv("DAEMON_GRPC_URL", "https://gateway.host.example")
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")

	for name, env := range map[string]map[string]string{
		"no request env": nil,
		"request env":    {"KEEP": "kept"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
				Command:   `printf 'data=%s grpc=%s type=%s shell=%s keep=%s\n' "$DAEMON_DATA_DIR" "$DAEMON_GRPC_URL" "$RELIANT_DAEMON_TYPE" "$` + daemonpolicy.AgentShellEnv + `" "$KEEP"`,
				Env:       env,
				TimeoutMs: 30000,
			})
			if err != nil {
				t.Fatalf("RunCommand: %v", err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit %d:\n%s", res.ExitCode, res.Combined)
			}
			want := "data= grpc= type=managed shell=1 keep=" + env["KEEP"]
			if got := strings.TrimSpace(res.Stdout); got != want {
				t.Fatalf("command environment:\n got %q\nwant %q", got, want)
			}
		})
	}
}
