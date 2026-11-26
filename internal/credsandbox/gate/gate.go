// Copyright (c) 2025 Reliant Labs

// Package gate runs a pipeline gate — a repository's full suite, which is
// pull-request code — inside an isolation boundary that holds none of the
// machine's credentials and gives the gate a container engine of its own.
//
// The `credentials: none` sandbox (the parent package) hides the daemon's
// credential files and /proc, but it replaces the engine's socket with
// /dev/null and leaves the child no capabilities, so a suite that starts
// containers (docker compose, k3d, testcontainers) cannot run there. A gate
// needs both halves at once: no host credential, no host /proc, and an engine
// whose `docker run -v /:/host` reaches only the boundary's own root. That is
// what a Backend provides (pipeline redesign §3.3, "Gate isolation"):
//
//   - a fresh filesystem: per-gate volumes, a read-only base image, no host
//     path and no host engine socket;
//   - its own PID namespace and /proc;
//   - an environment built only from an allowlist plus the repository's
//     declared non-secret gate.env (Env), never inherited from the daemon;
//   - a nested engine started inside the boundary;
//   - egress with no route to the host, the cloud metadata endpoint or private
//     ranges unless the repository allows a range (egress.go);
//   - CPU, memory, pids, disk and wall-time limits, where an exceeded limit is
//     ClassUnproven, never ClassFail;
//   - teardown of everything labelled with the run's LabelRunID, and leftovers
//     discoverable by that label for the daemon's janitor.
//
// It fails closed: a backend that cannot provide the boundary on this machine
// reports so from Detect and Preflight, and its Run refuses with a typed error
// (ErrUnavailable, ErrEgressNotEnforced, ErrDiskLimitNotEnforced) rather than
// running the gate with less. Nothing falls back to a weaker runtime, and a
// gate whose end was not seen whole is unproven, never a pass.
//
// The sysbox backend (sysbox.go) is the one implemented; a per-gate microVM
// backend (`vm`) is planned to satisfy the same interface. Nothing in the
// daemon calls this package yet: it is used once a gate pool exists.
//
//forge:exclude-contract: Backend is a per-machine isolation strategy selected by preflight, driven through the docker CLI; its tests fake the CLI (dockerExec), so a generated mock buys nothing
package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"time"
)

// The labels every resource a gate creates carries. LabelRunID is the
// leftover contract (pipeline redesign §8): the daemon's janitor reaps
// anything labelled with a run that is gone.
const (
	LabelRunID   = "reliant.run-id"
	LabelBackend = "reliant.gate.backend"
	LabelRole    = "reliant.gate.role"
)

// Class is a gate's verdict (pipeline redesign §3.4).
type Class string

const (
	// ClassPass: the suite passed on this tree.
	ClassPass Class = "pass"
	// ClassFail: the suite failed on this tree; the code is to blame.
	ClassFail Class = "fail"
	// ClassUnproven: the gate could not decide — an exceeded limit, an engine
	// that did not start, a canceled run. Never blamed on the code, never
	// lands.
	ClassUnproven Class = "unproven"
)

// ExitCode is the gate exit contract's code for c: 0 pass, 1 fail, 2
// unproven.
func (c Class) ExitCode() int {
	switch c {
	case ClassPass:
		return 0
	case ClassFail:
		return 1
	default:
		return 2
	}
}

// ClassifyExit maps a gate command's exit code to its class: 0 is a pass, 2 is
// unproven (the gate itself could not decide), and every other code is a fail.
// A gate that wants an infrastructure failure to be unproven exits 2.
func ClassifyExit(code int) Class {
	switch code {
	case 0:
		return ClassPass
	case 2:
		return ClassUnproven
	default:
		return ClassFail
	}
}

// Backend runs gates inside one kind of isolation boundary.
//
// The caller's sequence is Detect or Preflight (once, when the machine joins a
// gate pool), then per gate Run, CopyOut for the result files, and Teardown,
// which it defers so a failed Run leaves nothing behind.
type Backend interface {
	// Name is the backend's name: "sysbox" (or "vm").
	Name() string
	// Detect reports whether this machine has the backend's runtime at all.
	// It is cheap and changes nothing.
	Detect(ctx context.Context) Availability
	// Preflight checks everything Run needs on this machine — the runtime,
	// the host-side egress rules, disk-limit support — and reports each. It
	// creates only short-lived probe resources, labelled and removed.
	Preflight(ctx context.Context) (Report, error)
	// Run runs one gate to a verdict. An error means the gate never reached
	// one (it was refused, or setup failed): the caller treats it as
	// unproven. Resources stay until Teardown, so the caller can CopyOut.
	Run(ctx context.Context, spec Spec) (Result, error)
	// CopyOut writes a tar stream of path (absolute, inside the gate) to w,
	// and fails once more than maxBytes were written. The stream is data:
	// the reader must not follow its symlinks or execute anything in it.
	CopyOut(ctx context.Context, runID, path string, w io.Writer, maxBytes int64) error
	// Teardown removes every container, volume and network labelled with
	// runID. It is idempotent: a run with nothing left is a no-op.
	Teardown(ctx context.Context, runID string) error
}

// Availability is Detect's answer.
type Availability struct {
	Available bool
	// Reason says why the backend is unavailable, or what was found.
	Reason string
}

// Report is Preflight's answer, one field per requirement.
type Report struct {
	Backend      string
	Availability Availability
	// Egress is nil when the host-side egress rules are in force, and says
	// what is missing otherwise.
	Egress error
	// EgressAllow is the set of ranges this machine's rules let gates reach
	// despite the default deny. A Spec must declare exactly what these cover.
	EgressAllow []netip.Prefix
	// DiskLimit is true when a gate's disk can be bounded here; Run refuses
	// every gate otherwise (disk.go).
	DiskLimit       bool
	DiskLimitReason string
}

// Ready is nil when a gate can run on this machine: the runtime, the egress
// rules and an enforceable disk limit, each required.
func (r Report) Ready() error {
	if !r.Availability.Available {
		return fmt.Errorf("%w: %s", ErrUnavailable, r.Availability.Reason)
	}
	if r.Egress != nil {
		return fmt.Errorf("%w: %v", ErrEgressNotEnforced, r.Egress)
	}
	if !r.DiskLimit {
		return fmt.Errorf("%w: %s", ErrDiskLimitNotEnforced, r.DiskLimitReason)
	}
	return nil
}

// Spec is one gate.
type Spec struct {
	// RunID names the gate's resources and labels them (LabelRunID). It is
	// unique per gate run: [a-z0-9][a-z0-9_.-]{0,62}.
	RunID string
	// Image is the gate image (see SysboxImageContract). It is the read-only
	// base: everything the gate writes goes to its own volumes or to a
	// writable layer removed at teardown.
	Image string
	// Command is the gate's command, run with the tree as its working
	// directory. Its exit code is the gate exit contract (ClassifyExit).
	Command []string
	// The tree, exactly one of: a directory on the host, copied in; or a tar
	// stream, whose entries are placed under the tree's root. The caller
	// fetched it outside the boundary; no credential may be in it.
	SourceDir string
	SourceTar io.Reader
	// Env is the repository's declared non-secret gate.env. It is added to
	// the allowlisted base environment (BaseEnv) and may not set a reserved
	// name (ReservedEnv). Values are visible to anyone who can inspect the
	// container: never a secret.
	Env map[string]string
	// EgressAllow are the private or otherwise denied ranges the gate may
	// reach. They must match what the machine's rules allow (Report.EgressAllow).
	EgressAllow []netip.Prefix
	Limits      Limits
	// Stdout and Stderr receive the gate's output; nil discards it.
	Stdout, Stderr io.Writer
}

// Limits bound one gate. Every one is required: no gate runs unbounded.
// DiskBytes caps each of the gate's two volumes (xfs project quotas), or is
// the most its disk slot may hold (SysboxConfig.DiskSlotsDir); a machine that
// can do neither refuses the gate (ErrDiskLimitNotEnforced).
type Limits struct {
	CPUs        float64
	MemoryBytes int64
	Pids        int64
	DiskBytes   int64
	Wall        time.Duration
}

// Result is a gate's verdict and how it was reached.
type Result struct {
	Class Class
	// ExitCode is the gate command's exit, or -1 when the gate never reached
	// a terminal exit. It is not a verdict by itself: Class is.
	ExitCode int
	// Reason says why a gate is unproven when no exit code decided it.
	Reason    string
	TimedOut  bool
	OOMKilled bool
	Duration  time.Duration
}

var (
	// ErrUnavailable: the backend's runtime is not on this machine. Run
	// refuses rather than running the gate with a weaker one.
	ErrUnavailable = errors.New("gate backend unavailable")
	// ErrEgressNotEnforced: the host-side egress rules a gate needs are not in
	// force, or do not match what the gate declared.
	ErrEgressNotEnforced = errors.New("gate egress policy not enforced on this machine")
	// ErrDiskLimitNotEnforced: the machine cannot bound the gate's disk (no
	// xfs project quotas, no usable disk slot). The gate is refused rather
	// than run unbounded.
	ErrDiskLimitNotEnforced = errors.New("gate disk limit not enforceable on this machine")
	// ErrInvalidSpec: the Spec cannot be run as given.
	ErrInvalidSpec = errors.New("invalid gate spec")
	// ErrEnvInjected: the created container's environment held a variable
	// neither the gate nor its image declared (a docker CLI config injecting
	// proxies, say). The gate is not started.
	ErrEnvInjected = errors.New("gate environment holds an undeclared variable")
	// ErrUnboundedMount: the gate container has a mount outside its two
	// sized volumes (an image-declared VOLUME becomes an anonymous volume no
	// limit bounds). The gate is not started.
	ErrUnboundedMount = errors.New("gate container has an unbounded mount")
	// ErrDiskSlotQuarantined: Teardown could not confirm a gate's containers
	// were removed, so its disk slot was neither wiped nor released. The slot
	// stays out of use (a marker beside it says why) until a Teardown of the
	// same run confirms them gone, or an operator clears it.
	ErrDiskSlotQuarantined = errors.New("gate disk slot quarantined")
)

var runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// Validate reports what makes spec unrunnable.
func (s Spec) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, a...))
	}
	switch {
	case !runIDPattern.MatchString(s.RunID):
		return bad("run id %q must match %s", s.RunID, runIDPattern)
	case s.Image == "":
		return bad("no image")
	case len(s.Command) == 0:
		return bad("no command")
	case (s.SourceDir == "") == (s.SourceTar == nil):
		return bad("exactly one of SourceDir and SourceTar is required")
	case s.Limits.CPUs <= 0, s.Limits.MemoryBytes <= 0, s.Limits.Pids <= 0, s.Limits.DiskBytes <= 0, s.Limits.Wall <= 0:
		return bad("cpu, memory, pids, disk and wall limits are all required (got %+v)", s.Limits)
	}
	if _, err := GateEnv(s.RunID, s.Env); err != nil {
		return err
	}
	for _, p := range s.EgressAllow {
		if !p.IsValid() || !p.Addr().Is4() {
			return bad("egress allow %v: only valid IPv4 prefixes (gates have no IPv6 egress)", p)
		}
	}
	return nil
}
