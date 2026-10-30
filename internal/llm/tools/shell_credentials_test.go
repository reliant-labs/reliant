// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type requestRecordingDaemon struct {
	daemon.Client
	reqs []daemon.RunCommandRequest
}

func (d *requestRecordingDaemon) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	d.reqs = append(d.reqs, *req)
	return &daemon.CommandResult{}, nil
}

func (d *requestRecordingDaemon) StartBackground(_ context.Context, req *daemon.RunCommandRequest) (string, error) {
	d.reqs = append(d.reqs, *req)
	return "proc-1", nil
}

func runShellParams(t *testing.T, p ShellParams) (*requestRecordingDaemon, ToolResponse) {
	t.Helper()
	d := &requestRecordingDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Project: &db.Project{ID: "p", Path: "/work/p"}, Daemon: d}
	resp, err := (&shellTool{}).Execute(tc, p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return d, resp
}

func TestShellTool_CredentialsNoneReachesTheDaemon(t *testing.T) {
	for _, bg := range []bool{false, true} {
		d, resp := runShellParams(t, ShellParams{Command: "git push", Credentials: "none", RunInBackground: bg,
			Env: map[string]string{"GH_TOKEN": "ghp_from_params", "KEEP": "1"}})
		if resp.IsError || len(d.reqs) != 1 {
			t.Fatalf("background=%v: resp=%+v reqs=%d", bg, resp, len(d.reqs))
		}
		req := d.reqs[0]
		if req.Credentials != "none" {
			t.Errorf("background=%v: Credentials = %q, want none", bg, req.Credentials)
		}
		// A daemon that predates the field applies env and ignores Credentials:
		// the neutral assignments must already be in it, and a caller's own
		// GH_TOKEN must not survive into it.
		if v, ok := req.Env["GH_TOKEN"]; !ok || v != "" {
			t.Errorf("background=%v: env GH_TOKEN = %q (set=%v), want an explicit empty value", bg, v, ok)
		}
		if req.Env["GIT_TERMINAL_PROMPT"] != "0" || req.Env["KEEP"] != "1" {
			t.Errorf("background=%v: env = %v", bg, req.Env)
		}
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "ghp_from_params") {
			t.Errorf("background=%v: the caller's token went onto the wire: %s", bg, raw)
		}
	}
}

func TestShellTool_DefaultSendsNoCredentialsSetting(t *testing.T) {
	d, _ := runShellParams(t, ShellParams{Command: "ls", Env: map[string]string{"A": "b"}})
	// The caller's env, plus the chat the command runs for (ChatIDEnv).
	if got := d.reqs[0]; got.Credentials != "" || len(got.Env) != 2 || got.Env["A"] != "b" || got.Env[ChatIDEnv] == "" {
		t.Fatalf("the default request changed: %+v", got)
	}
}

func TestShellTool_UnknownCredentialsIsRefused(t *testing.T) {
	d, resp := runShellParams(t, ShellParams{Command: "git push", Credentials: "nne"})
	if !resp.IsError || len(d.reqs) != 0 {
		t.Fatalf("a typo must refuse the command, not run it with credentials: resp=%+v reqs=%d", resp, len(d.reqs))
	}
}

// The model is never shown `credentials` when a workflow binds it, and cannot
// override it.
func TestShellTool_CredentialsIsBindable(t *testing.T) {
	tool := NewShellTool(ShellPlatformUnknown)
	bound, err := BindTool(tool, Bindings{"credentials": LiteralBinding("none")})
	if err != nil {
		t.Fatalf("binding credentials: %v", err)
	}
	if strings.Contains(string(mustSchema(t, bound)), `"credentials"`) {
		t.Fatal("a bound credentials parameter is still in the schema the model sees")
	}
	if _, err := ApplyBindings("shell", `{"command":"ls","credentials":"inherit"}`, bound.(BindableTool).Bindings()); err == nil {
		t.Fatal("a call that overrides a bound credentials must be refused")
	}
	merged, err := ApplyBindings("shell", `{"command":"ls"}`, bound.(BindableTool).Bindings())
	if err != nil || !strings.Contains(merged, `"credentials":"none"`) {
		t.Fatalf("bound value was not merged: %q, %v", merged, err)
	}
}

func mustSchema(t *testing.T, tool Tool) []byte {
	t.Helper()
	raw, err := json.Marshal(tool.ParamSchema())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
