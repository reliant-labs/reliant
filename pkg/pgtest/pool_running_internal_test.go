//go:build !windows

package pgtest

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// silentPort is a loopback port nothing listens on: one the kernel just
// handed out and this test closed again.
func silentPort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

// answeringServerDir lays out a runtime dir whose postmaster this process
// cannot see — its pid file names a pid that is dead here, as a pid of
// another pid namespace is — while the server still answers on its port.
func answeringServerDir(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	fakeServerDir(t, port)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(runtimeDir(port), "data", "postmaster.pid")
	content := strconv.Itoa(dead.Process.Pid) + "\n" + filepath.Join(runtimeDir(port), "data") + "\n0\n" + strconv.Itoa(int(port)) + "\n"
	if err := os.WriteFile(pidFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return port
}

// stopSharedByPort never removes the data directory of a server that still
// answers. Removing it under a live postmaster leaves a server that answers
// pings and fails every CREATE DATABASE — what a sandboxed gate did to the
// host's shared server when it could not see the postmaster's pid.
func TestStopSharedByPort_NeverRemovesTheDirOfAServerThatAnswers(t *testing.T) {
	privatePool(t)
	port := answeringServerDir(t)

	stopSharedByPort(port)

	if err := dataIntact(port); err != nil {
		t.Fatalf("stopSharedByPort removed the data directory of a server that still answers on its port: %v", err)
	}
}

// Nor does the age-based reaper, whatever the dir's age.
func TestReap_NeverRemovesTheDirOfAServerThatAnswers(t *testing.T) {
	privatePool(t)
	port := answeringServerDir(t)
	prev := staleInstanceAge
	staleInstanceAge = time.Nanosecond
	t.Cleanup(func() { staleInstanceAge = prev })
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(runtimeDir(port), old, old)
	_ = os.Chtimes(filepath.Join(runtimeDir(port), "data", "postmaster.pid"), old, old)

	reapStaleInstances()

	if err := dataIntact(port); err != nil {
		t.Fatalf("reapStaleInstances removed the data directory of a server that still answers on its port: %v", err)
	}
}
