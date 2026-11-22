// Package helper is a shared test-helper package: its slowness and its marks
// reach the tests of other packages through facts.
package helper

import (
	"os/exec"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// BuildForge execs the toolchain and checks nothing.
func BuildForge() string { // want BuildForge:"slow: execs `go` at helper.go:14"
	out, _ := exec.Command("go", "build", "./cmd/forge").Output()
	return string(out)
}

// Render calls BuildForge, so it is slow one hop further away.
func Render() string { // want Render:"slow: calls BuildForge, which execs `go` at helper.go:14"
	return BuildForge()
}

// GuardedRender marks the test it is handed before doing the slow work.
func GuardedRender(t testing.TB) string { // want GuardedRender:"marks slow"
	testkit.Slow(t, "renders prod through forge", testkit.Inputs("deploy/**", "forge.yaml"))
	return Render()
}

// RawGuard is the hand-rolled marker, in a helper.
func RawGuard(t testing.TB) { // want RawGuard:"marks slow"
	if testing.Short() {
		t.Skip("slow")
	}
}

// GitIn runs git in dir: slow work only when dir is not a temp dir.
func GitIn(dir string, args ...string) { // want GitIn:"slow unless arg 0 is a temp dir: execs `git` at helper.go:38"
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	_ = cmd.Run()
}

// fast does nothing slow, so it gets no fact (and is unexported anyway).
func fast() int { return 1 }

// Fast does nothing slow: no fact.
func Fast() int { return fast() }
