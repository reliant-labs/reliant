// Copyright (c) 2026 Reliant Labs

//go:build linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"

	"github.com/reliant-labs/reliant/internal/hotswap"
)

// A tool call's command survives a hot swap of the daemon under it: the old
// image runs RunCommand for a request id, freezes it and execs in place; the
// new image adopts it and runs RunCommand again for the same request id,
// which finishes the SAME command instead of starting another, with every
// byte of its output and its own exit status.

const swapRunHelperEnv = "DAEMON_SWAP_RUN_HELPER"

// The command prints 30 numbered lines on each stream, 30ms apart, then
// exits 4. A second start would be visible: it writes its pid on line 0.
const swapRunScript = `echo "pid $$"; i=0; while [ $i -lt 30 ]; do i=$((i+1)); echo "line $i"; echo "err $i" >&2; sleep 0.03; done; exit 4`

func swapRunRequest(dir string) *RunCommandRequest {
	return &RunCommandRequest{Command: swapRunScript, WorkingDir: dir, TimeoutMs: 60_000, ChatID: "chat-swap"}
}

func numbered(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}
	return b.String()
}

func TestRunCommandSurvivesAHotSwap(t *testing.T) {
	testkit.Slow(t, "re-execs the test binary twice; each image probes the credentials sandbox in TestMain")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunCommandSwapHelper$", "-test.v=false")
	cmd.Env = append(os.Environ(), swapRunHelperEnv+"=phase1", "SWAP_RUN_DIR="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\nstderr:\n%s\nstdout:\n%s", err, stderr.String(), out)
	}
	var res struct {
		Result   CommandResult
		StartPID string
		Err      string
	}
	if err := json.Unmarshal(lastLine(out), &res); err != nil {
		t.Fatalf("helper output %q: %v\nstderr:\n%s", out, err, stderr.String())
	}
	if res.Err != "" {
		t.Fatalf("new image: %s", res.Err)
	}
	r := res.Result
	wantOut := "pid " + res.StartPID + "\n" + numbered("line", 30)
	if r.Stdout != wantOut {
		t.Errorf("stdout across the swap:\n%q\nwant\n%q", r.Stdout, wantOut)
	}
	if !strings.Contains(r.Stderr, numbered("err", 30)) {
		t.Errorf("stderr across the swap: %q", r.Stderr)
	}
	if r.ExitCode != 4 {
		t.Errorf("exit code %d, want the command's own 4", r.ExitCode)
	}
	if r.DurationMs < 800 {
		t.Errorf("duration %dms: it must count from the first start, not the resume", r.DurationMs)
	}
}

// TestRunCommandSwapHelper is both images of the test above.
func TestRunCommandSwapHelper(t *testing.T) {
	dir := os.Getenv("SWAP_RUN_DIR")
	ctx := WithToolRequestID(context.Background(), "req-swap-1")
	report := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Println(string(data))
		os.Exit(0)
	}
	switch os.Getenv(swapRunHelperEnv) {
	case "phase1":
		s := hotswap.Enable()
		c := NewLocalClient()
		go func() {
			_, _ = c.RunCommand(ctx, swapRunRequest(dir))
			fmt.Fprintln(os.Stderr, "phase1: the command finished before the swap")
			os.Exit(3)
		}()
		// Some output read by this image, the rest left for the next.
		deadline := time.Now().Add(10 * time.Second)
		for SwappableRuns() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond)
		procs, _, err := FreezeRuns()
		if err != nil || len(procs) != 1 {
			fmt.Fprintln(os.Stderr, "phase1: freeze:", err, len(procs))
			os.Exit(2)
		}
		var meta runHandover
		_ = json.Unmarshal(procs[0].Meta, &meta)
		startPID := strings.TrimPrefix(strings.SplitN(string(meta.Stdout), "\n", 2)[0], "pid ")
		// Replaced, not appended: Go reads the FIRST of two entries.
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, swapRunHelperEnv+"=") {
				env = append(env, kv)
			}
		}
		env = append(env, swapRunHelperEnv+"=phase2", "SWAP_RUN_START_PID="+startPID)
		err = s.Exec(os.Args[0], os.Args, env, procs, nil)
		fmt.Fprintln(os.Stderr, "phase1: exec:", err)
		os.Exit(2)
	case "phase2":
		procs, _, ok, err := hotswap.Restore()
		if !ok || err != nil {
			report(map[string]string{"Err": fmt.Sprintf("restore: ok=%v err=%v", ok, err)})
		}
		hotswap.Enable()
		if ids := AdoptRuns(procs); len(ids) != 1 || ids[0] != "req-swap-1" {
			report(map[string]string{"Err": fmt.Sprintf("adopted %v", ids)})
		}
		res, err := NewLocalClient().RunCommand(ctx, swapRunRequest(dir))
		if err != nil {
			report(map[string]string{"Err": err.Error()})
		}
		report(map[string]any{"Result": res, "StartPID": os.Getenv("SWAP_RUN_START_PID")})
	default:
		t.Skip("helper for TestRunCommandSurvivesAHotSwap")
	}
}

// A swap that does not happen (the exec failed, or the drain gave up) thaws
// the command: it finishes in this image with all of its output.
func TestRunCommandThawsAfterAnAbandonedSwap(t *testing.T) {
	hotswap.Enable()
	c := NewLocalClient()
	ctx := WithToolRequestID(context.Background(), "req-thaw-1")
	type result struct {
		res *CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := c.RunCommand(ctx, swapRunRequest(t.TempDir()))
		done <- result{res, err}
	}()
	for SwappableRuns() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	procs, thaw, err := FreezeRuns()
	if err != nil || len(procs) != 1 {
		t.Fatalf("freeze: %v (%d)", err, len(procs))
	}
	if SwappableRuns() != 0 {
		t.Fatal("a frozen command still counts as swappable")
	}
	time.Sleep(200 * time.Millisecond) // output piles up in the pipe meanwhile
	thaw()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.HasSuffix(r.res.Stdout, numbered("line", 30)) || r.res.ExitCode != 4 {
		t.Fatalf("after a thaw: exit %d, stdout %q", r.res.ExitCode, r.res.Stdout)
	}
}

func lastLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}
