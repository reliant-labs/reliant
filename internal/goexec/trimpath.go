package goexec

import (
	"os/exec"
	"strings"
	"sync"
)

// CheckEnv is Env for a go command that only CHECKS code — compiles it and
// throws the output away (`go list -export`, a compile check, `go vet`) or
// loads it for an analyzer — with -trimpath added to GOFLAGS (WithTrimpath).
func CheckEnv(extra ...string) []string {
	return WithTrimpath(Env(extra...))
}

// WithTrimpath returns env with -trimpath appended to its effective GOFLAGS,
// unless that already says -trimpath (or -trimpath=false, which is an
// opt-out and is respected). The effective GOFLAGS is env's own GOFLAGS when
// it has one, else the value `go env -w` or the toolchain's go.env sets —
// cmd/go reads exactly one of the layers, so the merge keeps whichever the
// command would have used. env itself is not modified.
//
// # Why
//
// Without -trimpath, cmd/go hashes every main-module package's ABSOLUTE
// directory into its build-cache key. A scratch copy (verify-generated
// regenerates in a fresh `.<repo>.forge-verify-*` every run) or another
// worktree of the same commit therefore compiles every package again while
// identical cache entries sit unused, and the test result cache misses with
// it. On a shared machine that compile happens while holding a heavy slot,
// so it is paid again in every other agent's wait. Measured on a 16-CPU Mac,
// one commit, two worktrees: the second compiled 267 packages and cached 0 of
// 73 test packages; with -trimpath, 0 compiles and 58 of 73 cached.
//
// # Not for everything
//
// A binary someone runs or debugs keeps its paths: delve needs absolute ones
// (forge debug passes -trimpath=false), and the dev loop's stack traces stay
// clickable. So this is opt-in per call site, for checks only.
func WithTrimpath(env []string) []string {
	return withTrimpath(env, goEnvFileFlags)
}

func withTrimpath(env []string, fileFlags func() string) []string {
	flags := ""
	set := false
	for _, kv := range env {
		if key, val, ok := strings.Cut(kv, "="); ok && isGOFLAGSKey(key) {
			flags, set = val, true // the last assignment wins, as it does for exec
		}
	}
	// cmd/go treats an empty GOFLAGS as unset and reads the file layer.
	if !set || strings.TrimSpace(flags) == "" {
		flags = fileFlags()
	}
	if mentionsTrimpath(flags) {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && isGOFLAGSKey(key) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOFLAGS="+strings.TrimSpace(flags+" -trimpath"))
}

// mentionsTrimpath reports whether a GOFLAGS value sets -trimpath either way.
func mentionsTrimpath(goflags string) bool {
	for _, f := range strings.Fields(goflags) {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if name == "trimpath" {
			return true
		}
	}
	return false
}

var (
	fileFlagsOnce sync.Once
	fileFlagsVal  string
)

// goEnvFileFlags is the GOFLAGS cmd/go reads when the environment sets none:
// `go env -w`, else $GOROOT/go.env (where the Reliant workspace image puts
// -trimpath). Asked of go itself, in the current directory, so a go.mod
// toolchain switch reports the switched toolchain's go.env. Once per process.
func goEnvFileFlags() string {
	fileFlagsOnce.Do(func() {
		cmd := exec.Command("go", "env", "GOFLAGS")
		cmd.Env = append(Env(), "GOFLAGS=")
		if out, err := cmd.Output(); err == nil {
			fileFlagsVal = strings.TrimSpace(string(out))
		}
	})
	return fileFlagsVal
}
