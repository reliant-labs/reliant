// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
)

// Backend runs one gate inside one kind of boundary. It is declared here, at
// its consumer, with the shape of the credsandbox gate backend (E9a): the
// sysbox implementation is adapted to it once E9a is back on main, and
// package runcgate implements the not-isolated runc one.
//
// Per gate the Runner calls Run, CopyOut for the output directory, then
// Teardown, which it always calls so a failed Run leaves nothing behind.
type Backend interface {
	// Isolation names the backend in queue_batches.isolation_backend terms.
	Isolation() gaterun.Isolation
	// Detect reports whether the machine has the backend's runtime. Cheap;
	// changes nothing.
	Detect(ctx context.Context) Availability
	// Preflight checks everything Run needs here and returns nil when a gate
	// can run. A machine that fails it refuses the gate.
	Preflight(ctx context.Context) error
	// Run runs one gate to an exit. An error means the gate never reached
	// one (refused, or setup failed): unproven. Resources stay until
	// Teardown, so CopyOut can read them.
	Run(ctx context.Context, spec Spec) (Result, error)
	// CopyOut writes a tar stream of path (absolute, inside the gate) to w,
	// and fails once more than maxBytes were written. The stream is data.
	CopyOut(ctx context.Context, runID, path string, w io.Writer, maxBytes int64) error
	// Teardown removes everything labelled with runID. Idempotent.
	Teardown(ctx context.Context, runID string) error
}

// Availability is Detect's answer.
type Availability struct {
	Available bool
	Reason    string
}

// DefaultTrustedGoCacheDir is the machine's trusted Go build cache: a plain
// GOCACHE directory that only main's seeding writes, as the daemon user.
const DefaultTrustedGoCacheDir = "/var/cache/workspace/go-build-trusted"

// TrustedGoCacheDir is the host directory a gate mounts at
// gaterun.TrustedGoCacheMount for the daemon's gate_trusted_go_cache setting: unset
// is DefaultTrustedGoCacheDir, "off" is none (""), anything else is that
// directory. The backend checks the directory before each gate and mounts
// nothing when it is missing or unsafe, so the default needs no machine to
// have one.
func TrustedGoCacheDir(setting string) string {
	switch s := strings.TrimSpace(setting); {
	case s == "":
		return DefaultTrustedGoCacheDir
	case strings.EqualFold(s, "off"):
		return ""
	default:
		return s
	}
}

// Spec is one gate as a backend runs it.
type Spec struct {
	// RunID names and labels the gate's resources:
	// [a-z0-9][a-z0-9_.-]{0,62}.
	RunID string
	// Image is the gate image: the read-only base with the repository's
	// toolchain (and forge, for sbom_scan).
	Image string
	// Command runs with SrcDir as its working directory.
	Command []string
	// SourceDir is the fetched tree on the host, copied into SrcDir. It
	// holds no credential.
	SourceDir string
	// Env is the whole environment ([GateEnv]): nothing is inherited.
	Env []string
	// VulnDBDir is the machine's grype database directory, mounted
	// read-only at VulnDBMount; "" mounts nothing (sbom_scan is then
	// unscanned).
	VulnDBDir string
	// CacheVolume is the repository's warm cache volume
	// ([CacheVolumeName]), mounted read-write at CacheMount and kept across
	// gates; "" mounts none. The Runner sets it only for a not-isolated gate
	// whose repository opted in (StartRequest.WarmCache): a cache shared
	// between gates is a path for one gate's code to poison the next one's
	// build, acceptable only under that opt-in. An isolated backend never
	// gets one.
	CacheVolume string
	// TrustedGoCacheDir is the machine's trusted Go build cache
	// ([TrustedGoCacheDir]), mounted read-only at gaterun.TrustedGoCacheMount; ""
	// mounts none. The backend mounts it only when it is an absolute, real
	// directory (not a symlink) that is neither group- nor world-writable,
	// and otherwise logs why and runs the gate without it. Unlike
	// CacheVolume it is safe for any gate: a candidate cannot write it, so
	// it can poison only its own overlay (GOCACHE).
	TrustedGoCacheDir string
	Limits            Limits
	// Stdout and Stderr receive the gate's own output; nil discards it.
	Stdout, Stderr io.Writer
}

// Limits bound one gate. A limit a backend cannot enforce is its
// Preflight's business; an exceeded one is unproven, never fail.
type Limits struct {
	CPUs        float64
	MemoryBytes int64
	Pids        int64
	DiskBytes   int64
	Wall        time.Duration
}

// Result is how a gate's command ended.
type Result struct {
	// Class is from the exit code, or unproven when the end was not seen
	// whole (a limit, a lost engine connection).
	Class gaterun.Class
	// ExitCode is the command's exit, -1 when none was read.
	ExitCode  int
	Reason    string
	TimedOut  bool
	OOMKilled bool
	Duration  time.Duration
}
