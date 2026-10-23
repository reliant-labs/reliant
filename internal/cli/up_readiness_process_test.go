package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHostReadinessProcessHelper(t *testing.T) {
	switch os.Getenv("FORGE_READINESS_HELPER") {
	case "exit":
		fmt.Fprintln(os.Stderr, "compiler failed: test diagnostic")
		os.Exit(7)
	case "slow":
		// Longer than the old 15-second gate; no actual compiler or cache
		// makes this test dependent on machine speed.
		time.Sleep(16 * time.Second)
		ln, err := net.Listen("tcp", os.Getenv("FORGE_READINESS_ADDR"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(8)
		}
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}
}

func TestHostReadinessObservesRealRunnerExit(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint("background=", background), func(t *testing.T) {
			dir, projectID, env := testStack(t)
			t.Chdir(dir)
			reg := newProcRegistry(projectID, dir, env)
			cmd := exec.Command(os.Args[0], "-test.run=^TestHostReadinessProcessHelper$")
			cmd.Env = append(os.Environ(), "FORGE_READINESS_HELPER=exit")
			if err := reg.start("api", cmd, background); err != nil {
				t.Fatal(err)
			}
			mp := reg.processes[0]
			select {
			case <-mp.done:
			case <-time.After(5 * time.Second):
				killProcessTree(mp.pid, syscall.SIGKILL)
				t.Fatal("runner exit was not observed")
			}
			if got := reg.exitReason("api"); got != "exit status 7" {
				t.Fatalf("exit status = %q", got)
			}
			path, err := upLogPath(env, "api")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "compiler failed: test diagnostic") {
				t.Fatalf("exit detection lost the diagnostic log: %q, %v", data, err)
			}
		})
	}
}

func TestHostReadinessAllowsRealStartupBeyondFifteenSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("real delayed-start regression takes 16 seconds")
	}
	requireProcInspection(t)
	dir, projectID, env := testStack(t)
	t.Chdir(dir)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addr := ln.Addr().String()
	_ = ln.Close()
	reg := newProcRegistry(projectID, dir, env)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostReadinessProcessHelper$")
	cmd.Env = append(os.Environ(), "FORGE_READINESS_HELPER=slow", "FORGE_READINESS_ADDR="+addr)
	if err := reg.start("api", cmd, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.shutdown)
	e := &KCLEntities{Workloads: []WorkloadEntity{hostWL("api", withPorts(int32(port)))}}
	if err := waitHostServicesReady(t.Context(), e, projectID, env, nil, hostReadyTimeout, hostReadyPoll, reg); err != nil {
		t.Fatalf("live runner with a cold startup was rejected: %v", err)
	}
}
