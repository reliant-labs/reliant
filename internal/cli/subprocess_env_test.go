package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The reproduction: LOG_LEVEL=debug (a variable the app under development
// legitimately exports) makes k3d write ANSI-coloured DEBU lines to STDOUT,
// ahead of the JSON, so `k3d cluster list -o json` fails to parse with
// "invalid character '\x1b'". forge must scrub it for its own parsed calls.
func TestScrubSubprocessLogEnv_RemovesLogVerbosityVars(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{
		"PATH=/usr/bin",
		"HOME=/home/dev",
		"LOG_LEVEL=debug",
		"K3D_LOG_LEVEL=trace",
		"DEBUG=1",
		"DOCKER_HOST=unix:///var/run/docker.sock",
	}
	scrubSubprocessLogEnv(cmd)

	got := strings.Join(cmd.Env, "\n")
	for _, gone := range []string{"LOG_LEVEL=", "K3D_LOG_LEVEL=", "DEBUG="} {
		if strings.Contains(got, gone) {
			t.Errorf("%s should have been scrubbed, env is:\n%s", gone, got)
		}
	}
	// The subprocess still has to be able to RUN and find docker.
	for _, kept := range []string{"PATH=/usr/bin", "HOME=/home/dev", "DOCKER_HOST=unix:///var/run/docker.sock"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s must be preserved, env is:\n%s", kept, got)
		}
	}
}

// A variable that merely CONTAINS a log-var name is not one.
func TestScrubSubprocessLogEnv_KeepsLookalikeNames(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"MY_LOG_LEVEL=debug", "LOG_LEVEL_OVERRIDE=x", "APP_DEBUG=1"}
	scrubSubprocessLogEnv(cmd)

	if len(cmd.Env) != 3 {
		t.Errorf("look-alike names must be preserved, got: %v", cmd.Env)
	}
}

// With no explicit Env, the scrub seeds from the process environment rather
// than handing the subprocess an empty one (which would strip PATH).
func TestScrubSubprocessLogEnv_SeedsFromProcessEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")

	cmd := exec.Command("true")
	scrubSubprocessLogEnv(cmd)

	if len(cmd.Env) == 0 {
		t.Fatal("Env must be seeded from os.Environ(), not left empty")
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "LOG_LEVEL=") {
			t.Fatalf("LOG_LEVEL survived the scrub: %s", kv)
		}
	}
	if os.Getenv("LOG_LEVEL") != "debug" {
		t.Error("the developer's own environment must not be modified")
	}
}

func TestFirstLine_StripsAnsiAndBounds(t *testing.T) {
	got := firstLine("\x1b[37mDEBU\x1b[0m[0000] DOCKER_SOCK=/var/run/docker.sock\nsecond line")
	if strings.Contains(got, "\x1b") {
		t.Errorf("ANSI escapes should be stripped, got %q", got)
	}
	if !strings.HasPrefix(got, "DEBU") || strings.Contains(got, "second line") {
		t.Errorf("want only the first line, got %q", got)
	}
	if firstLine("   \n") != "(blank)" {
		t.Errorf("blank first line should render as (blank), got %q", firstLine("   \n"))
	}
}
