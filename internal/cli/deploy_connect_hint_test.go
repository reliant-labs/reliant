package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type offlineHintCaller struct{}

func (offlineHintCaller) Call(context.Context, string, any, any) error { return errors.New("offline") }

// withConnectHintEnv replaces the hint's env and TTY seams for one test.
func withConnectHintEnv(t *testing.T, env map[string]string, tty bool) {
	t.Helper()
	getenv, interactive := connectHintGetenv, connectHintInteractive
	t.Cleanup(func() { connectHintGetenv, connectHintInteractive = getenv, interactive })
	connectHintGetenv = func(k string) string { return env[k] }
	connectHintInteractive = func(io.Writer) bool { return tty }
}

func hostedOutcome() (context.Context, *deployOutcome) {
	ctx, o := withDeployOutcome(context.Background(), "prod")
	noteDeployRelease(ctx, "v0.3.0")
	o.client, o.project = offlineHintCaller{}, "acme"
	return ctx, o
}

const wantHint = "Connect this machine so agents can work on acme: reliant daemon start --attach ."

func TestConnectHintAfterHostedDeploy(t *testing.T) {
	hintEnv := map[string]string{connectHintEnv: "  reliant daemon start --attach .\n"}

	t.Run("printed when every condition holds, before RESULT", func(t *testing.T) {
		withConnectHintEnv(t, hintEnv, true)
		ctx, o := hostedOutcome()
		var out bytes.Buffer
		if err := finishDeployOutcome(ctx, o, deployCmdFlags{}, nil, &out); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		i, j := strings.Index(s, wantHint), strings.LastIndex(s, "RESULT: ok")
		if i < 0 || j < i {
			t.Fatalf("want the hint before RESULT:\n%s", s)
		}
		if lines := strings.Split(strings.TrimRight(s, "\n"), "\n"); !strings.HasPrefix(lines[len(lines)-1], "RESULT:") {
			t.Errorf("RESULT is not the last line:\n%s", s)
		}
	})

	cases := []struct {
		name  string
		env   map[string]string
		tty   bool
		flags deployCmdFlags
		err   error
		self  bool
	}{
		{name: "no hint env", env: map[string]string{connectHintEnv: " \t"}, tty: true},
		{name: "--json", env: hintEnv, tty: true, flags: deployCmdFlags{jsonOut: true}},
		{name: "--dry-run", env: hintEnv, tty: true, flags: deployCmdFlags{dryRun: true}},
		{name: "CI", env: map[string]string{connectHintEnv: "x", "CI": "true"}, tty: true},
		{name: "not a terminal", env: hintEnv, tty: false},
		{name: "source ci", env: map[string]string{connectHintEnv: "x", deploySourceEnv: "ci"}, tty: true},
		{name: "source console", env: map[string]string{connectHintEnv: "x", deploySourceEnv: "console"}, tty: true},
		{name: "failed deploy", env: hintEnv, tty: true, err: errors.New("boom")},
		{name: "self-run target", env: hintEnv, tty: true, self: true},
	}
	for _, c := range cases {
		t.Run("not printed: "+c.name, func(t *testing.T) {
			withConnectHintEnv(t, c.env, c.tty)
			ctx, o := hostedOutcome()
			if c.self {
				o.client = nil
			}
			var out bytes.Buffer
			_ = finishDeployOutcome(ctx, o, c.flags, c.err, &out)
			if strings.Contains(out.String(), "Connect this machine") {
				t.Errorf("hint printed:\n%s", out.String())
			}
		})
	}

	t.Run("control characters are stripped and length capped", func(t *testing.T) {
		withConnectHintEnv(t, map[string]string{connectHintEnv: "run\x1b[31m me\a\r\n"}, true)
		var out bytes.Buffer
		printConnectHint(&out, "ac\x1bme", deployCmdFlags{})
		if got := out.String(); got != "\nConnect this machine so agents can work on acme: run[31m me\n" {
			t.Errorf("got %q", got)
		}
		if n := len([]rune(sanitizeConnectHint(strings.Repeat("y", 500)))); n != connectHintMaxLen {
			t.Errorf("hint is %d runes, want %d", n, connectHintMaxLen)
		}
	})
}
