package goexec

import (
	"os"
	"runtime"
	"strings"
)

// Env is the environment for a `go` subprocess forge runs on its own
// behalf: the current process environment with -mod=mod removed from
// GOFLAGS, then extra (KEY=VALUE entries, appended verbatim).
//
// # Why -mod=mod, and only -mod=mod
//
// -mod=mod is the one -mod value that lets `go build`, `go list` and
// everything built on them (go/packages, goimports) rewrite go.mod and
// go.sum as a side effect. The default, readonly, is what CI runs with. A
// caller that exports GOFLAGS=-mod=mod for its OWN go commands — control-
// plane's scripts/pin-sibling.sh did, for its `go get`s — therefore made
// `forge generate` record ~350 go.sum lines a CI regenerate never
// produces, and the tree went dirty for a reason that had nothing to do
// with the code. A generator's output must be a function of the repository,
// not of whoever happened to invoke it.
//
// Everything else in GOFLAGS is kept: -tags, -trimpath, -buildvcs and
// -mod=vendor say how to build, not whether the build may edit the module
// files, and a user who sets them means them for forge's builds too.
//
// # What is not scrubbed
//
//   - extra. It is what forge itself — or a project's declared build
//     config, such as a GoBuild env block in KCL — asks for explicitly, so
//     it is appended after the scrub and wins over the inherited value.
//   - A GOFLAGS persisted with `go env -w`. That is machine-wide toolchain
//     configuration the user's own `go build` obeys too, not a value a
//     calling script leaked into forge, and an empty GOFLAGS cannot
//     override it anyway (cmd/go treats an empty variable as unset).
func Env(extra ...string) []string {
	return envFrom(os.Environ(), extra...)
}

// envFrom is Env over an explicit base environment, so the scrub is
// testable without mutating the test process's own environment.
func envFrom(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !isGOFLAGSKey(key) {
			out = append(out, kv)
			continue
		}
		// An emptied GOFLAGS is dropped rather than written as GOFLAGS=,
		// which reads identically to cmd/go and only adds noise.
		if kept := withoutModMod(val); kept != "" {
			out = append(out, key+"="+kept)
		}
	}
	return append(out, extra...)
}

// withoutModMod drops every -mod=mod entry from a GOFLAGS value. cmd/go
// splits GOFLAGS on whitespace and accepts one or two leading dashes, so
// both spellings are matched.
func withoutModMod(goflags string) string {
	fields := strings.Fields(goflags)
	kept := fields[:0]
	for _, f := range fields {
		if f == "-mod=mod" || f == "--mod=mod" {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// isGOFLAGSKey matches the way the OS resolves the variable cmd/go reads:
// case-insensitively on Windows, exactly everywhere else.
func isGOFLAGSKey(key string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(key, "GOFLAGS")
	}
	return key == "GOFLAGS"
}
