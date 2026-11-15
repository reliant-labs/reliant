//go:build linux

package pgtest

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests put a second pool user in a pid namespace of its own, sharing
// this process's /tmp: what reliant's credential sandbox does to the
// credential-free gates (pr-fix, pr-regen) that run beside host-namespace
// gates (pr-review's verify-generated). The pool used to name its users by
// pid; across the namespace boundary neither side could see the other's, so
// each believed itself the last user and tore the server down under the other
// (forge bug df26c327; control-plane rc.18/rc.19, forge #1906).

// poolUserHelperEnv selects TestPoolUserHelper's mode in a re-executed test
// binary: "cycle" (acquire, print the URL, release) or "hold" (acquire, print
// the URL, release once stdin closes).
const poolUserHelperEnv = "FORGE_PGTEST_POOL_USER_HELPER"

// TestPoolUserHelper is not a test: it is the second pool user, run in
// another pid namespace by the tests below.
func TestPoolUserHelper(t *testing.T) {
	mode := os.Getenv(poolUserHelperEnv)
	if mode == "" {
		t.Skip("helper process for the pid-namespace pool tests")
	}
	url, release, err := AcquireShared()
	if err != nil {
		t.Fatalf("acquire the shared server: %v", err)
	}
	if !CanReach(url) {
		t.Fatalf("the shared server at %s does not answer", url)
	}
	fmt.Printf("POOLURL %s\n", url)
	if mode == "hold" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	release()
}

// otherPidNamespace returns a command that runs this test binary's
// TestPoolUserHelper in a new user + pid namespace, as reliant's credential
// sandbox runs a gate, or skips naming why it cannot.
func otherPidNamespace(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("needs util-linux unshare(1) to run a pool user in another pid namespace")
	}
	args := []string{"--user", "--map-current-user", "--pid", "--fork"}
	if out, err := exec.Command(unshare, append(args, "true")...).CombinedOutput(); err != nil {
		t.Skipf("this machine does not allow an unprivileged user + pid namespace (unshare: %v: %s)", err, strings.TrimSpace(string(out)))
	}
	cmd := exec.Command(unshare, append(args, os.Args[0], "-test.run=^TestPoolUserHelper$", "-test.count=1")...)
	cmd.Env = append(os.Environ(), poolUserHelperEnv+"="+mode)
	cmd.Stderr = os.Stderr
	return cmd
}

// urlPort is the port of a maintenance DSN.
func urlPort(raw string) (uint32, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, err
	}
	p, err := strconv.ParseUint(u.Port(), 10, 16)
	return uint32(p), err
}

// readPoolURL reads the helper's POOLURL line.
func readPoolURL(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if url, ok := strings.CutPrefix(sc.Text(), "POOLURL "); ok {
			return url
		}
	}
	t.Fatalf("the helper in the other pid namespace printed no POOLURL (scan: %v)", sc.Err())
	return ""
}

// A user in another pid namespace that comes and goes must not stop the
// server this process is using. Before leases it pruned this process's pid
// (invisible to it), found itself the last user, failed to signal the
// postmaster and deleted its data directory anyway.
func TestPool_AUserInAnotherPidNamespaceLeavesThisOnesServerAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres in two pid namespaces; runs in task test")
	}
	privatePool(t)
	url, port, mode, err := attachEmbedded()
	if err != nil {
		t.Fatalf("boot the shared server: %v", err)
	}
	if mode != poolModePooled {
		t.Fatalf("the first attach is not pooled (mode %d)", mode)
	}
	t.Cleanup(func() { detachEmbedded(port) })
	pid, alive := postmaster(filepath.Join(runtimeDir(port), "data", "postmaster.pid"))
	if !alive {
		t.Fatal("the shared server has no live postmaster")
	}

	other := otherPidNamespace(t, "cycle")
	out, err := other.Output()
	if err != nil {
		t.Fatalf("the pool user in another pid namespace failed: %v\n%s", err, out)
	}
	t.Logf("the other namespace's user ran on %s", readPoolURL(t, strings.NewReader(string(out))))

	if err := dataIntact(port); err != nil {
		t.Fatalf("a user in another pid namespace tore down this process's server: %v", err)
	}
	if !processAlive(pid) {
		t.Fatalf("a user in another pid namespace stopped this process's postmaster %d", pid)
	}
	if !CanReach(url) {
		t.Fatal("this process's server no longer answers after a user in another pid namespace came and went")
	}
}

// A server whose postmaster runs in another pid namespace is not shared
// outside it: when that namespace ends, the kernel SIGKILLs every process in
// it, the postmaster included, under any outside user — and nothing outside
// it can stop that postmaster cleanly either.
func TestPool_AServerInAnotherPidNamespaceIsNotShared(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres in two pid namespaces; runs in task test")
	}
	privatePool(t)
	other := otherPidNamespace(t, "hold")
	stdin, err := other.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := other.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = other.Process.Kill(); _ = other.Wait() })
	otherURL := readPoolURL(t, stdout)
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	url, port, _, err := attachEmbedded()
	if err != nil {
		t.Fatalf("attach beside the other namespace's server: %v", err)
	}
	t.Cleanup(func() { detachEmbedded(port) })
	if url == otherURL {
		t.Fatalf("this process reused %s, a server whose postmaster runs in another pid namespace", url)
	}
	// Nor did it take that server for a dead one and reclaim it: from here
	// its postmaster's pid names nothing, but it is alive and in use.
	otherPort, err := urlPort(otherURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataIntact(otherPort); err != nil {
		t.Fatalf("attaching here destroyed the server another pid namespace is using: %v", err)
	}
	if !CanReach(otherURL) {
		t.Fatal("the server another pid namespace is using stopped answering when this process attached")
	}

	// The other namespace ends; its server goes with it.
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- other.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the pool user in another pid namespace failed: %v", err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the pool user in another pid namespace did not finish")
	}
	if !CanReach(url) {
		t.Fatal("this process's server stopped answering when another pid namespace ended")
	}
	if err := dataIntact(port); err != nil {
		t.Fatalf("this process's server lost its data directory when another pid namespace ended: %v", err)
	}
}
