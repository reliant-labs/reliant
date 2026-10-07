// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/builddefaults"
)

// helperOutputEnv makes the test binary act as the generator: generate()
// clears the process environment, which must never happen to `go test`
// itself, so it runs in a child process.
const helperOutputEnv = "DOCGEN_CLI_TEST_OUTPUT"

func TestMain(m *testing.M) {
	if out := os.Getenv(helperOutputEnv); out != "" {
		if _, err := generate(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestReferenceIgnoresTheGeneratingEnvironment pins cli.md to the binary's
// compiled-in defaults. Flag defaults read the environment while the command
// tree is built, so cli.md generated inside a dev stack documented
// `--server http://localhost:8090` — committed to main that way, and rewritten
// by every agent whose shell had a different RELIANT_SERVER_URL. CI's drift
// gate cannot catch that on its own: its environment sets none of these.
func TestReferenceIgnoresTheGeneratingEnvironment(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "cli.md")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		helperOutputEnv+"="+out,
		"RELIANT_SERVER_URL=http://envleak.invalid:8090",
		"RELIANT_GATEWAY_URL=http://envleak.invalid:39190",
		"TOOLS_DAEMON_PORT=7777",
		"DAEMON_LISTEN_PORT=7778",
		"TLS_CERT_FILE=/envleak/cert.pem",
		"HOME=/envleak/home",
		"XDG_CONFIG_HOME=/envleak/xdg",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate in a child process: %v\n%s", err, output)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read generated reference: %v", err)
	}
	reference := string(data)

	for _, line := range strings.Split(reference, "\n") {
		if strings.Contains(line, "envleak") || strings.Contains(line, "7777") || strings.Contains(line, "7778") {
			t.Errorf("cli.md carries the generating environment:\n  %s", line)
		}
	}
	for _, want := range []string{
		"| `--server` | `string` | `" + builddefaults.ServerURL + "` |",
		"| `--gateway` | `string` | `" + builddefaults.GatewayURL + "` |",
		"| `--listen-port` | `int` | `9190` |",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("cli.md should document the compiled-in default %q", want)
		}
	}
}
