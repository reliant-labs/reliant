// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

func testStepEnv() *StepEnv {
	return &StepEnv{
		Set:    map[string]string{"ANTHROPIC_BASE_URL": "https://gw.example"},
		Secret: SecretEnv{"ANTHROPIC_AUTH_TOKEN": "rlsk_live_secret_value"},
		Unset:  []string{"ANTHROPIC_API_KEY"},
	}
}

// Apply drops every name the step env touches, inherited or requested, and
// appends its own values last.
func TestStepEnvApply(t *testing.T) {
	t.Parallel()
	got := testStepEnv().Apply([]string{
		"PATH=/bin",
		"ANTHROPIC_API_KEY=sk-ant-person",
		"ANTHROPIC_BASE_URL=https://attacker.example",
		"ANTHROPIC_AUTH_TOKEN=old",
	})
	want := []string{"PATH=/bin", "ANTHROPIC_BASE_URL=https://gw.example", "ANTHROPIC_AUTH_TOKEN=rlsk_live_secret_value"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Apply:\n got %q\nwant %q", got, want)
	}
	if (*StepEnv)(nil).Apply([]string{"A=1"})[0] != "A=1" {
		t.Fatal("a nil step env changes nothing")
	}
}

func TestStepEnvRedact(t *testing.T) {
	t.Parallel()
	env := testStepEnv()
	got := env.Redact("token is rlsk_live_secret_value, again rlsk_live_secret_value; url https://gw.example")
	want := "token is [redacted: ANTHROPIC_AUTH_TOKEN], again [redacted: ANTHROPIC_AUTH_TOKEN]; url https://gw.example"
	if got != want {
		t.Fatalf("Redact:\n got %q\nwant %q", got, want)
	}
	if (*StepEnv)(nil).Redact("x") != "x" {
		t.Fatal("a nil step env redacts nothing")
	}
}

// The secret values never print, in any format or in slog; JSON (the wire)
// carries them.
func TestSecretEnvNeverPrints(t *testing.T) {
	t.Parallel()
	env := testStepEnv()
	req := &RunCommandRequest{Command: "claude -p x", StepEnv: env}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("req", "secret", env.Secret, "env", env)
	for _, s := range []string{
		fmt.Sprintf("%v", req), fmt.Sprintf("%+v", req), fmt.Sprintf("%#v", env.Secret),
		fmt.Sprintf("%s", env.Secret), fmt.Sprintf("%v", *env), fmt.Sprintf("%+v", *env), logged.String(),
	} {
		if strings.Contains(s, "rlsk_live_secret_value") {
			t.Fatalf("the secret printed: %s", s)
		}
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"step_env":{"set":{"ANTHROPIC_BASE_URL":"https://gw.example"},"secret":{"ANTHROPIC_AUTH_TOKEN":"rlsk_live_secret_value"},"unset":["ANTHROPIC_API_KEY"]}`) {
		t.Fatalf("wire: %s", wire)
	}
}

// The key reaches the command through StepEnv, after the credential policy:
// credentials: none strips a token-looking name the request's Env carries,
// and a step env's secret survives it. A name it unsets is gone though the
// daemon inherited it, and the request's Env cannot redirect its gateway.
func TestRunCommand_StepEnvSurvivesCredentialsNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printf is POSIX")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-person")

	cmd := `printf 'base=%s token=%s apikey=%s\n' "$ANTHROPIC_BASE_URL" "$ANTHROPIC_AUTH_TOKEN" "$ANTHROPIC_API_KEY"`
	for _, tc := range []struct {
		name        string
		credentials string
		stepEnv     *StepEnv
		want        string
	}{
		{
			name: "env alone is stripped", credentials: daemonpolicy.CredentialsNone,
			want: "base= token= apikey=",
		},
		{
			name: "step env, credentials none", credentials: daemonpolicy.CredentialsNone, stepEnv: testStepEnv(),
			want: "base=https://gw.example token=rlsk_live_secret_value apikey=",
		},
		{
			name: "step env, inherit", stepEnv: testStepEnv(),
			want: "base=https://gw.example token=rlsk_live_secret_value apikey=",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.credentials == daemonpolicy.CredentialsNone {
				testkit.Slow(t, "runs a real shell command in the credential sandbox")
			}
			res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
				Command:    cmd,
				WorkingDir: t.TempDir(),
				// What a caller might try: the key and a redirect through Env.
				Env:         map[string]string{"ANTHROPIC_AUTH_TOKEN": "rlsk_via_env", "ANTHROPIC_BASE_URL": "https://attacker.example"},
				Credentials: tc.credentials,
				StepEnv:     tc.stepEnv,
				TimeoutMs:   30000,
			})
			if err != nil {
				t.Fatalf("RunCommand: %v", err)
			}
			if res.ExitCode == credsandbox.RefusedExitCode && tc.credentials == daemonpolicy.CredentialsNone {
				t.Skipf("this kernel cannot provide the credential sandbox: %s", res.Combined)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit %d: %s", res.ExitCode, res.Combined)
			}
			if got := strings.TrimSpace(res.Stdout); got != tc.want {
				t.Fatalf("command environment:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A step env never runs in the background: its output there escapes the
// redaction, and the process would outlive the key's step.
func TestStartBackground_RefusesStepEnv(t *testing.T) {
	t.Parallel()
	_, err := NewLocalClient().StartBackground(context.Background(), &RunCommandRequest{Command: "claude -p x", StepEnv: testStepEnv()})
	if err == nil || !strings.Contains(err.Error(), "background") {
		t.Fatalf("want a refusal, got %v", err)
	}
}
