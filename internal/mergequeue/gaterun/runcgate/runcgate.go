// Copyright (c) 2025 Reliant Labs

// Package runcgate is the gate's not-isolated backend: a plain runc container
// on the machine's own docker engine (owner decision, 2026-11-26).
//
// It exists so the pipeline can run its shadow day before any sysbox gate
// machine does, and nothing more: a gate it ran is gaterun.IsolationRunc, and
// gaterun.GateResult.Landable is false for every such result. gaterun's
// Runner chooses it only for a shadow gate of a repository whose mode is
// shadow.
//
// It is as contained as runc allows without root on the host:
//   - no host path is mounted except the grype database and the machine's
//     trusted Go build cache, both read-only; the tree is copied into a
//     per-gate volume, and the output is copied back out;
//   - no host engine socket, no privileges, every capability dropped,
//     no-new-privileges, a read-only root filesystem;
//   - the environment is exactly the gate's (gaterun.GateEnv), checked after
//     create, so a docker CLI config cannot inject a proxy or a token;
//   - CPU, memory (no swap), pids and wall time are bounded.
//
// What it cannot give, and why it is not isolated: the container's root is
// the host's root (no user namespace), there is no nested engine (a suite
// that starts containers fails here), egress is not restricted (it is on the
// engine's default bridge, so the host's own addresses and the bridge's other
// containers are reachable), and disk is not bounded. A network per gate was
// tried and dropped: it isolates nothing while egress is open, and a shared
// engine runs out of address pools for it.
//
// The warm cache (Spec.CacheVolume) weakens it further, on purpose. A gate
// whose repository set gate.allow_unisolated gets that repository's named
// cache volume (gaterun.CacheVolumeName: reliant-gate-cache-<owner>-<name>-<hash>),
// read-write at gaterun.CacheMount. The gate image points GOCACHE, GOMODCACHE, the pnpm
// store and XDG_CACHE_HOME there. The volume is labelled LabelCache (no run
// id), so Teardown leaves it, and the janitor finds caches by that label. A
// same-name volume without those labels is refused (ErrCacheForeign), never
// mounted. A
// cache shared across gates is a cross-gate poisoning vector: one candidate's
// code can plant a build artifact that a later candidate's gate trusts. It
// is acceptable only because the repository opted in to unisolated gates and
// a runc gate is never landable. The follow-up that closes this gap and the
// missing docker engine together is a dedicated gate machine with sysbox,
// where each gate gets isolation and a nested engine of its own.
//
// The trusted Go build cache (Spec.TrustedGoCacheDir) is a different kind of
// sharing. It is the machine's GOCACHE that only main's seeding writes,
// bind-mounted read-only at gaterun.TrustedGoCacheMount; the gate image's
// GOCACHEPROG (`forge cache prog --trusted`) reads it before the gate's own
// GOCACHE and writes only to GOCACHE. A candidate cannot write the trusted
// tier, so it can poison only its own overlay: unlike the warm cache volume,
// this mount adds no cross-gate poisoning vector. It is mounted only when the
// directory is absolute, a real directory (not a symlink) and neither group-
// nor world-writable; otherwise the gate logs one line and runs without it.
//
//forge:exclude-contract: a per-machine gate backend implementing gatehost.Backend (the consumer's interface), driven through the docker CLI; its tests fake the CLI, so a generated mock buys nothing
package runcgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun/gatehost"
)

// Labels on everything a gate creates; the leftover contract with the
// daemon's janitor (pipeline redesign §8).
const (
	LabelRunID   = "reliant.run-id"
	LabelBackend = "reliant.gate.backend"
	// LabelCache marks a repository's warm cache volume; its value is the
	// volume name. It carries no run id, so no gate's Teardown removes it.
	LabelCache = "reliant.gate.cache"
)

// ErrEnvInjected: the created container's environment held a variable
// neither the gate nor its image declared. The gate is not started.
var ErrEnvInjected = errors.New("gate environment holds an undeclared variable")

// ErrCacheForeign: a volume with the warm cache's name exists but lacks the
// labels this backend gives a cache, so something else made it. It is not
// mounted, and the gate is not started.
var ErrCacheForeign = errors.New("the warm cache volume was not created by the gate backend")

// ErrInvalidSpec: the Spec cannot be run as given.
var ErrInvalidSpec = errors.New("invalid gate spec")

// Backend is the runc gate backend.
type Backend struct {
	// Docker is the docker CLI; "" is "docker" on PATH.
	Docker string
	// run is the CLI seam: it runs docker with args and returns its exit
	// code; an error means docker could not be run at all.
	run func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error)
}

// New returns the runc backend over the docker CLI.
func New() *Backend { return &Backend{} }

var _ gatehost.Backend = (*Backend)(nil)

func (b *Backend) exec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error) {
	if b.run != nil {
		return b.run(ctx, stdin, stdout, stderr, args...)
	}
	bin := b.Docker
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// docker runs one CLI call and returns its stdout, failing on a non-zero
// exit with its stderr.
func (b *Backend) docker(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	code, err := b.exec(ctx, nil, &stdout, &stderr, args...)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Isolation implements gatehost.Backend.
func (b *Backend) Isolation() gaterun.Isolation { return gaterun.IsolationRunc }

// Detect implements gatehost.Backend: a docker engine answers.
func (b *Backend) Detect(ctx context.Context) gatehost.Availability {
	v, err := b.docker(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return gatehost.Availability{Reason: "no docker engine: " + err.Error()}
	}
	return gatehost.Availability{Available: true, Reason: "docker " + v}
}

// Preflight implements gatehost.Backend: the engine has the runc runtime.
func (b *Backend) Preflight(ctx context.Context) error {
	out, err := b.docker(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return err
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &runtimes); err != nil {
		return fmt.Errorf("read the engine's runtimes: %w", err)
	}
	if _, ok := runtimes["runc"]; !ok {
		return errors.New("the docker engine has no runc runtime")
	}
	return nil
}

var (
	runIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	cacheVolumePattern = regexp.MustCompile(`^reliant-gate-cache-[a-z0-9_.-]{1,201}$`)
)

func validate(s gatehost.Spec) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, a...))
	}
	switch {
	case !runIDPattern.MatchString(s.RunID):
		return bad("run id %q", s.RunID)
	case s.Image == "":
		return bad("no image")
	case len(s.Command) == 0:
		return bad("no command")
	case s.SourceDir == "":
		return bad("no source")
	case s.Limits.CPUs <= 0, s.Limits.MemoryBytes <= 0, s.Limits.Pids <= 0, s.Limits.Wall <= 0:
		return bad("cpu, memory, pids and wall limits are required (got %+v)", s.Limits)
	case s.CacheVolume != "" && !cacheVolumePattern.MatchString(s.CacheVolume):
		return bad("cache volume %q is not reliant-gate-cache-<repo>", s.CacheVolume)
	}
	for _, kv := range s.Env {
		if !strings.Contains(kv, "=") {
			return bad("env entry %q has no value", kv)
		}
	}
	return nil
}

func names(runID string) (container, volume string) {
	n := "reliant-gate-" + runID
	return n, n + "-work"
}

// createArgs is the `docker create` of one gate. Every value is explicit: no
// host path but the read-only database and trusted Go cache (Run checks the
// cache first), no inherited variable, no privilege.
func createArgs(s gatehost.Spec) []string {
	ctr, vol := names(s.RunID)
	args := []string{
		"create", "--name", ctr, "--runtime", "runc",
		"--label", LabelRunID + "=" + s.RunID, "--label", LabelBackend + "=runc",
		"--network", "bridge", "--hostname", "gate",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=1g", "--tmpfs", "/run:rw,nosuid,nodev,size=64m",
		"--mount", "type=volume,source=" + vol + ",target=" + gaterun.WorkMount,
		"--cpus", strconv.FormatFloat(s.Limits.CPUs, 'f', -1, 64),
		"--memory", strconv.FormatInt(s.Limits.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(s.Limits.MemoryBytes, 10),
		"--pids-limit", strconv.FormatInt(s.Limits.Pids, 10),
		"--workdir", gaterun.SrcDir,
		"--entrypoint", s.Command[0],
	}
	if s.CacheVolume != "" {
		args = append(args, "--mount", "type=volume,source="+s.CacheVolume+",target="+gaterun.CacheMount)
	}
	if s.VulnDBDir != "" {
		args = append(args, "--mount", "type=bind,source="+s.VulnDBDir+",target="+gaterun.VulnDBMount+",readonly")
	}
	if s.TrustedGoCacheDir != "" {
		args = append(args, "--mount", "type=bind,source="+s.TrustedGoCacheDir+",target="+gaterun.TrustedGoCacheMount+",readonly")
	}
	for _, kv := range s.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, s.Image)
	return append(args, s.Command[1:]...)
}

// Run implements gatehost.Backend.
func (b *Backend) Run(ctx context.Context, s gatehost.Spec) (gatehost.Result, error) {
	if err := validate(s); err != nil {
		return gatehost.Result{}, err
	}
	ctr, vol := names(s.RunID)
	labels := []string{"--label", LabelRunID + "=" + s.RunID, "--label", LabelBackend + "=runc"}
	if _, err := b.docker(ctx, append(append([]string{"volume", "create"}, labels...), vol)...); err != nil {
		return gatehost.Result{}, err
	}
	stdout, stderr := orDiscard(s.Stdout), orDiscard(s.Stderr)
	if s.CacheVolume != "" {
		if err := b.ensureCache(ctx, s.CacheVolume); err != nil {
			return gatehost.Result{}, err
		}
		_, _ = fmt.Fprintf(stdout, "reliant-gate: %s: warm cache volume %s (%s) at %s\n", s.RunID, s.CacheVolume, b.volumeSize(ctx, s.CacheVolume), gaterun.CacheMount)
		defer func() {
			_, _ = fmt.Fprintf(stdout, "reliant-gate: %s: warm cache volume %s is %s after the gate\n", s.RunID, s.CacheVolume, b.volumeSize(context.WithoutCancel(ctx), s.CacheVolume))
		}()
	}
	if s.TrustedGoCacheDir != "" {
		if err := checkTrustedGoCache(s.TrustedGoCacheDir); err != nil {
			_, _ = fmt.Fprintf(stdout, "reliant-gate: %s: trusted Go cache not mounted, the gate builds on its own cache: %v\n", s.RunID, err)
			s.TrustedGoCacheDir = ""
		} else {
			_, _ = fmt.Fprintf(stdout, "reliant-gate: %s: trusted Go cache %s read-only at %s\n", s.RunID, s.TrustedGoCacheDir, gaterun.TrustedGoCacheMount)
		}
	}
	if _, err := b.docker(ctx, createArgs(s)...); err != nil {
		return gatehost.Result{}, err
	}
	if err := b.checkEnv(ctx, ctr, s); err != nil {
		return gatehost.Result{}, err
	}
	// -a: the tree is owned by the image's user (the gate image runs as a
	// non-root user), not by the host uid that fetched it, so the gate can
	// write in its own checkout.
	if _, err := b.docker(ctx, "cp", "-a", strings.TrimRight(s.SourceDir, "/")+"/.", ctr+":"+gaterun.SrcDir); err != nil {
		return gatehost.Result{}, err
	}

	wctx, cancel := context.WithTimeout(ctx, s.Limits.Wall)
	defer cancel()
	start := time.Now()
	code, attachErr := b.exec(wctx, nil, stdout, stderr, "start", "--attach", ctr)
	res := gatehost.Result{Class: gaterun.ClassUnproven, ExitCode: -1, Duration: time.Since(start)}
	if wctx.Err() != nil {
		b.kill(ctx, ctr)
		if ctx.Err() != nil {
			return gatehost.Result{}, ctx.Err()
		}
		res.TimedOut, res.Reason = true, fmt.Sprintf("the gate ran past its %s wall limit", s.Limits.Wall)
		return res, nil
	}
	state, err := b.state(ctx, ctr)
	switch {
	case err != nil:
		res.Reason = "could not read the gate's end: " + err.Error()
	case state.Status != "exited":
		b.kill(ctx, ctr)
		res.Reason = fmt.Sprintf("the gate is %s, not exited, after its attach ended", state.Status)
	case state.OOMKilled:
		res.OOMKilled, res.ExitCode, res.Reason = true, state.ExitCode, "the gate ran out of memory"
	case attachErr != nil:
		res.Reason = "the attach to the gate was lost: " + attachErr.Error()
	case code != state.ExitCode:
		res.Reason = fmt.Sprintf("the attach reported exit %d, the engine %d", code, state.ExitCode)
	default:
		res.Class, res.ExitCode = gaterun.ClassifyExit(state.ExitCode), state.ExitCode
	}
	return res, nil
}

// checkTrustedGoCache says why dir must not be mounted as the trusted Go
// cache, or nil. It must be an absolute, clean path that `--mount` cannot
// misread (no comma), name a real directory rather than a symlink to one,
// and be writable by its owner alone: a directory another user can write is
// not a cache only main's seeding fills.
func checkTrustedGoCache(dir string) error {
	switch {
	case !filepath.IsAbs(dir) || filepath.Clean(dir) != dir:
		return fmt.Errorf("%q is not a clean absolute path", dir)
	case strings.ContainsAny(dir, ",\"\n"):
		return fmt.Errorf("%q holds a character a mount option cannot carry", dir)
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist", dir)
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", dir)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s is group- or world-writable (%s)", dir, fi.Mode().Perm())
	}
	return nil
}

type containerState struct {
	Status    string `json:"Status"`
	ExitCode  int    `json:"ExitCode"`
	OOMKilled bool   `json:"OOMKilled"`
}

func (b *Backend) state(ctx context.Context, ctr string) (containerState, error) {
	out, err := b.docker(ctx, "inspect", "--format", "{{json .State}}", ctr)
	if err != nil {
		return containerState{}, err
	}
	var st containerState
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return containerState{}, fmt.Errorf("decode the gate's state: %w", err)
	}
	return st, nil
}

// ensureCache creates the warm cache volume name, or finds it, and refuses
// one this backend did not make. `docker volume create` is idempotent and
// returns an existing volume of that name unchanged, so the labels are read
// back afterwards: a same-name volume that someone else created (and could
// have filled) is never mounted into a gate.
func (b *Backend) ensureCache(ctx context.Context, name string) error {
	if _, err := b.docker(ctx, "volume", "create", "--label", LabelCache+"="+name, "--label", LabelBackend+"=runc", name); err != nil {
		return err
	}
	out, err := b.docker(ctx, "volume", "inspect", "--format", "{{json .Labels}}", name)
	if err != nil {
		return err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return fmt.Errorf("decode the cache volume's labels: %w", err)
	}
	if labels[LabelCache] != name || labels[LabelBackend] != "runc" {
		return fmt.Errorf("%w: %s has labels %v", ErrCacheForeign, name, labels)
	}
	return nil
}

// volumeSize is the engine's size of the volume name, as `docker system df
// -v` reports it ("1.2GB"), or why it is unknown. It is for the log only.
func (b *Backend) volumeSize(ctx context.Context, name string) string {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := b.docker(dctx, "system", "df", "-v", "--format", "{{json .Volumes}}")
	if err != nil {
		return "size unknown: " + err.Error()
	}
	var vols []struct{ Name, Size string }
	if err := json.Unmarshal([]byte(out), &vols); err != nil {
		return "size unknown: " + err.Error()
	}
	for _, v := range vols {
		if v.Name == name {
			return v.Size
		}
	}
	return "size unknown: not listed"
}

// checkEnv refuses a created container whose environment holds anything the
// gate or its image did not declare.
func (b *Backend) checkEnv(ctx context.Context, ctr string, s gatehost.Spec) error {
	allowed := map[string]bool{}
	for _, kv := range s.Env {
		allowed[kv] = true
	}
	imgOut, err := b.docker(ctx, "image", "inspect", "--format", "{{json .Config.Env}}", s.Image)
	if err != nil {
		return err
	}
	var imgEnv []string
	if err := json.Unmarshal([]byte(imgOut), &imgEnv); err != nil {
		return fmt.Errorf("decode the image's env: %w", err)
	}
	for _, kv := range imgEnv {
		allowed[kv] = true
	}
	out, err := b.docker(ctx, "inspect", "--format", "{{json .Config.Env}}", ctr)
	if err != nil {
		return err
	}
	var env []string
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return fmt.Errorf("decode the gate's env: %w", err)
	}
	for _, kv := range env {
		if !allowed[kv] {
			name, _, _ := strings.Cut(kv, "=")
			return fmt.Errorf("%w: %s", ErrEnvInjected, name)
		}
	}
	return nil
}

func (b *Backend) kill(ctx context.Context, ctr string) {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, _ = b.docker(kctx, "kill", ctr)
}

// CopyOut implements gatehost.Backend: `docker cp <ctr>:<path> -`, a tar
// stream, cut off past maxBytes.
func (b *Backend) CopyOut(ctx context.Context, runID, path string, w io.Writer, maxBytes int64) error {
	ctr, _ := names(runID)
	cw := &cappedWriter{w: w, left: maxBytes}
	var stderr bytes.Buffer
	code, err := b.exec(ctx, nil, cw, &stderr, "cp", ctr+":"+path, "-")
	if cw.over {
		return fmt.Errorf("the gate's output is over %d bytes", maxBytes)
	}
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}
	return nil
}

// Teardown implements gatehost.Backend: every container and volume labelled
// with runID, in that order.
func (b *Backend) Teardown(ctx context.Context, runID string) error {
	filter := "label=" + LabelRunID + "=" + runID
	var errs []error
	for _, kind := range [][]string{{"ps", "-aq"}, {"volume", "ls", "-q"}} {
		out, err := b.docker(ctx, append(kind, "--filter", filter)...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ids := strings.Fields(out)
		if len(ids) == 0 {
			continue
		}
		var rm []string
		switch kind[0] {
		case "ps":
			rm = append([]string{"rm", "-fv"}, ids...)
		default:
			rm = append([]string{kind[0], "rm"}, ids...)
		}
		if _, err := b.docker(ctx, rm...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type cappedWriter struct {
	w    io.Writer
	left int64
	over bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.over = true
		return 0, errors.New("over the output cap")
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
