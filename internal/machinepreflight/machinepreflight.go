// Copyright (c) 2026 Reliant Labs

// Package machinepreflight is a node's machine self-check (Node.preflight):
// before the node's work starts, the machine it was placed on checks that it
// can do that work at all, and says why not in one typed reason.
//
// Why. A fixer agent placed on a machine whose /tmp had lost its sticky
// mode, whose disk was full, whose daemon ran as the wrong user or whose PATH
// had no git failed minutes in, with an error about the symptom, and its
// retries ran on the same machine and failed the same way, each spending one
// of the node's attempts. The check moves that failure to before the work,
// where the runtime can give the node to another machine instead
// (internal/workflow/runtime, node_preflight.go) without spending an attempt.
//
// What is checked, on the daemon (Check), each against Spec:
//
//   - disk: free bytes on the filesystem of the daemon's home (its
//     workspace) and of /tmp, at least MinFreeBytes; and the disk guard is
//     not refusing heavy work (internal/diskguard);
//   - tmp: /tmp is a directory with mode 1777 that a file can be created in;
//   - uid: the daemon does not run as root, unless AllowRoot, and owns its
//     home;
//   - tools: git and every Spec.Tools entry resolve on the daemon's PATH;
//   - load: the 5-minute load average per CPU is at most MaxLoadPerCPU.
//
// A reading the platform cannot give (no /proc/loadavg, no uid on Windows) is
// skipped, never failed: unknown is not unhealthy.
//
// The result carries reason codes and plain messages only: paths, sizes and
// tool names, never an environment value or a secret.
//
//forge:exclude-contract: pure checks over an injected Env (Check) and the wire types of the daemon command; no I/O of its own beyond SystemEnv's OS reads, no component
package machinepreflight

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Command is the daemon command that runs Check on the daemon's machine
// (daemonruntime cmd_machine_preflight.go). A daemon older than it answers an
// unknown-command error, which the server reads as ReasonDaemonOutdated.
const Command = "machine.preflight"

// Defaults, applied to a Spec field left unset.
const (
	// DefaultMinFreeBytes is the least free disk a checked filesystem may
	// have: room for a checkout, a build's objects and a test's temp files.
	DefaultMinFreeBytes uint64 = 5 << 30
	// DefaultMaxLoadPerCPU is the most 5-minute load per CPU: twice as many
	// runnable tasks as CPUs is a machine whose new work waits in line.
	DefaultMaxLoadPerCPU = 2.0
	// DefaultMaxMachines is how many machines a node tries before it fails
	// with no healthy machine.
	DefaultMaxMachines = 3
	// MaxMachinesLimit bounds max_machines.
	MaxMachinesLimit = 20
)

// RequiredTool is checked on every machine, whatever the node lists: every
// node that runs on a machine works in a git checkout.
const RequiredTool = "git"

// TmpDir is the temp directory checked. Literal, not os.TempDir: it is the
// directory every tool falls back to, and the one a broken machine broke.
const TmpDir = "/tmp"

// Reason is why a machine failed its check. A closed set: the runtime, the
// log and the metric branch on it.
type Reason string

const (
	// ReasonDiskLow: a checked filesystem has less free space than allowed.
	ReasonDiskLow Reason = "disk_low"
	// ReasonDiskRefusing: the disk guard refuses heavy work on the machine.
	ReasonDiskRefusing Reason = "disk_refusing"
	// ReasonTmpUnusable: /tmp is missing, not mode 1777, or not writable.
	ReasonTmpUnusable Reason = "tmp_unusable"
	// ReasonUIDRoot: the daemon runs as root and the node does not allow it.
	ReasonUIDRoot Reason = "uid_root"
	// ReasonUIDMismatch: the daemon does not own its home.
	ReasonUIDMismatch Reason = "uid_mismatch"
	// ReasonToolMissing: a required tool is not on the daemon's PATH.
	ReasonToolMissing Reason = "tool_missing"
	// ReasonLoadHigh: the machine's load per CPU is above the threshold.
	ReasonLoadHigh Reason = "load_high"
	// ReasonDaemonOutdated: the daemon predates the check (server-side).
	ReasonDaemonOutdated Reason = "daemon_outdated"
	// ReasonUnreachable: the check could not reach the machine (server-side).
	ReasonUnreachable Reason = "unreachable"
	// ReasonInvalid: the daemon could not read the request (server-side
	// sent something it does not understand).
	ReasonInvalid Reason = "invalid_request"
)

// Reasons is the closed set of Reason values.
var Reasons = []Reason{
	ReasonDiskLow, ReasonDiskRefusing, ReasonTmpUnusable, ReasonUIDRoot, ReasonUIDMismatch,
	ReasonToolMissing, ReasonLoadHigh, ReasonDaemonOutdated, ReasonUnreachable, ReasonInvalid,
}

// Spec is what one check asks of a machine. The zero value of each field
// takes its default (withDefaults).
type Spec struct {
	// Tools must resolve on the daemon's PATH, beside RequiredTool.
	Tools []string `json:"tools,omitempty"`
	// MinFreeBytes is the least free space on each checked filesystem.
	MinFreeBytes uint64 `json:"min_free_bytes,omitempty"`
	// MaxLoadPerCPU is the most 5-minute load average per CPU.
	MaxLoadPerCPU float64 `json:"max_load_per_cpu,omitempty"`
	// AllowRoot accepts a daemon running as root.
	AllowRoot bool `json:"allow_root,omitempty"`
}

// WithDefaults is s with every unset field at its default, and the tool list
// sorted, deduplicated and holding RequiredTool.
func (s Spec) WithDefaults() Spec {
	if s.MinFreeBytes == 0 {
		s.MinFreeBytes = DefaultMinFreeBytes
	}
	if s.MaxLoadPerCPU <= 0 || math.IsNaN(s.MaxLoadPerCPU) || math.IsInf(s.MaxLoadPerCPU, 0) {
		s.MaxLoadPerCPU = DefaultMaxLoadPerCPU
	}
	seen := map[string]bool{RequiredTool: true}
	tools := []string{RequiredTool}
	for _, t := range s.Tools {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tools = append(tools, t)
	}
	sort.Strings(tools[1:])
	s.Tools = tools
	return s
}

// Request is the Command payload.
type Request struct {
	Spec Spec `json:"spec"`
}

// Failure is one check that failed: a Reason and a message a person can act
// on.
type Failure struct {
	Reason  Reason `json:"reason"`
	Message string `json:"message"`
}

// Result is the Command response: OK, or every check that failed, in check
// order (the first is the machine's reason).
type Result struct {
	OK       bool      `json:"ok"`
	Failures []Failure `json:"failures,omitempty"`
}

// Primary is the result's first failure, the zero Failure when it passed.
func (r Result) Primary() Failure {
	if len(r.Failures) == 0 {
		return Failure{}
	}
	return r.Failures[0]
}

// Message is every failure's message, joined.
func (r Result) Message() string {
	msgs := make([]string, 0, len(r.Failures))
	for _, f := range r.Failures {
		msgs = append(msgs, f.Message)
	}
	return strings.Join(msgs, "; ")
}

// Disk is one filesystem reading.
type Disk struct {
	FreeBytes  uint64
	TotalBytes uint64
}

// Env is what Check reads of the machine. A nil func is a reading the
// platform cannot give, and its check is skipped.
type Env struct {
	// Home is the daemon's home: its workspace lives on this filesystem.
	Home string
	// TmpDir is the temp directory checked; "" means TmpDir.
	TmpDir string
	// StatFS reads the filesystem holding path.
	StatFS func(path string) (Disk, error)
	// DiskRefusal is the disk guard's refusal of heavy work, "" when it
	// admits it (diskguard.Guard.HeavyRefusal).
	DiskRefusal func() string
	// Stat stats a path (os.Stat).
	Stat func(path string) (fs.FileInfo, error)
	// Owner is the uid owning a stat result; ok false when unknown.
	Owner func(fi fs.FileInfo) (uid int, ok bool)
	// CreateTemp creates and removes a file in dir, proving it writable.
	CreateTemp func(dir string) error
	// UID is the daemon's uid; ok false on a platform without one.
	UID func() (uid int, ok bool)
	// LookPath resolves a tool on the daemon's PATH (exec.LookPath).
	LookPath func(name string) (string, error)
	// Load5 is the 5-minute load average; ok false when unknown.
	Load5 func() (load float64, ok bool)
	// CPUs is the CPU count load is divided by; 0 skips the load check.
	CPUs float64
}

// Check runs every check of spec (with its defaults) against env.
func Check(env Env, spec Spec) Result {
	spec = spec.WithDefaults()
	var failures []Failure
	add := func(r Reason, format string, args ...any) {
		failures = append(failures, Failure{Reason: r, Message: fmt.Sprintf(format, args...)})
	}
	tmp := env.TmpDir
	if tmp == "" {
		tmp = TmpDir
	}

	// Disk: one reading per filesystem, labelled by the first path on it.
	if env.StatFS != nil {
		for _, path := range uniquePaths(env.Home, tmp) {
			d, err := env.StatFS(path)
			if err != nil {
				continue // unreadable is unknown; /tmp's own check reports a missing /tmp
			}
			if d.TotalBytes > 0 && d.FreeBytes < spec.MinFreeBytes {
				add(ReasonDiskLow, "%s has %s free, under the %s the node needs", path, FormatBytes(d.FreeBytes), FormatBytes(spec.MinFreeBytes))
			}
		}
	}
	if env.DiskRefusal != nil {
		if refusal := strings.TrimSpace(env.DiskRefusal()); refusal != "" {
			add(ReasonDiskRefusing, "the machine's disk guard refuses heavy work: %s", firstLine(refusal))
		}
	}

	// /tmp: a sticky, world-writable directory a file can be created in.
	if env.Stat != nil {
		fi, err := env.Stat(tmp)
		switch {
		case err != nil:
			add(ReasonTmpUnusable, "%s cannot be read: %v", tmp, plainErr(err))
		case !fi.IsDir():
			add(ReasonTmpUnusable, "%s is not a directory", tmp)
		case fi.Mode()&fs.ModeSticky == 0 || fi.Mode().Perm() != 0o777:
			add(ReasonTmpUnusable, "%s has mode %s, not 1777 (drwxrwxrwt)", tmp, modeString(fi.Mode()))
		case env.CreateTemp != nil:
			if err := env.CreateTemp(tmp); err != nil {
				add(ReasonTmpUnusable, "%s is not writable: %v", tmp, plainErr(err))
			}
		}
	}

	// uid: not root unless allowed, and the owner of its home.
	if env.UID != nil {
		if uid, ok := env.UID(); ok {
			if uid == 0 && !spec.AllowRoot {
				add(ReasonUIDRoot, "the daemon runs as root; a node runs as root only with preflight.allow_root")
			}
			if env.Home != "" && env.Stat != nil && env.Owner != nil {
				if fi, err := env.Stat(env.Home); err == nil {
					if owner, ok := env.Owner(fi); ok && owner != uid {
						add(ReasonUIDMismatch, "the daemon runs as uid %d but its home %s belongs to uid %d", uid, env.Home, owner)
					}
				}
			}
		}
	}

	// Tools on PATH.
	if env.LookPath != nil {
		var missing []string
		for _, tool := range spec.Tools {
			if _, err := env.LookPath(tool); err != nil {
				missing = append(missing, tool)
			}
		}
		if len(missing) > 0 {
			add(ReasonToolMissing, "not on the daemon's PATH: %s", strings.Join(missing, ", "))
		}
	}

	// Load per CPU.
	if env.Load5 != nil && env.CPUs > 0 {
		if load, ok := env.Load5(); ok {
			if per := load / env.CPUs; per > spec.MaxLoadPerCPU {
				add(ReasonLoadHigh, "load is %.2f per CPU over 5 minutes (%.1f on %s CPUs), above %.2f",
					per, load, strconv.FormatFloat(env.CPUs, 'f', -1, 64), spec.MaxLoadPerCPU)
			}
		}
	}

	return Result{OK: len(failures) == 0, Failures: failures}
}

// uniquePaths is the non-empty paths, each once.
func uniquePaths(paths ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// plainErr is an error's text without the path the message already names.
func plainErr(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// modeString is a directory's mode as octal with its sticky bit (e.g.
// 0755, 1777).
func modeString(m fs.FileMode) string {
	octal := uint32(m.Perm())
	if m&fs.ModeSticky != 0 {
		octal |= 0o1000
	}
	if m&fs.ModeSetgid != 0 {
		octal |= 0o2000
	}
	if m&fs.ModeSetuid != 0 {
		octal |= 0o4000
	}
	return fmt.Sprintf("%04o", octal)
}

// FormatBytes renders n in binary units: "5 GiB", "512 MiB".
func FormatBytes(n uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// ParseSize parses a size such as "5GiB", "500MB", "10G" or "1048576". KB,
// MB, GB and TB are decimal; KiB, MiB, GiB, TiB (and the bare K, M, G, T) are
// binary, as df -h reads.
func ParseSize(s string) (uint64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	num, unit := t[:i], strings.ToLower(strings.TrimSpace(t[i:]))
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) {
		return 0, fmt.Errorf("size %q: want a number and a unit, such as 5GiB or 500MB", s)
	}
	mult, ok := sizeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit %q (use B, KB, MB, GB, TB, KiB, MiB, GiB or TiB)", s, strings.TrimSpace(t[i:]))
	}
	out := v * mult
	if out >= math.MaxUint64/2 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return uint64(out), nil
}

var sizeUnits = map[string]float64{
	"": 1, "b": 1,
	"k": 1 << 10, "kib": 1 << 10, "kb": 1e3,
	"m": 1 << 20, "mib": 1 << 20, "mb": 1e6,
	"g": 1 << 30, "gib": 1 << 30, "gb": 1e9,
	"t": 1 << 40, "tib": 1 << 40, "tb": 1e12,
}

// Config is a node's resolved preflight: what the machine is asked (Spec)
// and how many machines the node may try.
type Config struct {
	Spec        Spec
	MaxMachines int
}

// FromProto resolves a node's preflight; ok false when the node sets none.
// An invalid field is an error (validation reports the same one).
func FromProto(p *reliantv1.NodePreflight) (cfg Config, ok bool, err error) {
	if p == nil {
		return Config{}, false, nil
	}
	cfg.Spec.Tools = append([]string(nil), p.GetTools()...)
	cfg.Spec.AllowRoot = p.GetAllowRoot()
	if v := strings.TrimSpace(p.GetMinFreeDisk()); v != "" {
		n, err := ParseSize(v)
		if err != nil {
			return Config{}, true, fmt.Errorf("min_free_disk: %w", err)
		}
		if n == 0 {
			return Config{}, true, fmt.Errorf("min_free_disk must be more than 0")
		}
		cfg.Spec.MinFreeBytes = n
	}
	if v := p.GetMaxLoad(); v != 0 {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return Config{}, true, fmt.Errorf("max_load must be a positive number, got %v", v)
		}
		cfg.Spec.MaxLoadPerCPU = v
	}
	for _, t := range p.GetTools() {
		if strings.TrimSpace(t) == "" || strings.ContainsAny(t, "/\\ \t\n") {
			return Config{}, true, fmt.Errorf("tools: %q is not a command name", t)
		}
	}
	cfg.MaxMachines = DefaultMaxMachines
	if n := p.GetMaxMachines(); n != 0 {
		if n < 1 || n > MaxMachinesLimit {
			return Config{}, true, fmt.Errorf("max_machines must be between 1 and %d, got %d", MaxMachinesLimit, n)
		}
		cfg.MaxMachines = int(n)
	}
	cfg.Spec = cfg.Spec.WithDefaults()
	return cfg, true, nil
}
