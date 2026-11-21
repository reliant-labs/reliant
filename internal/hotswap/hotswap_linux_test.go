// Copyright (c) 2026 Reliant Labs

//go:build linux

package hotswap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The proof the owner asked for: start a long command, swap the image under
// it, and the command keeps running, its output is all captured (part read
// before the swap, the rest after) and its exit status is collected by the
// new image.
//
// The test binary is both images. TestHotSwapKeepsRunningCommands runs it as
// a child in phase 1 (helperEnv=phase1), which starts the commands, reads a
// little of one, and execs itself in place as phase 2 through Spawner.Exec.
// Phase 2 restores the handoff, drains each command's output, waits for it,
// and prints a report the parent test judges.

const helperEnv = "HOTSWAP_TEST_HELPER"

type report struct {
	Restored     bool               `json:"restored"`
	Err          string             `json:"err,omitempty"`
	SamePID      bool               `json:"same_pid"`
	BeforeSwap   string             `json:"before_swap"`
	LivenessOpen bool               `json:"liveness_open"`
	Commands     map[string]outcome `json:"commands"`
}

type outcome struct {
	Output   string `json:"output"`
	Exited   bool   `json:"exited"`
	Code     int    `json:"code"`
	Signaled bool   `json:"signaled"`
	Signal   string `json:"signal,omitempty"`
}

func TestHotSwapKeepsRunningCommands(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHotSwapHelper$", "-test.v=false")
	cmd.Env = append(os.Environ(), helperEnv+"=phase1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\nstderr:\n%s\nstdout:\n%s", err, stderr.String(), out)
	}
	var r report
	line := lastJSONLine(out)
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("helper report %q: %v\nstderr:\n%s", out, err, stderr.String())
	}
	if !r.Restored || r.Err != "" {
		t.Fatalf("the new image did not take over: restored=%v err=%s", r.Restored, r.Err)
	}
	if !r.SamePID {
		t.Fatal("the new image runs under another pid: it would not be the commands' parent")
	}

	// The long command: lines 1-3 read by the old image, 4-20 by the new,
	// nothing lost or repeated, and its own exit status.
	run := r.Commands["run"]
	if r.BeforeSwap != lines(1, 3) {
		t.Errorf("read before the swap = %q, want lines 1-3", r.BeforeSwap)
	}
	if run.Output != lines(4, 20) {
		t.Errorf("read after the swap = %q, want lines 4-20", run.Output)
	}
	if !run.Exited || run.Code != 3 {
		t.Errorf("run: exited=%v code=%d signaled=%v (%s), want exit 3", run.Exited, run.Code, run.Signaled, run.Signal)
	}

	// A sandbox-style command (parent-death signal, liveness pipe) started on
	// the spawner's thread survives the exec, and the liveness pipe is still
	// held open for it.
	sb := r.Commands["sandboxed"]
	if !sb.Exited || sb.Code != 5 || sb.Output != "alive\n" {
		t.Errorf("sandboxed: %+v, want it to finish after the swap with exit 5", sb)
	}
	if !r.LivenessOpen {
		t.Error("the liveness pipe did not cross the exec: every sandbox init would see its daemon gone")
	}

	// Why the Spawner exists: the same command started on another thread is
	// killed by the exec, because execve ends that thread.
	other := r.Commands["other-thread"]
	if !other.Signaled || other.Signal != syscall.SIGKILL.String() {
		t.Errorf("other-thread: %+v, want it killed by its parent-death signal", other)
	}
}

// TestHotSwapHelper is the two images of the test above; it does nothing in
// a normal run.
func TestHotSwapHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "phase1":
		phase1()
	case "phase2":
		phase2()
	default:
		t.Skip("helper for TestHotSwapKeepsRunningCommands")
	}
}

const pidEnv = "HOTSWAP_TEST_PID"
const beforeEnv = "HOTSWAP_TEST_BEFORE"

func phase1() {
	s := NewSpawner()
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "phase1:", err)
		os.Exit(2)
	}
	start := func(cmd *exec.Cmd, onSpawner bool) *os.File {
		r, w, err := os.Pipe()
		if err != nil {
			fail(err)
		}
		cmd.Stdout = w
		if onSpawner {
			err = s.Start(cmd)
		} else {
			done := make(chan error)
			// A thread of its own that stays alive until the exec ends it,
			// as an unlocked goroutine's thread would.
			go func() {
				runtime.LockOSThread()
				done <- cmd.Start()
				select {}
			}()
			err = <-done
		}
		if err != nil {
			fail(err)
		}
		_ = w.Close()
		return r
	}

	runCmd := exec.Command("sh", "-c", `i=0; while [ $i -lt 20 ]; do i=$((i+1)); echo "line $i"; sleep 0.04; done; exit 3`)
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	runOut := start(runCmd, true)

	// credsandbox's init: SIGKILL when its parent thread exits, and it holds
	// the read end of the daemon's liveness pipe.
	liveR, liveW, err := os.Pipe()
	if err != nil {
		fail(err)
	}
	sbCmd := exec.Command("sh", "-c", `sleep 0.6; echo alive; exit 5`)
	sbCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	sbCmd.ExtraFiles = []*os.File{liveR}
	sbOut := start(sbCmd, true)
	_ = liveR.Close()

	otherCmd := exec.Command("sh", "-c", `sleep 0.6; echo survived; exit 0`)
	otherCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	otherOut := start(otherCmd, false)

	// Read exactly three lines, byte by byte: anything buffered here and not
	// handed over would be lost.
	var before strings.Builder
	buf := make([]byte, 1)
	for n := 0; n < 3; {
		if _, err := runOut.Read(buf); err != nil {
			fail(err)
		}
		before.WriteByte(buf[0])
		if buf[0] == '\n' {
			n++
		}
	}

	env := append(withoutEnv(os.Environ(), helperEnv),
		helperEnv+"=phase2", fmt.Sprintf("%s=%d", pidEnv, os.Getpid()), beforeEnv+"="+before.String())
	procs := []Process{
		{Kind: "run", ID: "run", PID: runCmd.Process.Pid, Files: map[string]*os.File{"stdout": runOut}},
		{Kind: "run", ID: "sandboxed", PID: sbCmd.Process.Pid, Files: map[string]*os.File{"stdout": sbOut}},
		{Kind: "run", ID: "other-thread", PID: otherCmd.Process.Pid, Files: map[string]*os.File{"stdout": otherOut}},
	}
	err = s.Exec(os.Args[0], os.Args, env, procs, map[string]*os.File{"liveness": liveW})
	fail(err)
}

func phase2() {
	r := report{Commands: map[string]outcome{}, BeforeSwap: os.Getenv(beforeEnv)}
	r.SamePID = os.Getenv(pidEnv) == fmt.Sprint(os.Getpid())
	procs, extra, ok, err := Restore()
	r.Restored = ok
	if err != nil {
		r.Err = err.Error()
	}
	if live := extra["liveness"]; live != nil {
		// The read end lives in the sandboxed command only while it runs,
		// so probe it before waiting for that command.
		_, werr := live.Write([]byte{0})
		r.LivenessOpen = werr == nil
	}
	for _, p := range procs {
		out, _ := io.ReadAll(bufio.NewReader(p.Files["stdout"]))
		st, werr := Wait(p.PID)
		o := outcome{Output: string(out)}
		if werr != nil {
			o.Signal = werr.Error()
		} else if ws, ok := st.Sys().(syscall.WaitStatus); ok {
			o.Exited, o.Code = ws.Exited(), ws.ExitStatus()
			if ws.Signaled() {
				o.Signaled, o.Signal = true, ws.Signal().String()
			}
		}
		r.Commands[p.ID] = o
	}
	data, _ := json.Marshal(r)
	fmt.Println(string(data))
	os.Exit(0)
}

func lines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func lastJSONLine(out []byte) []byte {
	all := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(all) - 1; i >= 0; i-- {
		if bytes.HasPrefix(all[i], []byte("{")) {
			return all[i]
		}
	}
	return out
}
