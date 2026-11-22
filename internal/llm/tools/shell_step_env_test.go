// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

const stepEnvTestKey = "rlsk_shell_test_secret_0123456789"

func shellStepEnv() *daemon.StepEnv {
	return &daemon.StepEnv{
		Set:    map[string]string{"ANTHROPIC_BASE_URL": "https://gw.example"},
		Secret: daemon.SecretEnv{"ANTHROPIC_AUTH_TOKEN": stepEnvTestKey},
		Unset:  []string{"ANTHROPIC_API_KEY"},
	}
}

// echoingDaemon records each request and answers with output that prints the
// step's key, as a CLI that dumps its environment would.
type echoingDaemon struct {
	daemon.Client
	reqs []daemon.RunCommandRequest
	bg   int
}

func (d *echoingDaemon) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	d.reqs = append(d.reqs, *req)
	out := "ANTHROPIC_AUTH_TOKEN=" + stepEnvTestKey + "\n" + strings.Repeat("x", 100)
	return &daemon.CommandResult{Stdout: out, Stderr: "auth " + stepEnvTestKey, Combined: out}, nil
}

func (d *echoingDaemon) StartBackground(context.Context, *daemon.RunCommandRequest) (string, error) {
	d.bg++
	return "bg-1", nil
}

func stepEnvShellCall(t *testing.T, tc *rctx.ToolContext, input string) ToolResponse {
	t.Helper()
	resp, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: input})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

// A run step's step env (server-set) reaches the daemon as StepEnv, never as
// Env, and its key is gone from the result before anything else sees it.
func TestShellTool_RunStepEnvIsAppliedAndRedacted(t *testing.T) {
	d := &echoingDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: true, RunStepEnv: shellStepEnv()}
	resp := stepEnvShellCall(t, tc, `{"command":"claude -p x","credentials":"none","timeout":60000}`)
	if len(d.reqs) != 1 {
		t.Fatalf("daemon got %d requests", len(d.reqs))
	}
	req := d.reqs[0]
	if req.StepEnv == nil || req.StepEnv.Secret["ANTHROPIC_AUTH_TOKEN"] != stepEnvTestKey {
		t.Fatalf("StepEnv = %v, want the run step's", req.StepEnv)
	}
	for name, value := range req.Env {
		if strings.Contains(value, stepEnvTestKey) || name == "ANTHROPIC_AUTH_TOKEN" || name == "ANTHROPIC_BASE_URL" {
			t.Errorf("the step env travelled in Env: %s", name)
		}
	}
	if strings.Contains(resp.Content, stepEnvTestKey) || strings.Contains(resp.Metadata, stepEnvTestKey) {
		t.Fatalf("the key reached the result: %s", resp.Content)
	}
	var out ShellOutput
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Stdout, "ANTHROPIC_AUTH_TOKEN=[redacted: ANTHROPIC_AUTH_TOKEN]") || out.Stderr != "auth [redacted: ANTHROPIC_AUTH_TOKEN]" {
		t.Errorf("redacted output: %+v", out)
	}
}

// The redaction comes before the output is cut, so a key split by a
// truncation marker cannot survive it.
func TestShellTool_RunStepEnvRedactsBeforeTruncating(t *testing.T) {
	d := &echoingDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: true, RunStepEnv: shellStepEnv()}
	resp := stepEnvShellCall(t, tc, `{"command":"claude -p x","max_output":40}`)
	if strings.Contains(resp.Content, stepEnvTestKey[:12]) {
		t.Fatalf("a piece of the key survived truncation: %s", resp.Content)
	}
}

// Only the server's context carries a step env: a model's call (no RunStep)
// gets none even if one were on the context, and tool input cannot name one.
func TestShellTool_OnlyARunStepGetsAStepEnv(t *testing.T) {
	d := &echoingDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStepEnv: shellStepEnv()}
	stepEnvShellCall(t, tc, `{"command":"claude -p x"}`)
	if len(d.reqs) != 1 || d.reqs[0].StepEnv != nil {
		t.Fatalf("a model's call carried a step env: %+v", d.reqs)
	}

	d = &echoingDaemon{}
	tc = &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: true}
	stepEnvShellCall(t, tc, `{"command":"claude -p x","step_env":{"secret":{"ANTHROPIC_AUTH_TOKEN":"forged"}}}`)
	for _, req := range d.reqs {
		if req.StepEnv != nil {
			t.Fatalf("tool input set a step env: %+v", req.StepEnv)
		}
	}
}

// A step env never runs in the background, nor through another machine's
// client: refused, and nothing runs.
func TestShellTool_RunStepEnvRefusesBackground(t *testing.T) {
	for _, input := range []string{
		`{"command":"claude -p x","run_in_background":true}`,
		`{"command":"claude -p x &"}`,
	} {
		d := &echoingDaemon{}
		tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
			Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: true, RunStepEnv: shellStepEnv()}
		resp := stepEnvShellCall(t, tc, input)
		if !resp.IsError || d.bg != 0 || len(d.reqs) != 0 {
			t.Errorf("%s: want a refusal and nothing run, got %+v (bg %d, runs %d)", input, resp, d.bg, len(d.reqs))
		}
	}
}
