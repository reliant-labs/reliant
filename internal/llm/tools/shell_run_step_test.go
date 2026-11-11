// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The clamp decision (reliant#2003): a model's call is capped at
// MaxShellTimeout; a workflow run step runs for its node's declared timeout,
// up to MaxRunStepTimeout and never unbounded.
func TestShellTimeoutMs(t *testing.T) {
	ms := func(d time.Duration) int { return int(d.Milliseconds()) }
	for _, tc := range []struct {
		name       string
		requested  int
		runStep    bool
		want       int
		wantCapped bool
	}{
		{"model default", 0, false, ms(DefaultShellTimeout), false},
		{"model negative is the default", -5, false, ms(DefaultShellTimeout), false},
		{"model under the cap", ms(2 * time.Minute), false, ms(2 * time.Minute), false},
		{"model at the cap", ms(MaxShellTimeout), false, ms(MaxShellTimeout), false},
		{"model over the cap is capped at 10m", ms(30 * time.Minute), false, ms(MaxShellTimeout), true},
		{"model asking for hours is still capped at 10m", ms(6 * time.Hour), false, ms(MaxShellTimeout), true},
		{"run step default", 0, true, ms(DefaultShellTimeout), false},
		{"run step 30m is honoured", ms(30 * time.Minute), true, ms(30 * time.Minute), false},
		{"run step 345m is honoured", ms(345 * time.Minute), true, ms(345 * time.Minute), false},
		{"run step at the hard max", ms(MaxRunStepTimeout), true, ms(MaxRunStepTimeout), false},
		{"run step over the hard max is capped at 12h", ms(48 * time.Hour), true, ms(MaxRunStepTimeout), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, capped := shellTimeoutMs(tc.requested, tc.runStep)
			if got != tc.want || capped != tc.wantCapped {
				t.Errorf("shellTimeoutMs(%d, runStep=%v) = (%d, %v), want (%d, %v)",
					tc.requested, tc.runStep, got, capped, tc.want, tc.wantCapped)
			}
		})
	}
	if MaxRunStepTimeout != 12*time.Hour {
		t.Errorf("MaxRunStepTimeout = %v, want 12h: a run step is bounded, never unbounded", MaxRunStepTimeout)
	}
}

func TestShellTimeoutMessage(t *testing.T) {
	ms := func(d time.Duration) int { return int(d.Milliseconds()) }
	for _, tc := range []struct {
		name      string
		runStep   bool
		timeout   int
		requested int
		capped    bool
		want      []string
	}{
		{"run step names its timeout", true, ms(30 * time.Minute), ms(30 * time.Minute), false,
			[]string{"step exceeded its 30-minute timeout"}},
		{"run step over the hard max names the cap", true, ms(MaxRunStepTimeout), ms(24 * time.Hour), true,
			[]string{"step exceeded its 12-hour timeout", "declared a 24-hour timeout", "12-hour cap"}},
		{"model call keeps its message", false, ms(time.Minute), ms(time.Minute), false,
			[]string{"Command was aborted before completion"}},
		{"model call over the cap names the cap", false, ms(MaxShellTimeout), ms(30 * time.Minute), true,
			[]string{"Command was aborted before completion", "10-minute foreground limit", "30-minute timeout it asked for", "run_in_background"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shellTimeoutMessage(tc.runStep, tc.timeout, tc.requested, tc.capped)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q does not contain %q", got, w)
				}
			}
			if strings.Contains(got, "asleep") {
				t.Errorf("message %q blames a machine sleep for a timeout", got)
			}
		})
	}
}

// timingOutDaemon records each request and answers it as a command its
// deadline stopped.
type timingOutDaemon struct {
	daemon.Client
	reqs []daemon.RunCommandRequest
}

func (d *timingOutDaemon) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	d.reqs = append(d.reqs, *req)
	return &daemon.CommandResult{ExitCode: daemon.TimeoutExitCode, TimedOut: true, Stderr: "partial"}, nil
}

func runShellCall(t *testing.T, runStep bool, input string) (*timingOutDaemon, ShellOutput) {
	t.Helper()
	d := &timingOutDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: runStep}
	resp, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: input})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.reqs) != 1 {
		t.Fatalf("daemon got %d requests, want 1; response %+v", len(d.reqs), resp)
	}
	var out ShellOutput
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		t.Fatalf("response is not shell output JSON: %v: %q", err, resp.Content)
	}
	return d, out
}

// A run step's command reaches the daemon with its node's timeout, and a
// timeout kill says so.
func TestShellTool_RunStepHonoursItsNodeTimeout(t *testing.T) {
	d, out := runShellCall(t, true, `{"command":"make release","timeout":1800000}`)
	if got := d.reqs[0].TimeoutMs; got != 1_800_000 {
		t.Errorf("TimeoutMs = %d, want 1800000: a run step's 30m timeout was clamped", got)
	}
	if !strings.Contains(out.Stderr, "step exceeded its 30-minute timeout") {
		t.Errorf("stderr = %q, want it to say the step exceeded its 30-minute timeout", out.Stderr)
	}
}

// A model cannot claim to be a run step: the flag is not a tool parameter, so
// a call carrying one is refused before anything runs, and a model's call
// stays under the 10-minute cap. Only the server-built context
// (rctx.ToolContext.RunStep) lifts it.
func TestShellTool_ToolInputCannotClaimRunStep(t *testing.T) {
	for _, input := range []string{
		`{"command":"make release","timeout":1800000,"run_step":true}`,
		`{"command":"make release","timeout":1800000,"RunStep":true}`,
		`{"command":"make release","timeout":1800000,"runStep":true}`,
	} {
		d := &timingOutDaemon{}
		tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
			Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d}
		resp, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: input})
		if err != nil {
			t.Fatalf("input %s: Run: %v", input, err)
		}
		if len(d.reqs) != 0 || !resp.IsError {
			t.Errorf("input %s: a call claiming a run-step flag ran (requests %+v, response %+v)", input, d.reqs, resp)
		}
	}

	d, out := runShellCall(t, false, `{"command":"make release","timeout":1800000}`)
	if got := d.reqs[0].TimeoutMs; got != int(MaxShellTimeout.Milliseconds()) {
		t.Errorf("TimeoutMs = %d, want the model's %v cap", got, MaxShellTimeout)
	}
	if strings.Contains(out.Stderr, "step exceeded") || !strings.Contains(out.Stderr, "10-minute foreground limit") {
		t.Errorf("a model's capped call reads %q, want the foreground-limit explanation", out.Stderr)
	}

	// And it is not offered: no parameter of the shell tool can carry it.
	typ := reflect.TypeOf(ShellParams{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := strings.ToLower(strings.Split(f.Tag.Get("json"), ",")[0] + f.Name)
		if strings.Contains(strings.ReplaceAll(name, "_", ""), "runstep") {
			t.Errorf("ShellParams.%s can carry a run-step flag from tool input", f.Name)
		}
	}
	schema, err := json.Marshal(NewShellTool(ShellPlatformUnix).ParamSchema())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(schema)), "run_step") {
		t.Errorf("the shell schema offers run_step to the model: %s", schema)
	}
}

// A run node's writable_checkouts reach the daemon; a model's call cannot name
// any, so a model run with credentials: none bound cannot unfreeze the shared
// checkouts for itself (credsandbox).
func TestShellTool_OnlyARunStepNamesWritableCheckouts(t *testing.T) {
	input := `{"command":"make gen","credentials":"none","writable_checkouts":["reliant"]}`
	d, _ := runShellCall(t, true, input)
	if got := d.reqs[0].WritableCheckouts; !reflect.DeepEqual(got, []string{"reliant"}) {
		t.Errorf("run step: WritableCheckouts = %v, want [reliant]", got)
	}

	md := &timingOutDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
		Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: md}
	if _, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: input}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, req := range md.reqs {
		if len(req.WritableCheckouts) != 0 {
			t.Errorf("a model's call reached the daemon with WritableCheckouts %v", req.WritableCheckouts)
		}
	}
}

// A run step starts in its node's work_dir (rctx.RunWorkDir, server-set); a
// model's call, or a run step with none, starts in the workspace.
func TestShellTool_ARunStepStartsInItsWorkDir(t *testing.T) {
	run := func(t *testing.T, runStep bool, workDir string) string {
		t.Helper()
		d := &timingOutDaemon{}
		tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
			Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d, RunStep: runStep, RunWorkDir: workDir}
		if _, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: `{"command":"ls"}`}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(d.reqs) != 1 {
			t.Fatalf("daemon got %d requests, want 1", len(d.reqs))
		}
		return d.reqs[0].WorkingDir
	}
	if got := run(t, true, "/var/cache/x/run-1"); got != "/var/cache/x/run-1" {
		t.Errorf("run step with a work_dir starts in %q, want /var/cache/x/run-1", got)
	}
	if got := run(t, true, ""); got != "/work/p" {
		t.Errorf("run step with no work_dir starts in %q, want the checkout", got)
	}
	if got := run(t, false, "/var/cache/x/run-1"); got != "/work/p" {
		t.Errorf("a model's call starts in %q, want the checkout: only a run step has a work_dir", got)
	}
}

// A workflow that binds writable_checkouts on an agent's shell (tool_params)
// gives the model's commands that directory, scoped to cache hand-offs: the
// server says the parameter was bound (rctx.ToolContext.BoundParams), and
// tools.ApplyBindings has put the bound value in the input. The same value
// from a model, with nothing bound, reaches the daemon as nothing.
func TestShellTool_ABoundWritableCheckoutsReachesTheDaemonScopedToHandOffs(t *testing.T) {
	const clone = "/var/cache/workspace/xdg/pr-fix/cp-12-run/control-plane"
	run := func(t *testing.T, bound bool) daemon.RunCommandRequest {
		t.Helper()
		bindings := Bindings{"credentials": LiteralBinding("none")}
		if bound {
			bindings["writable_checkouts"] = LiteralBinding([]any{clone})
		}
		model := `{"command":"git commit -qam fix","writable_checkouts":["` + clone + `"]}`
		if bound {
			model = `{"command":"git commit -qam fix"}`
		}
		input, err := ApplyBindings(ShellToolName, model, bindings)
		if err != nil {
			t.Fatalf("ApplyBindings: %v", err)
		}
		md := &timingOutDaemon{}
		tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1",
			Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: md}
		if bound {
			tc.BoundParams = bindings.Names()
		}
		if _, err := NewShellTool(ShellPlatformUnix).Run(tc, ToolCall{ID: "call-1", Name: ShellToolName, Input: input}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(md.reqs) == 0 {
			t.Fatal("no command reached the daemon")
		}
		return md.reqs[0]
	}

	req := run(t, true)
	if !reflect.DeepEqual(req.WritableCheckouts, []string{clone}) || req.WritableCheckoutsScope != daemon.WritableScopeBoundShell {
		t.Errorf("bound: WritableCheckouts = %v scope %q, want [%s] scope %q",
			req.WritableCheckouts, req.WritableCheckoutsScope, clone, daemon.WritableScopeBoundShell)
	}
	req = run(t, false)
	if len(req.WritableCheckouts) != 0 || req.WritableCheckoutsScope != "" {
		t.Errorf("a model's own value reached the daemon: %v scope %q", req.WritableCheckouts, req.WritableCheckoutsScope)
	}
}

// A model that sets a bound writable_checkouts to another directory is
// refused, not run with either value (checkBoundKeys).
func TestShellTool_AModelCannotOverrideABoundWritableCheckouts(t *testing.T) {
	bindings := Bindings{"writable_checkouts": LiteralBinding([]any{"/var/cache/x/clone"})}
	_, err := ApplyBindings(ShellToolName, `{"command":"true","writable_checkouts":["/home/workspace/projects/p"]}`, bindings)
	var set *BoundParamSetError
	if !errors.As(err, &set) {
		t.Fatalf("err = %v, want a BoundParamSetError", err)
	}
}

// writable_checkouts may be bound: a workflow gives an agent's shell its
// scratch clone that way.
func TestShellTool_WritableCheckoutsIsBindable(t *testing.T) {
	if _, unbindable := UnbindableParams(ShellToolName)["writable_checkouts"]; unbindable {
		t.Error("writable_checkouts is on the shell tool's unbindable list")
	}
	if _, err := BindTool(NewShellTool(ShellPlatformUnix), Bindings{"writable_checkouts": LiteralBinding([]any{"/x"})}); err != nil {
		t.Errorf("binding writable_checkouts: %v", err)
	}
}
