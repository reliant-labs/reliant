// Copyright (c) 2025 Reliant Labs
package gate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SysboxRuntime is the OCI runtime the sysbox backend requires the engine to
// have registered. Without it Run refuses; it never falls back to runc, whose
// containers would need --privileged to run an engine.
const SysboxRuntime = "sysbox-runc"

const (
	defaultEngineWait = 60 * time.Second
	// stopGrace bounds how long a killed gate's attach and inspect may take.
	stopGrace = 30 * time.Second
)

// SysboxConfig configures the sysbox backend for one machine.
type SysboxConfig struct {
	// ProbeImage is the trusted, machine-chosen image (holding iptables-save
	// and ip6tables-save) the backend runs with the host's network namespace
	// and CAP_NET_ADMIN to read the host's filter tables before each gate.
	// With DiskSlotsDir it also wipes a slot (sh, find, rm, mkdir), with the
	// slot alone bind-mounted.
	// It is never the gate's image, which the repository chooses. Required:
	// without it the egress rules cannot be checked, and every gate is
	// refused.
	ProbeImage string
	// DNS, when set, are the resolvers the gate container uses (--dns). Set
	// it where the host's resolver is in a denied range (GCP's is
	// 169.254.169.254), or name resolution inside the gate fails.
	DNS []string
	// DiskSlotsDir, when set, is a machine-provided directory of disk slots,
	// slot-0, slot-1, …: each the mount point of a filesystem of its own (a
	// loop-backed image or a partition, provisioned by gate-check, pipeline
	// build plan E9c), with this process able to create a lock file beside
	// it. A gate claims a free slot and keeps its volumes on it, so the
	// slot's size is the gate's disk limit, enforced by the filesystem. Left
	// empty, the gate's volumes are the engine's own with a size= limit,
	// which needs the engine's volumes directory on xfs with project quotas.
	// Without either, every gate is refused (ErrDiskLimitNotEnforced).
	DiskSlotsDir string
	// EngineWait bounds the nested engine's start (default 60s).
	EngineWait time.Duration
	// Docker is the docker CLI to run (default "docker").
	Docker string
}

// dockerExec runs one docker CLI command. Tests replace it.
type dockerExec func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error

// Sysbox is the sysbox backend: each gate is a sysbox-runc container with its
// own user, PID, mount and network namespaces, a nested dockerd whose
// data-root is a fresh per-gate volume, the tree on a second fresh volume,
// its own bridge network under the host's egress rules (egress.go), and
// cgroup limits. sysbox makes --privileged unnecessary, and the backend never
// passes it, a host path or the host engine's socket.
type Sysbox struct {
	cfg  SysboxConfig
	exec dockerExec

	// slotSize measures a disk slot (disk_linux.go); tests replace it.
	slotSize func(parent, slot string) (int64, error)

	mu    sync.Mutex
	slots map[string]*diskSlot // run id -> its claimed disk slot
}

var _ Backend = (*Sysbox)(nil)

// NewSysbox returns the sysbox backend for this machine's engine.
func NewSysbox(cfg SysboxConfig) *Sysbox {
	if cfg.EngineWait <= 0 {
		cfg.EngineWait = defaultEngineWait
	}
	if cfg.Docker == "" {
		cfg.Docker = "docker"
	}
	return &Sysbox{cfg: cfg, exec: cliExec(cfg.Docker), slotSize: slotSize, slots: map[string]*diskSlot{}}
}

// Name is "sysbox".
func (s *Sysbox) Name() string { return "sysbox" }

func cliExec(bin string) dockerExec {
	return func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		cmd.WaitDelay = 10 * time.Second
		return cmd.Run()
	}
}

// output runs a docker command and returns its stdout; a failure carries its
// stderr.
func (s *Sysbox) output(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	var out, errb bytes.Buffer
	if err := s.exec(ctx, stdin, &out, &errb, args...); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			return out.String(), fmt.Errorf("docker %s: %w", args[0], err)
		}
		return out.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// Detect reports whether the engine has sysbox-runc registered.
func (s *Sysbox) Detect(ctx context.Context) Availability {
	out, err := s.output(ctx, nil, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return Availability{Reason: fmt.Sprintf("the container engine did not answer: %v", err)}
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &runtimes); err != nil {
		return Availability{Reason: fmt.Sprintf("unreadable runtime list from docker info: %v", err)}
	}
	if _, ok := runtimes[SysboxRuntime]; !ok {
		names := make([]string, 0, len(runtimes))
		for n := range runtimes {
			names = append(names, n)
		}
		return Availability{Reason: fmt.Sprintf("the engine has no %s runtime (it has: %s); install sysbox", SysboxRuntime, strings.Join(sortedStrings(names), ", "))}
	}
	return Availability{Available: true, Reason: SysboxRuntime + " is registered with the engine"}
}

// Preflight checks the runtime, the host's egress rules and that the disk
// limit can be enforced (disk.go).
func (s *Sysbox) Preflight(ctx context.Context) (Report, error) {
	r := Report{Backend: s.Name(), Availability: s.Detect(ctx)}
	if !r.Availability.Available {
		return r, nil
	}
	probeID := "preflight-" + randomSuffix()
	defer func() { _ = s.Teardown(context.WithoutCancel(ctx), probeID) }()
	r.EgressAllow, r.Egress = s.readEgress(ctx, probeID)
	r.DiskLimit, r.DiskLimitReason = s.diskPreflight(ctx, probeID)
	return r, nil
}

// readEgress reads the host's filter tables through ProbeImage and verifies
// the gate rules are in force, returning the ranges the machine allows.
func (s *Sysbox) readEgress(ctx context.Context, runID string) ([]netip.Prefix, error) {
	if s.cfg.ProbeImage == "" {
		return nil, errors.New("no probe image is configured, so the host's egress rules cannot be checked")
	}
	if out, err := s.output(ctx, nil, "info", "--format", "{{json .FirewallBackend}}"); err == nil {
		if err := checkFirewallBackend(out); err != nil {
			return nil, err
		}
	}
	out, err := s.output(ctx, nil, probeArgs(runID, s.cfg.ProbeImage)...)
	if err != nil {
		return nil, fmt.Errorf("reading the host's filter tables: %w", err)
	}
	v4, v6 := splitDumps(out)
	return VerifyRules(v4, v6)
}

// checkFirewallBackend refuses an engine whose firewall is not iptables (the
// nftables backend has no DOCKER-USER chain). An engine too old to report one
// uses iptables.
func checkFirewallBackend(infoJSON string) error {
	var fw struct{ Driver string }
	s := strings.TrimSpace(infoJSON)
	if s == "" || s == "null" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), &fw); err != nil || fw.Driver == "" || fw.Driver == "iptables" {
		return nil
	}
	return fmt.Errorf("the engine's firewall backend is %q; gate egress rules need the iptables backend", fw.Driver)
}

// probeScript dumps every filter table the probe image can read, IPv4 and
// IPv6, from whichever iptables backends it has.
const probeScript = `for fam in iptables ip6tables; do
	for v in -nft -legacy ""; do
		cmd="${fam}${v}-save"
		command -v "$cmd" >/dev/null 2>&1 || continue
		echo "### reliant-gate-dump $fam $cmd"
		"$cmd" -t filter 2>&1 || true
	done
done
`

const dumpMarker = "### reliant-gate-dump "

func probeArgs(runID, image string) []string {
	return []string{
		"run", "--rm",
		"--runtime", "runc",
		"--network", "host",
		"--cap-drop", "ALL", "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW",
		"--read-only", "--tmpfs", "/run",
		"--user", "0",
		"--label", LabelRunID + "=" + runID,
		"--label", LabelBackend + "=sysbox",
		"--label", LabelRole + "=probe",
		"--entrypoint", "/bin/sh",
		image, "-c", probeScript,
	}
}

// splitDumps separates the probe's output into IPv4 and IPv6 dumps.
func splitDumps(out string) (v4, v6 []string) {
	for _, part := range strings.Split(out, dumpMarker)[1:] {
		header, body, _ := strings.Cut(part, "\n")
		switch strings.Fields(header + " x")[0] {
		case "iptables":
			v4 = append(v4, body)
		case "ip6tables":
			v6 = append(v6, body)
		}
	}
	return v4, v6
}

// names are one gate's resource names.
type names struct {
	container, network, work, engine, bridge string
}

func namesFor(runID string) names {
	sum := sha256.Sum256([]byte(runID))
	return names{
		container: "reliant-gate-" + runID,
		network:   "reliant-gate-" + runID,
		work:      volumeName(runID, "work"),
		engine:    volumeName(runID, "engine"),
		// IFNAMSIZ is 16 with the NUL: 5 + 10 characters.
		bridge: BridgePrefix + hex.EncodeToString(sum[:])[:10],
	}
}

func volumeName(runID, role string) string { return "reliant-gate-" + runID + "-" + role }

func labelArgs(runID, role string) []string {
	return []string{
		"--label", LabelRunID + "=" + runID,
		"--label", LabelBackend + "=sysbox",
		"--label", LabelRole + "=" + role,
	}
}

func networkCreateArgs(runID string, n names) []string {
	args := []string{"network", "create", "--driver", "bridge"}
	args = append(args, labelArgs(runID, "network")...)
	return append(args,
		"--ipv6=false",
		"--opt", "com.docker.network.bridge.name="+n.bridge,
		"--opt", "com.docker.network.bridge.enable_icc=false",
		n.network,
	)
}

func volumeCreateArgs(runID, name, role string, sizeBytes int64) []string {
	args := []string{"volume", "create"}
	args = append(args, labelArgs(runID, role)...)
	if sizeBytes > 0 {
		args = append(args, "--opt", "size="+strconv.FormatInt(sizeBytes, 10))
	}
	return append(args, name)
}

// createArgs is the gate container's `docker create`. What it never holds is
// the point: no --privileged, no host path, no engine socket, no host PID,
// network or IPC namespace, no bare --env that would copy this process's
// value in.
func createArgs(spec Spec, n names, env []string, cfg SysboxConfig) []string {
	mem := strconv.FormatInt(spec.Limits.MemoryBytes, 10)
	args := []string{
		"create",
		"--name", n.container,
		"--runtime", SysboxRuntime,
		"--init",
		"--pull", "missing",
		"--network", n.network,
		"--hostname", "gate",
		"--cpus", strconv.FormatFloat(spec.Limits.CPUs, 'f', -1, 64),
		"--memory", mem,
		"--memory-swap", mem,
		"--pids-limit", strconv.FormatInt(spec.Limits.Pids, 10),
		"--log-driver", "json-file", "--log-opt", "max-size=50m", "--log-opt", "max-file=2",
		"--mount", "type=volume,src=" + n.work + ",dst=" + WorkMount,
		"--mount", "type=volume,src=" + n.engine + ",dst=" + EngineDataRoot,
		// Every other path is bounded too (review B1 on #3793): the root
		// layer is read-only, and the few paths the image's own engine must
		// write are size-capped tmpfs, which also count against --memory.
		"--read-only",
		"--tmpfs", "/run:rw,nosuid,nodev,size=" + strconv.FormatInt(RunTmpfsBytes, 10),
		"--tmpfs", "/var/log:rw,nosuid,nodev,size=" + strconv.FormatInt(LogTmpfsBytes, 10),
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=" + strconv.FormatInt(TmpTmpfsBytes, 10),
		"--workdir", WorkMount,
		"--entrypoint", "/bin/sh",
	}
	args = append(args, labelArgs(spec.RunID, "gate")...)
	for _, d := range cfg.DNS {
		args = append(args, "--dns", d)
	}
	for _, kv := range env {
		args = append(args, "--env", kv)
	}
	args = append(args, spec.Image, "-c", bootstrapScript(cfg.EngineWait), "reliant-gate")
	return append(args, spec.Command...)
}

// Run runs one gate. See Backend.
func (s *Sysbox) Run(ctx context.Context, spec Spec) (Result, error) {
	if err := spec.Validate(); err != nil {
		return Result{}, err
	}
	if a := s.Detect(ctx); !a.Available {
		return Result{}, fmt.Errorf("%w: %s", ErrUnavailable, a.Reason)
	}
	provisioned, err := s.readEgress(ctx, spec.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrEgressNotEnforced, err)
	}
	if err := CheckEgressAllow(spec.EgressAllow, provisioned); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrEgressNotEnforced, err)
	}
	env, err := GateEnv(spec.RunID, spec.Env)
	if err != nil {
		return Result{}, err
	}
	n := namesFor(spec.RunID)
	if _, err := s.output(ctx, nil, networkCreateArgs(spec.RunID, n)...); err != nil {
		return Result{}, err
	}
	if err := s.createVolumes(ctx, spec, n); err != nil {
		return Result{}, err
	}
	if _, err := s.output(ctx, nil, createArgs(spec, n, env, s.cfg)...); err != nil {
		return Result{}, err
	}
	if err := s.verifyEnv(ctx, n.container, spec.Image, env); err != nil {
		return Result{}, err
	}
	if err := s.verifyMounts(ctx, n.container); err != nil {
		return Result{}, err
	}
	if err := s.copyIn(ctx, spec, n); err != nil {
		return Result{}, err
	}
	return s.execute(ctx, spec, n)
}

func (s *Sysbox) createVolume(ctx context.Context, runID, name, role string, size int64) error {
	_, err := s.output(ctx, nil, volumeCreateArgs(runID, name, role, size)...)
	return err
}

// The size caps of the gate's tmpfs mounts. /run holds the nested engine's
// sockets and pid files, /var/log its log, /tmp anything a tool insists on
// writing there (the gate's own TMPDIR is on the work volume).
const (
	RunTmpfsBytes = 64 << 20
	LogTmpfsBytes = 64 << 20
	TmpTmpfsBytes = 1 << 30
)

// boundedMounts are the only mount destinations a gate container may have:
// the two sized volumes. An image that declares another VOLUME gets an
// anonymous, unbounded volume from the engine, so the gate is refused.
var boundedMounts = map[string]bool{WorkMount: true, EngineDataRoot: true}

// verifyMounts refuses a container with a mount outside the two sized
// volumes (an image-declared VOLUME, say), since nothing bounds it.
func (s *Sysbox) verifyMounts(ctx context.Context, container string) error {
	out, err := s.output(ctx, nil, "container", "inspect", "--format", "{{json .Mounts}}", container)
	if err != nil {
		return err
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
		return fmt.Errorf("reading the gate container's mounts: %w", err)
	}
	var extra []string
	for _, m := range mounts {
		if !boundedMounts[m.Destination] {
			extra = append(extra, m.Destination)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("%w: %s", ErrUnboundedMount, strings.Join(sortedStrings(extra), ", "))
	}
	return nil
}

// verifyEnv refuses a container whose environment holds a variable neither
// the gate nor its image declared.
func (s *Sysbox) verifyEnv(ctx context.Context, container, image string, want []string) error {
	cOut, err := s.output(ctx, nil, "container", "inspect", "--format", "{{json .Config.Env}}", container)
	if err != nil {
		return err
	}
	iOut, err := s.output(ctx, nil, "image", "inspect", "--format", "{{json .Config.Env}}", image)
	if err != nil {
		return err
	}
	var got, img []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(cOut)), &got); err != nil {
		return fmt.Errorf("reading the gate container's environment: %w", err)
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(iOut)), &img) // an image with no ENV prints null
	return checkEnv(got, img, want)
}

func checkEnv(got, image, want []string) error {
	allowed := make(map[string]bool, len(image)+len(want))
	for _, kv := range image {
		allowed[kv] = true
	}
	for _, kv := range want {
		allowed[kv] = true
	}
	var extra []string
	for _, kv := range got {
		if !allowed[kv] {
			name, _, _ := strings.Cut(kv, "=")
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("%w: %s", ErrEnvInjected, strings.Join(sortedStrings(extra), ", "))
	}
	return nil
}

// copyIn places the tree at TreeDir on the work volume.
func (s *Sysbox) copyIn(ctx context.Context, spec Spec, n names) error {
	if spec.SourceDir != "" {
		_, err := s.output(ctx, nil, "cp", strings.TrimRight(spec.SourceDir, "/")+"/.", n.container+":"+TreeDir)
		return err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(prefixTar(spec.SourceTar, pw, path.Base(TreeDir))) }()
	_, err := s.output(ctx, pr, "cp", "-", n.container+":"+WorkMount)
	_ = pr.CloseWithError(io.ErrClosedPipe)
	return err
}

// prefixTar copies the tar stream src to dst with every entry moved under
// dir, refusing entries that name an absolute path or climb out with "..".
func prefixTar(src io.Reader, dst io.Writer, dir string) error {
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tw.Close()
		}
		if err != nil {
			return err
		}
		name, err := inside(dir, h.Name)
		if err != nil {
			return err
		}
		h.Name = name
		if h.Typeflag == tar.TypeLink {
			if h.Linkname, err = inside(dir, h.Linkname); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, tr, h.Size); err != nil {
			return err
		}
	}
}

func inside(dir, name string) (string, error) {
	clean := path.Clean("/" + name)
	if path.IsAbs(name) || strings.Contains("/"+name+"/", "/../") {
		return "", fmt.Errorf("%w: tar entry %q leaves the tree", ErrInvalidSpec, name)
	}
	if clean == "/" {
		return dir + "/", nil
	}
	return dir + clean, nil
}

// containerState is the part of `docker inspect .State` the verdict needs.
type containerState struct {
	Status    string
	ExitCode  int
	OOMKilled bool
	Error     string
}

// execute starts the gate attached, enforces the wall-time limit, and reads
// its verdict from the engine.
func (s *Sysbox) execute(ctx context.Context, spec Spec, n names) (Result, error) {
	stdout, stderr := spec.Stdout, spec.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	// The attach outlives ctx: a canceled gate is killed through the engine,
	// and its attach then ends on its own.
	attachCtx, stopAttach := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAttach()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- s.exec(attachCtx, nil, stdout, stderr, "start", "--attach", n.container) }()

	timer := time.NewTimer(spec.Limits.Wall)
	defer timer.Stop()
	var attachErr error
	timedOut, canceled := false, false
	select {
	case attachErr = <-done:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		canceled = true
	}
	if timedOut || canceled {
		s.kill(ctx, n.container)
		select {
		case attachErr = <-done:
		case <-time.After(stopGrace):
			stopAttach()
			attachErr = <-done
		}
	}
	elapsed := time.Since(start)

	st, err := s.inspectState(ctx, n.container)
	if err != nil {
		return Result{}, err
	}
	if st.Status == "created" {
		return Result{}, fmt.Errorf("the gate container did not start: %v %s", attachErr, st.Error)
	}
	res := Result{ExitCode: -1, OOMKilled: st.OOMKilled, TimedOut: timedOut, Duration: elapsed}
	if st.Status != "exited" {
		// Only a terminal exit carries a verdict. A gate still running (the
		// attach lost the engine, or the kill did not land) is stopped, and
		// whatever it would have exited is never read.
		s.kill(ctx, n.container)
		res.Class, res.Reason = ClassUnproven, fmt.Sprintf("the gate had not exited (state %q) when its attach ended (%v); it was killed", st.Status, attachErr)
		if timedOut {
			res.Reason = fmt.Sprintf("the gate exceeded its wall-time limit of %s and had not exited after the kill", spec.Limits.Wall)
		}
		if canceled {
			res.Reason = "the gate was canceled"
			return res, ctx.Err()
		}
		return res, nil
	}
	res.ExitCode = st.ExitCode
	res.Class, res.Reason = classify(st.ExitCode, st.OOMKilled, timedOut, spec.Limits.Wall)
	switch {
	case canceled:
		res.Class, res.Reason = ClassUnproven, "the gate was canceled"
		return res, ctx.Err()
	case timedOut:
		return res, nil
	case !attachSawExit(attachErr, st.ExitCode):
		// The engine says the gate exited, but the attach did not end by
		// reporting that exit: the connection was lost, and the gate's
		// output may not all have been seen. Not a verdict either way.
		res.Class, res.Reason = ClassUnproven, fmt.Sprintf("the connection to the gate was lost (%v); it exited %d, but its full output was not seen", attachErr, st.ExitCode)
	}
	return res, nil
}

// inspectState reads the gate container's state from the engine.
func (s *Sysbox) inspectState(ctx context.Context, container string) (containerState, error) {
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	out, err := s.output(inspectCtx, nil, "container", "inspect", "--format", "{{json .State}}", container)
	if err != nil {
		return containerState{}, fmt.Errorf("reading the gate's exit: %w", err)
	}
	var st containerState
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		return containerState{}, fmt.Errorf("reading the gate's exit: %w", err)
	}
	return st, nil
}

func (s *Sysbox) kill(ctx context.Context, container string) {
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	_, _ = s.output(killCtx, nil, "kill", container) // fails only when it already exited
}

// attachSawExit: the attach ended by reporting the container's own exit.
// `docker start --attach` exits with the container's status, so a clean
// exit is a nil error and any other is an exit error carrying the same code;
// anything else (the CLI's own failure, a different code) means the attach
// ended for another reason, such as a lost connection.
func attachSawExit(attachErr error, exitCode int) bool {
	if attachErr == nil {
		return exitCode == 0
	}
	var ec interface{ ExitCode() int }
	return exitCode != 0 && errors.As(attachErr, &ec) && ec.ExitCode() == exitCode
}

// classify turns how a gate ended into its verdict. An exceeded limit is
// unproven, never a fail: the wall clock ran out, or the kernel killed a
// process for memory and the suite did not pass despite it.
func classify(exitCode int, oomKilled, timedOut bool, wall time.Duration) (Class, string) {
	switch {
	case timedOut:
		return ClassUnproven, fmt.Sprintf("the gate exceeded its wall-time limit of %s", wall)
	case exitCode == 0:
		return ClassPass, ""
	case oomKilled:
		return ClassUnproven, "the gate exceeded its memory limit"
	}
	c := ClassifyExit(exitCode)
	if c == ClassUnproven {
		return c, "the gate exited 2: unproven"
	}
	return c, ""
}

// CopyOut streams a tar of path inside the gate. See Backend.
func (s *Sysbox) CopyOut(ctx context.Context, runID, p string, w io.Writer, maxBytes int64) error {
	if !runIDPattern.MatchString(runID) {
		return fmt.Errorf("%w: run id %q", ErrInvalidSpec, runID)
	}
	if !path.IsAbs(p) || path.Clean(p) != p {
		return fmt.Errorf("%w: copy-out path %q must be absolute and clean", ErrInvalidSpec, p)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lw := &limitWriter{w: w, left: maxBytes, cancel: cancel}
	var errb bytes.Buffer
	err := s.exec(ctx, nil, lw, &errb, "cp", namesFor(runID).container+":"+p, "-")
	if lw.exceeded {
		return fmt.Errorf("gate output at %s exceeds %d bytes", p, maxBytes)
	}
	if err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return lw.err
}

// limitWriter passes up to left bytes to w, then cancels the copy and
// discards the rest (an error would leave the CLI blocked on its pipe).
type limitWriter struct {
	w        io.Writer
	left     int64
	cancel   func()
	exceeded bool
	err      error
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.exceeded || l.err != nil {
		return len(p), nil
	}
	if int64(len(p)) > l.left {
		l.exceeded = true
		l.cancel()
		return len(p), nil
	}
	l.left -= int64(len(p))
	if _, err := l.w.Write(p); err != nil {
		l.err = err
		l.cancel()
	}
	return len(p), nil
}

// Teardown removes the run's containers, then its volumes and networks, by
// label, then releases its disk slot: wiped and freed only when the
// containers were confirmed removed, quarantined otherwise (disk.go). See
// Backend.
func (s *Sysbox) Teardown(ctx context.Context, runID string) error {
	if !runIDPattern.MatchString(runID) {
		// An empty or odd id must never become a label filter matching
		// other runs' resources.
		return fmt.Errorf("%w: run id %q", ErrInvalidSpec, runID)
	}
	filter := "label=" + LabelRunID + "=" + runID
	containersErr := s.removeListed(ctx, []string{"ps", "-aq", "--filter", filter}, []string{"rm", "-f", "-v"})
	errs := []error{
		containersErr,
		s.removeListed(ctx, []string{"volume", "ls", "-q", "--filter", filter}, []string{"volume", "rm", "-f"}),
		s.removeListed(ctx, []string{"network", "ls", "-q", "--filter", filter}, []string{"network", "rm"}),
		s.releaseSlot(ctx, runID, containersErr),
	}
	return errors.Join(errs...)
}

// removeListed removes what the list command prints; nil means all of it is
// gone (or there was none).
func (s *Sysbox) removeListed(ctx context.Context, list, rm []string) error {
	out, err := s.output(ctx, nil, list...)
	if err != nil {
		return err
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	_, err = s.output(ctx, nil, append(append([]string{}, rm...), ids...)...)
	return err
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sortedStrings(ss []string) []string {
	sort.Strings(ss)
	return ss
}
