//go:build !windows

package hostinfra

// A fake-binary harness. The supervised engines run a pinned third-party
// executable by path; these tests point that path at a symlink to the test
// binary itself, which TestMain turns into a stand-in server when
// HOSTINFRA_FAKE is set. The supervisor therefore launches, verifies,
// adopts, signals and stops a REAL process, with nothing downloaded.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	fakeEnv          = "HOSTINFRA_FAKE"
	fakeVersionEnv   = "HOSTINFRA_FAKE_VERSION"
	fakeValidateEnv  = "HOSTINFRA_FAKE_VALIDATE"
	fakeExitEnv      = "HOSTINFRA_FAKE_EXIT"
	fakeEngineName   = "fakeeng"
	fakeEngineVerStr = "fakeeng 1.0.0"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(runFake(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

var fakeHealthPort = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

// runFake is the stand-in server: it answers a version probe, a config
// validation, and otherwise serves (or sleeps, or exits) according to mode.
func runFake(mode string, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-v", "version":
			fmt.Println(os.Getenv(fakeVersionEnv))
			return 0
		case "operator":
			return 0
		case "validate":
			if os.Getenv(fakeValidateEnv) == "fail" {
				fmt.Fprintln(os.Stderr, "error: no receivers configured")
				return 1
			}
			return 0
		}
	}
	if os.Getenv(fakeExitEnv) != "" {
		time.Sleep(300 * time.Millisecond)
		return 3
	}
	switch mode {
	case "otelcol":
		for _, a := range args {
			cfg, ok := strings.CutPrefix(a, "--config=")
			if !ok {
				continue
			}
			b, err := os.ReadFile(cfg)
			if err != nil {
				return 4
			}
			m := fakeHealthPort.FindSubmatch(b)
			if m == nil {
				return 5
			}
			return serveHTTP("127.0.0.1:" + string(m[1]))
		}
		return 6
	case "zitadel":
		if pat := os.Getenv("ZITADEL_FIRSTINSTANCE_PATPATH"); pat != "" {
			if err := os.WriteFile(pat, []byte("fake-pat"), 0o600); err != nil {
				return 7
			}
		}
		return serveHTTP("127.0.0.1:" + os.Getenv("ZITADEL_PORT"))
	case "serve":
		// A NATS-shaped server: it greets every connection with an INFO line
		// that announces JetStream, which is what natsReady reads. The port
		// is `--port N` (nats, temporal) or the second argument.
		port := ""
		for i, a := range args {
			if a == "--port" && i+1 < len(args) {
				port = args[i+1]
			}
		}
		if port == "" && len(args) >= 2 && args[0] == "serve" {
			port = args[1]
		}
		if port == "" {
			return 8
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			return 9
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				return 10
			}
			_, _ = c.Write([]byte("INFO {\"jetstream\":true}\r\n"))
			_ = c.Close()
		}
	}
	select {} // "sleep": run until signalled
}

func serveHTTP(addr string) int {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec // a loopback test stand-in
		return 11
	}
	return 0
}

// installFake makes dest a symlink to this test binary and selects the
// stand-in behaviour for every process the test launches.
func installFake(t *testing.T, dest, mode, version string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, dest); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnv, mode)
	t.Setenv(fakeVersionEnv, version)
}

// isolateCache points the user cache directory (where every pinned binary is
// kept) at a scratch directory.
func isolateCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
}

// reapPID waits for pid as its parent, so a stopped child does not linger as
// a zombie the supervisor's signal-0 probe still sees as alive. (In forge
// the parent is the short-lived `forge env up`; under a test it is us.)
func reapPID(pid int) {
	go func() {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	}()
}

// reapPIDFile reaps, as their parent, every child whose pid the pidfile comes
// to name for the rest of the test, so a stopped or crashed child is seen as
// gone rather than as a zombie.
func reapPIDFile(t *testing.T, pidFile string) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		seen := map[int]bool{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			if pid, _, ok := readPIDFile(pidFile); ok && !seen[pid] {
				seen[pid] = true
				reapPID(pid)
			}
		}
	}()
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}

// deadPID is the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}
