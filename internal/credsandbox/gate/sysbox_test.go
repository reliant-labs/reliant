// Copyright (c) 2025 Reliant Labs
package gate

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker stands in for the docker CLI: it records every command and
// answers the ones the backend runs.
type fakeDocker struct {
	t *testing.T

	mu    sync.Mutex
	calls [][]string

	runtimes     string              // docker info .Runtimes
	mounts       []map[string]string // container inspect .Mounts; nil = the two sized volumes
	v4, v6       string              // the probe's dumps
	containerEnv []string
	imageEnv     []string
	state        containerState
	sizeFails    bool                            // volume create --opt size fails
	attach       func(ctx context.Context) error // docker start --attach; default: the CLI's exit for state
	killed       chan struct{}
	killOnce     sync.Once
	slotSize     int64 // what every disk slot holds
	wiped        []string
	listed       map[string]string // list command (first two args) -> ids
	containersOf map[string]string // run id -> what `ps -aq --filter label=<run id>` lists, over listed["ps"]
	psFails      bool              // listing containers fails
	rmFails      bool              // removing containers fails
	copiedTar    bytes.Buffer
}

func newFake(t *testing.T) *fakeDocker {
	v4, v6 := readDumps(t)
	return &fakeDocker{
		t:        t,
		runtimes: `{"runc":{"path":"runc"},"sysbox-runc":{"path":"/usr/bin/sysbox-runc"}}`,
		v4:       v4, v6: v6,
		imageEnv:     []string{"PATH=/image/bin", "DOCKER_VERSION=27.0"},
		state:        containerState{Status: "exited"},
		killed:       make(chan struct{}),
		listed:       map[string]string{},
		containersOf: map[string]string{},
	}
}

func (f *fakeDocker) backend(cfg SysboxConfig) *Sysbox {
	if cfg.ProbeImage == "" {
		cfg.ProbeImage = "probe@sha256:abc"
	}
	s := NewSysbox(cfg)
	s.exec = f.exec
	s.slotSize = func(_, slot string) (int64, error) { return f.slotSize, nil }
	return s
}

// exitStatus is an exit error with a code, as *exec.ExitError is.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

func (f *fakeDocker) exec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{}, args...))
	f.mu.Unlock()
	head := strings.Join(args[:min(2, len(args))], " ")
	switch {
	case head == "info --format":
		switch args[2] {
		case "{{json .Runtimes}}":
			fmt.Fprintln(stdout, f.runtimes)
		case "{{json .FirewallBackend}}":
			fmt.Fprintln(stdout, `{"Driver":"iptables"}`)
		}
	case args[0] == "run" && slices.Contains(args, wipeScript):
		f.mu.Lock()
		for _, a := range args {
			if strings.HasPrefix(a, "type=bind,src=") {
				f.wiped = append(f.wiped, a)
			}
		}
		f.mu.Unlock()
	case args[0] == "run": // the egress probe
		fmt.Fprintf(stdout, "%siptables iptables-save\n%s%sip6tables ip6tables-save\n%s", dumpMarker, f.v4, dumpMarker, f.v6)
	case head == "volume create":
		if f.sizeFails && slices.Contains(args, "--opt") {
			fmt.Fprintln(stderr, "quota size requested but no quota support")
			return errors.New("exit status 1")
		}
	case head == "container inspect" && strings.Contains(args[3], ".Mounts"):
		mounts := f.mounts
		if mounts == nil {
			mounts = []map[string]string{{"Type": "volume", "Destination": WorkMount}, {"Type": "volume", "Destination": EngineDataRoot}}
		}
		_ = json.NewEncoder(stdout).Encode(mounts)
	case head == "container inspect" && strings.Contains(args[3], "Config.Env"):
		env := f.containerEnv
		if env == nil {
			// What the engine does: the image's ENV, overridden by --env.
			env = mergeEnv(f.imageEnv, envArgs(f.createCall()))
		}
		_ = json.NewEncoder(stdout).Encode(env)
	case head == "image inspect":
		_ = json.NewEncoder(stdout).Encode(f.imageEnv)
	case head == "container inspect":
		_ = json.NewEncoder(stdout).Encode(f.state)
	case head == "cp -":
		_, _ = io.Copy(&f.copiedTar, stdin)
	case head == "start --attach":
		if f.attach != nil {
			return f.attach(ctx)
		}
		if f.state.ExitCode != 0 {
			return exitStatus(f.state.ExitCode) // what the CLI does: it exits with the container's status
		}
	case args[0] == "kill":
		f.killOnce.Do(func() { close(f.killed) })
	case head == "ps -aq":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.psFails {
			fmt.Fprintln(stderr, "Cannot connect to the Docker daemon")
			return errors.New("exit status 1")
		}
		if ids, ok := f.containersOf[strings.TrimPrefix(args[len(args)-1], "label="+LabelRunID+"=")]; ok {
			fmt.Fprint(stdout, ids)
			return nil
		}
		fmt.Fprint(stdout, f.listed["ps"])
	case head == "rm -f":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rmFails {
			fmt.Fprintln(stderr, "Error response from daemon: could not kill container")
			return errors.New("exit status 1")
		}
	case head == "volume ls", head == "network ls":
		fmt.Fprint(stdout, f.listed[args[0]])
	}
	return nil
}

func (f *fakeDocker) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeDocker) createCall() []string { // the latest
	cmds := f.commands()
	for i := len(cmds) - 1; i >= 0; i-- {
		if cmds[i][0] == "create" {
			return cmds[i]
		}
	}
	return nil
}

func (f *fakeDocker) ran(verb ...string) bool {
	for _, c := range f.commands() {
		if len(c) >= len(verb) && slices.Equal(c[:len(verb)], verb) {
			return true
		}
	}
	return false
}

func envArgs(create []string) []string {
	var out []string
	for i := 0; i+1 < len(create); i++ {
		if create[i] == "--env" {
			out = append(out, create[i+1])
		}
	}
	return out
}

func mergeEnv(base, over []string) []string {
	m := map[string]string{}
	var order []string
	for _, kv := range append(slices.Clone(base), over...) {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = kv
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, m[k])
	}
	return out
}

func testSpec(t *testing.T) Spec {
	s := validSpec()
	s.SourceDir = t.TempDir()
	s.Limits.Wall = time.Minute
	s.EgressAllow = mustPrefixes("10.20.0.0/16") // what the testdata machine allows
	return s
}

// The create command is the boundary. It must never hold what would hand the
// gate the host: privilege, a host path, the host engine's socket, a host
// namespace, or a bare --env.
func TestCreateArgsHoldNoHostAccess(t *testing.T) {
	t.Setenv("RELIANT_GATE_TEST_CANARY", "daemon-secret")
	spec := validSpec()
	spec.Env = map[string]string{"GOFLAGS": "-mod=mod"}
	spec.Limits.DiskBytes = 10 << 30
	env, err := GateEnv(spec.RunID, spec.Env)
	if err != nil {
		t.Fatal(err)
	}
	n := namesFor(spec.RunID)
	args := createArgs(spec, n, env, SysboxConfig{DNS: []string{"1.1.1.1"}, EngineWait: time.Minute})
	joined := strings.Join(args, " ")

	banned := []string{"--privileged", "-v", "--volume", "--volumes-from", "--pid", "--ipc", "--userns", "--uts",
		"--cap-add", "--security-opt", "--device", "--env-file", "--cgroup-parent", "--add-host", "-e"}
	for _, a := range args {
		if slices.Contains(banned, a) {
			t.Errorf("create args hold %q", a)
		}
		if strings.Contains(a, "daemon-secret") {
			t.Errorf("create args hold this process's environment: %q", a)
		}
	}
	flag := func(name string) []string {
		var vals []string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				vals = append(vals, args[i+1])
			}
		}
		return vals
	}
	want := map[string][]string{
		"--runtime":     {SysboxRuntime},
		"--network":     {n.network},
		"--cpus":        {"4"},
		"--memory":      {"8589934592"},
		"--memory-swap": {"8589934592"},
		"--pids-limit":  {"4096"},
		"--dns":         {"1.1.1.1"},
		"--entrypoint":  {"/bin/sh"},
		"--mount": {
			"type=volume,src=reliant-gate-run-1-work,dst=/gate",
			"type=volume,src=reliant-gate-run-1-engine,dst=/var/lib/docker",
		},
		"--label": {"reliant.run-id=run-1", "reliant.gate.backend=sysbox", "reliant.gate.role=gate"},
		"--env":   env,
	}
	for name, vals := range want {
		if got := flag(name); !slices.Equal(got, vals) {
			t.Errorf("%s = %q, want %q", name, got, vals)
		}
	}
	for _, kv := range flag("--env") {
		if !strings.Contains(kv, "=") {
			t.Errorf("bare --env %q copies this process's value in", kv)
		}
	}
	if !slices.Contains(args, "--init") {
		t.Errorf("no --init: %s", joined)
	}
	// The command follows the image, after the bootstrap.
	i := slices.Index(args, spec.Image)
	if i < 0 || args[i+1] != "-c" || args[i+3] != "reliant-gate" || !slices.Equal(args[i+4:], spec.Command) {
		t.Errorf("image and command tail = %q", args[max(i, 0):])
	}
}

func TestNetworkAndVolumeArgs(t *testing.T) {
	n := namesFor("run-1")
	if len(n.bridge) > 15 || !strings.HasPrefix(n.bridge, BridgePrefix) {
		t.Errorf("bridge %q must start %q and fit IFNAMSIZ", n.bridge, BridgePrefix)
	}
	if namesFor("run-2").bridge == n.bridge {
		t.Error("two runs share a bridge name")
	}
	net := networkCreateArgs("run-1", n)
	for _, want := range []string{"--ipv6=false", "com.docker.network.bridge.name=" + n.bridge, "com.docker.network.bridge.enable_icc=false", "reliant.run-id=run-1", n.network} {
		if !slices.Contains(net, want) {
			t.Errorf("network create lacks %q: %q", want, net)
		}
	}
	if v := volumeCreateArgs("run-1", n.work, "work", 0); slices.Contains(v, "--opt") {
		t.Errorf("unlimited volume carries an option: %q", v)
	}
	if v := volumeCreateArgs("run-1", n.work, "work", 1024); !slices.Contains(v, "size=1024") || !slices.Contains(v, "reliant.run-id=run-1") {
		t.Errorf("limited volume: %q", v)
	}
}

// The probe reads host netfilter, so it runs the machine's own image, never
// the gate's, and never under sysbox.
func TestProbeArgs(t *testing.T) {
	args := probeArgs("run-1", "probe@sha256:abc")
	for _, want := range []string{"--rm", "runc", "--network", "host", "NET_ADMIN", "--read-only", "probe@sha256:abc", "reliant.run-id=run-1"} {
		if !slices.Contains(args, want) {
			t.Errorf("probe args lack %q: %q", want, args)
		}
	}
	v4, v6 := splitDumps(dumpMarker + "iptables iptables-nft-save\nA\n" + dumpMarker + "ip6tables ip6tables-save\nB\n" + dumpMarker + "iptables iptables-legacy-save\nC\n")
	if !slices.Equal(v4, []string{"A\n", "C\n"}) || !slices.Equal(v6, []string{"B\n"}) {
		t.Errorf("splitDumps = %q, %q", v4, v6)
	}
}

func TestRunSequenceAndVerdicts(t *testing.T) {
	for _, c := range []struct {
		state containerState
		want  Class
	}{
		{containerState{Status: "exited", ExitCode: 0}, ClassPass},
		{containerState{Status: "exited", ExitCode: 1}, ClassFail},
		{containerState{Status: "exited", ExitCode: 2}, ClassUnproven},
		{containerState{Status: "exited", ExitCode: 137, OOMKilled: true}, ClassUnproven},
	} {
		f := newFake(t)
		f.state = c.state
		res, err := f.backend(SysboxConfig{}).Run(context.Background(), testSpec(t))
		if err != nil {
			t.Fatalf("exit %d: Run: %v", c.state.ExitCode, err)
		}
		if res.Class != c.want || res.ExitCode != c.state.ExitCode || res.OOMKilled != c.state.OOMKilled {
			t.Errorf("exit %d oom %v: result %+v, want class %s", c.state.ExitCode, c.state.OOMKilled, res, c.want)
		}
		var verbs []string
		for _, cmd := range f.commands() {
			v := cmd[0]
			if cmd[0] == "volume" || cmd[0] == "network" || cmd[0] == "container" || cmd[0] == "image" || cmd[0] == "start" {
				v += " " + cmd[1]
			}
			verbs = append(verbs, v)
		}
		want := []string{"info", "info", "run", "network create", "volume create", "volume create", "create", "container inspect", "image inspect", "container inspect", "cp", "start --attach", "container inspect"}
		if !slices.Equal(verbs, want) {
			t.Errorf("command sequence = %q, want %q", verbs, want)
		}
	}
}

func TestRunRefusesWithoutSysbox(t *testing.T) {
	f := newFake(t)
	f.runtimes = `{"runc":{"path":"runc"}}`
	_, err := f.backend(SysboxConfig{}).Run(context.Background(), testSpec(t))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Run without sysbox = %v, want ErrUnavailable", err)
	}
	if f.ran("create") || f.ran("network", "create") {
		t.Error("a refused gate created resources")
	}
	if a := f.backend(SysboxConfig{}).Detect(context.Background()); a.Available || !strings.Contains(a.Reason, "runc") {
		t.Errorf("Detect = %+v", a)
	}
}

func TestRunRefusesWithoutEgressRules(t *testing.T) {
	cases := map[string]func(*fakeDocker, *Spec, *SysboxConfig){
		"rules absent":     func(f *fakeDocker, _ *Spec, _ *SysboxConfig) { f.v4 = "*filter\nCOMMIT\n" },
		"no probe image":   func(f *fakeDocker, _ *Spec, c *SysboxConfig) { c.ProbeImage = "" },
		"undeclared allow": func(f *fakeDocker, s *Spec, _ *SysboxConfig) { s.EgressAllow = nil },
		"unprovided allow": func(f *fakeDocker, s *Spec, _ *SysboxConfig) {
			s.EgressAllow = mustPrefixes("10.20.0.0/16", "192.168.5.0/24")
		},
	}
	for name, mutate := range cases {
		f := newFake(t)
		spec := testSpec(t)
		cfg := SysboxConfig{ProbeImage: "probe"}
		mutate(f, &spec, &cfg)
		s := NewSysbox(cfg)
		s.exec = f.exec
		_, err := s.Run(context.Background(), spec)
		if !errors.Is(err, ErrEgressNotEnforced) {
			t.Errorf("%s: Run = %v, want ErrEgressNotEnforced", name, err)
		}
		if f.ran("create") {
			t.Errorf("%s: a refused gate created its container", name)
		}
	}
}

func TestRunRefusesInjectedEnv(t *testing.T) {
	f := newFake(t)
	f.containerEnv = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HTTPS_PROXY=http://user:pw@proxy:3128"}
	_, err := f.backend(SysboxConfig{}).Run(context.Background(), testSpec(t))
	if !errors.Is(err, ErrEnvInjected) || !strings.Contains(err.Error(), "HTTPS_PROXY") {
		t.Fatalf("Run = %v, want ErrEnvInjected naming HTTPS_PROXY", err)
	}
	if f.ran("start") {
		t.Error("a gate with an injected variable was started")
	}
}

func TestCheckEnvAllowsImageEnv(t *testing.T) {
	if err := checkEnv([]string{"A=1", "IMG=x"}, []string{"IMG=x"}, []string{"A=1"}); err != nil {
		t.Errorf("image and gate env: %v", err)
	}
	if err := checkEnv([]string{"IMG=changed"}, []string{"IMG=x"}, nil); !errors.Is(err, ErrEnvInjected) {
		t.Errorf("a changed image value: %v", err)
	}
}

func TestRunWallTimeIsUnprovenAndKills(t *testing.T) {
	f := newFake(t)
	f.state = containerState{Status: "exited", ExitCode: 137}
	f.attach = func(ctx context.Context) error {
		select {
		case <-f.killed:
			return exitStatus(137)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	spec := testSpec(t)
	spec.Limits.Wall = 20 * time.Millisecond
	res, err := f.backend(SysboxConfig{}).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Class != ClassUnproven || !res.TimedOut || !strings.Contains(res.Reason, "wall-time") {
		t.Errorf("result %+v, want unproven by wall time", res)
	}
	if !f.ran("kill", namesFor(spec.RunID).container) {
		t.Error("the gate was not killed")
	}
}

func TestRunCanceledKillsAndReturnsError(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.attach = func(context.Context) error {
		cancel()
		<-f.killed
		return nil
	}
	res, err := f.backend(SysboxConfig{}).Run(ctx, testSpec(t))
	if !errors.Is(err, context.Canceled) || res.Class != ClassUnproven {
		t.Fatalf("Run = %+v, %v; want unproven and context.Canceled", res, err)
	}
}

// A lost connection to the engine is never a verdict: only an exit the
// attach itself reported, from a container in a terminal exited state, is.
func TestRunLostConnectionIsUnproven(t *testing.T) {
	lost := errors.New("error during connect: unexpected EOF")
	cases := []struct {
		name   string
		state  containerState
		attach error
		want   Class
		killed bool
	}{
		{"clean exit 0", containerState{Status: "exited"}, nil, ClassPass, false},
		{"exit 1, reported by the attach", containerState{Status: "exited", ExitCode: 1}, exitStatus(1), ClassFail, false},
		{"attach lost, gate still running", containerState{Status: "running"}, lost, ClassUnproven, true},
		{"attach lost, gate exited 0", containerState{Status: "exited"}, lost, ClassUnproven, false},
		{"attach lost with its own exit code, gate exited 0", containerState{Status: "exited"}, exitStatus(1), ClassUnproven, false},
		{"attach lost, gate exited 1", containerState{Status: "exited", ExitCode: 1}, lost, ClassUnproven, false},
		{"attach reports another code than the gate's", containerState{Status: "exited", ExitCode: 3}, exitStatus(1), ClassUnproven, false},
		{"attach ended cleanly, gate exited 1", containerState{Status: "exited", ExitCode: 1}, nil, ClassUnproven, false},
		{"attach ended cleanly, gate still running", containerState{Status: "running"}, nil, ClassUnproven, true},
		{"gate paused", containerState{Status: "paused"}, lost, ClassUnproven, true},
		{"gate dead", containerState{Status: "dead", ExitCode: 0}, lost, ClassUnproven, true},
	}
	for _, c := range cases {
		f := newFake(t)
		f.state = c.state
		attachErr := c.attach
		f.attach = func(context.Context) error { return attachErr }
		spec := testSpec(t)
		res, err := f.backend(SysboxConfig{}).Run(context.Background(), spec)
		if err != nil {
			t.Fatalf("%s: Run: %v", c.name, err)
		}
		if res.Class != c.want {
			t.Errorf("%s: class = %s (%+v), want %s", c.name, res.Class, res, c.want)
		}
		if res.Class == ClassUnproven && res.Reason == "" {
			t.Errorf("%s: unproven with no reason", c.name)
		}
		if c.state.Status != "exited" && res.ExitCode != -1 {
			t.Errorf("%s: exit code %d read from a gate that never exited", c.name, res.ExitCode)
		}
		if got := f.ran("kill", namesFor(spec.RunID).container); got != c.killed {
			t.Errorf("%s: killed = %v, want %v", c.name, got, c.killed)
		}
	}
}

// ext4, or xfs without project quotas, cannot bound a volume: the gate is
// refused before anything runs.
func TestRunRefusesUnenforceableDiskLimit(t *testing.T) {
	f := newFake(t)
	f.sizeFails = true
	_, err := f.backend(SysboxConfig{}).Run(context.Background(), testSpec(t))
	if !errors.Is(err, ErrDiskLimitNotEnforced) {
		t.Fatalf("Run = %v, want ErrDiskLimitNotEnforced", err)
	}
	if f.ran("create") || f.ran("start") {
		t.Error("a gate whose disk cannot be bounded was created")
	}
	for _, c := range f.commands() {
		if c[0] == "volume" && c[1] == "create" && !slices.Contains(c, "size="+strconv.FormatInt(testSpec(t).Limits.DiskBytes, 10)) {
			t.Errorf("an unlimited volume was created: %q", c)
		}
	}
}

func slotsDir(t *testing.T, n int) string {
	// Disk slots are Linux-only (disk_other.go refuses them); their tests
	// skip elsewhere, and disk_other_test.go tests the refusal (review B2 on
	// #3793).
	if runtime.GOOS != "linux" {
		t.Skip("disk slots need Linux")
	}
	dir := t.TempDir()
	for i := range n {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("slot-%d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunOnDiskSlot(t *testing.T) {
	f := newFake(t)
	f.slotSize = 10 << 30
	dir := slotsDir(t, 1)
	s := f.backend(SysboxConfig{DiskSlotsDir: dir})
	spec := testSpec(t)
	spec.Limits.DiskBytes = 10 << 30
	if _, err := s.Run(context.Background(), spec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	slot := filepath.Join(dir, "slot-0")
	if want := []string{"type=bind,src=" + slot + ",dst=/slot"}; !slices.Equal(f.wiped, want) {
		t.Errorf("wipes = %q, want the slot alone mounted: %q", f.wiped, want)
	}
	for _, role := range []string{"work", "engine"} {
		if !f.ran("volume", "create", "--driver", "local", "--label", LabelRunID+"="+spec.RunID) {
			t.Errorf("no labelled local volume")
		}
		found := false
		for _, c := range f.commands() {
			found = found || (c[0] == "volume" && slices.Contains(c, "device="+filepath.Join(slot, role)) && slices.Contains(c, "o=bind"))
		}
		if !found {
			t.Errorf("the %s volume is not a bind volume on the slot: %q", role, f.commands())
		}
	}
	// The slot is held: a second gate finds none free.
	other := testSpec(t)
	other.RunID = "run-2"
	if _, err := s.Run(context.Background(), other); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("a second gate on a held slot: %v", err)
	}
	// Teardown wipes and frees it.
	if err := s.Teardown(context.Background(), spec.RunID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(f.wiped) != 2 {
		t.Errorf("teardown did not wipe the slot: %q", f.wiped)
	}
	if _, err := s.Run(context.Background(), other); err != nil {
		t.Errorf("the freed slot: %v", err)
	}
}

func TestRunRefusesUnboundingDiskSlots(t *testing.T) {
	f := newFake(t)
	f.slotSize = 100 << 30
	spec := testSpec(t)
	spec.Limits.DiskBytes = 10 << 30
	_, err := f.backend(SysboxConfig{DiskSlotsDir: slotsDir(t, 1)}).Run(context.Background(), spec)
	if !errors.Is(err, ErrDiskLimitNotEnforced) || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a slot larger than the limit: %v", err)
	}
	f = newFake(t)
	_, err = f.backend(SysboxConfig{DiskSlotsDir: slotsDir(t, 0)}).Run(context.Background(), testSpec(t))
	if !errors.Is(err, ErrDiskLimitNotEnforced) {
		t.Errorf("no slots: %v", err)
	}
	if f.ran("create") {
		t.Error("a refused gate created its container")
	}
}

// slotGate runs a gate on a one-slot machine and returns the backend, the
// slot and the spec.
func slotGate(t *testing.T, f *fakeDocker, dir string) (*Sysbox, string, Spec) {
	t.Helper()
	f.slotSize = 10 << 30
	s := f.backend(SysboxConfig{DiskSlotsDir: dir})
	spec := testSpec(t)
	spec.Limits.DiskBytes = 10 << 30
	if _, err := s.Run(context.Background(), spec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	slot := filepath.Join(dir, "slot-0")
	if owner, err := readOwner(slot); err != nil || owner != spec.RunID {
		t.Fatalf("the claimed slot's owner record = %q, %v; want %q", owner, err, spec.RunID)
	}
	return s, slot, spec
}

// crash drops what this process holds, as its death would: the slot's lock
// goes, while its containers and the records beside the slot stay.
func crash(s *Sysbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, slot := range s.slots {
		_ = slot.lock.Close()
		delete(s.slots, id)
	}
}

func (f *fakeDocker) wipes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.wiped)
}

// A teardown that cannot remove the gate's containers neither wipes nor
// frees its slot: it quarantines it, and only a teardown of the same run
// that confirms the containers gone releases it.
func TestTeardownQuarantinesSlotWhenContainersSurvive(t *testing.T) {
	f := newFake(t)
	s, slot, spec := slotGate(t, f, slotsDir(t, 1))
	f.listed["ps"] = "gate-container\n"
	f.rmFails = true
	err := s.Teardown(context.Background(), spec.RunID)
	if !errors.Is(err, ErrDiskSlotQuarantined) {
		t.Fatalf("Teardown = %v, want ErrDiskSlotQuarantined", err)
	}
	if f.wipes() != 1 {
		t.Errorf("a slot whose containers survive was wiped: %q", f.wiped)
	}
	marker, rerr := os.ReadFile(quarantinePath(slot))
	if rerr != nil || !strings.Contains(string(marker), spec.RunID) {
		t.Errorf("quarantine marker = %q, %v", marker, rerr)
	}
	other := testSpec(t)
	other.RunID = "run-2"
	other.Limits.DiskBytes = 10 << 30
	if _, err := s.Run(context.Background(), other); !errors.Is(err, errNoFreeSlot) {
		t.Errorf("a gate claimed a quarantined slot: %v", err)
	}
	// A retry that removes them releases it.
	f.rmFails = false
	if err := s.Teardown(context.Background(), spec.RunID); err != nil {
		t.Fatalf("retried Teardown: %v", err)
	}
	if f.wipes() != 2 {
		t.Errorf("the released slot was not wiped: %q", f.wiped)
	}
	for _, p := range []string{quarantinePath(slot), ownerPath(slot)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is left after a clean teardown: %v", p, err)
		}
	}
	f.listed["ps"] = ""
	if _, err := s.Run(context.Background(), other); err != nil {
		t.Errorf("the released slot: %v", err)
	}
}

// The quarantine is on disk: a restarted daemon honours it, whatever the
// engine now says, until a teardown of that run confirms its containers gone.
func TestQuarantinedSlotSurvivesARestart(t *testing.T) {
	f := newFake(t)
	dir := slotsDir(t, 1)
	s, slot, spec := slotGate(t, f, dir)
	f.psFails = true
	if err := s.Teardown(context.Background(), spec.RunID); !errors.Is(err, ErrDiskSlotQuarantined) {
		t.Fatalf("Teardown with an unlistable engine = %v, want ErrDiskSlotQuarantined", err)
	}
	crash(s)
	f.psFails = false

	restarted := f.backend(SysboxConfig{DiskSlotsDir: dir})
	other := testSpec(t)
	other.RunID = "run-2"
	other.Limits.DiskBytes = 10 << 30
	_, err := restarted.Run(context.Background(), other)
	if !errors.Is(err, errNoFreeSlot) || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("a restarted daemon's claim of a quarantined slot = %v", err)
	}
	if f.wipes() != 1 {
		t.Errorf("the quarantined slot was wiped: %q", f.wiped)
	}
	// The restarted daemon's teardown of the old run finds the slot by its
	// owner record and, with the containers confirmed gone, releases it.
	if err := restarted.Teardown(context.Background(), spec.RunID); err != nil {
		t.Fatalf("Teardown after the restart: %v", err)
	}
	if _, err := os.Stat(quarantinePath(slot)); !os.IsNotExist(err) {
		t.Errorf("the quarantine marker is left: %v", err)
	}
	if _, err := restarted.Run(context.Background(), other); err != nil {
		t.Errorf("the released slot: %v", err)
	}
}

// After a daemon restart the slot is unlocked, but the gate's container may
// still be running on it: the claim confirms through the engine that the
// previous owner's containers are gone before it wipes anything.
func TestClaimReconcilesAPreviousOwner(t *testing.T) {
	f := newFake(t)
	dir := slotsDir(t, 1)
	s, slot, spec := slotGate(t, f, dir)
	crash(s)

	restarted := f.backend(SysboxConfig{DiskSlotsDir: dir})
	other := testSpec(t)
	other.RunID = "run-2"
	other.Limits.DiskBytes = 10 << 30
	claim := func() error {
		t.Helper()
		_, err := restarted.Run(context.Background(), other)
		return err
	}

	f.containersOf[spec.RunID] = "surviving-gate\n"
	if err := claim(); !errors.Is(err, errNoFreeSlot) || !strings.Contains(err.Error(), "still has containers") {
		t.Errorf("claim with the previous gate still running = %v", err)
	}
	f.psFails = true
	if err := claim(); !errors.Is(err, errNoFreeSlot) || !strings.Contains(err.Error(), "cannot confirm") {
		t.Errorf("claim with an engine that cannot list = %v", err)
	}
	f.psFails = false
	if f.wipes() != 1 {
		t.Fatalf("a slot whose previous gate may survive was wiped: %q", f.wiped)
	}
	if owner, _ := readOwner(slot); owner != spec.RunID {
		t.Errorf("a skipped slot's owner record changed to %q", owner)
	}

	// The stale owner's containers are gone: the slot is reclaimed.
	f.containersOf[spec.RunID] = ""
	if err := claim(); err != nil {
		t.Fatalf("claim of a stale owner's slot: %v", err)
	}
	if f.wipes() != 2 {
		t.Errorf("the reclaimed slot was not wiped: %q", f.wiped)
	}
	if owner, _ := readOwner(slot); owner != other.RunID {
		t.Errorf("the reclaimed slot's owner = %q, want %q", owner, other.RunID)
	}
}

// An owner record that is not a run id never becomes a label filter.
func TestClaimSkipsAnUnreadableOwnerRecord(t *testing.T) {
	f := newFake(t)
	f.slotSize = 10 << 30
	dir := slotsDir(t, 1)
	if err := os.WriteFile(ownerPath(filepath.Join(dir, "slot-0")), []byte("x,label=other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t)
	spec.Limits.DiskBytes = 10 << 30
	_, err := f.backend(SysboxConfig{DiskSlotsDir: dir}).Run(context.Background(), spec)
	if !errors.Is(err, errNoFreeSlot) || !strings.Contains(err.Error(), "not a run id") {
		t.Errorf("claim over a malformed owner record = %v", err)
	}
	if f.ran("ps") || f.wipes() != 0 {
		t.Errorf("a malformed owner record was used: %q", f.commands())
	}
}

// A slot that is only a directory on its parent's filesystem bounds nothing.
func TestSlotSizeNeedsItsOwnFilesystem(t *testing.T) {
	dir := slotsDir(t, 1)
	if _, err := slotSize(dir, filepath.Join(dir, "slot-0")); err == nil {
		t.Error("a plain directory passed as a disk slot")
	}
}

func TestRunTarSourceLandsUnderTree(t *testing.T) {
	var src bytes.Buffer
	tw := tar.NewWriter(&src)
	_ = tw.WriteHeader(&tar.Header{Name: "go.mod", Mode: 0o644, Size: 6, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("module"))
	_ = tw.WriteHeader(&tar.Header{Name: "link", Linkname: "go.mod", Typeflag: tar.TypeLink})
	_ = tw.Close()
	f := newFake(t)
	spec := testSpec(t)
	spec.SourceDir, spec.SourceTar = "", &src
	if _, err := f.backend(SysboxConfig{}).Run(context.Background(), spec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !f.ran("cp", "-", namesFor(spec.RunID).container+":"+WorkMount) {
		t.Errorf("tar not copied to %s: %q", WorkMount, f.commands())
	}
	tr := tar.NewReader(&f.copiedTar)
	var got []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		got = append(got, h.Name+"|"+h.Linkname)
	}
	if want := []string{"src/|", "src/go.mod|", "src/link|src/go.mod"}; !slices.Equal(got, want) {
		t.Errorf("copied entries = %q, want %q", got, want)
	}
}

func TestPrefixTarRefusesEscapes(t *testing.T) {
	for _, h := range []tar.Header{
		{Name: "../evil", Typeflag: tar.TypeReg},
		{Name: "/etc/passwd", Typeflag: tar.TypeReg},
		{Name: "a/../../evil", Typeflag: tar.TypeReg},
		{Name: "ok", Linkname: "../../etc/shadow", Typeflag: tar.TypeLink},
	} {
		var src bytes.Buffer
		tw := tar.NewWriter(&src)
		_ = tw.WriteHeader(&h)
		_ = tw.Close()
		if err := prefixTar(&src, io.Discard, "src"); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%q -> %q: prefixTar = %v, want ErrInvalidSpec", h.Name, h.Linkname, err)
		}
	}
}

func TestTeardownByLabelAndIdempotent(t *testing.T) {
	f := newFake(t)
	s := f.backend(SysboxConfig{})
	if err := s.Teardown(context.Background(), "run-1"); err != nil {
		t.Fatalf("Teardown with nothing left: %v", err)
	}
	for _, c := range f.commands() {
		if c[len(c)-1] != "label=reliant.run-id=run-1" {
			t.Errorf("a teardown with nothing left ran %q", c)
		}
	}
	f = newFake(t)
	f.listed = map[string]string{"ps": "c1\nc2\n", "volume": "v1\nv2\n", "network": "n1\n"}
	s = f.backend(SysboxConfig{})
	if err := s.Teardown(context.Background(), "run-1"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	var rms [][]string
	for _, c := range f.commands() {
		if slices.Contains(c, "rm") {
			rms = append(rms, c)
		}
	}
	want := [][]string{{"rm", "-f", "-v", "c1", "c2"}, {"volume", "rm", "-f", "v1", "v2"}, {"network", "rm", "n1"}}
	if !slices.EqualFunc(rms, want, slices.Equal[[]string]) {
		t.Errorf("removals = %q, want %q (containers first)", rms, want)
	}
	for _, bad := range []string{"", "x/y", "Run"} {
		if err := s.Teardown(context.Background(), bad); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("Teardown(%q) = %v: an odd id must never become a label filter", bad, err)
		}
	}
}

func TestCopyOutCapsBytes(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1000)
	s := NewSysbox(SysboxConfig{})
	s.exec = func(ctx context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
		if !slices.Equal(args, []string{"cp", "reliant-gate-run-1:/gate/src/.reliant", "-"}) {
			t.Errorf("args = %q", args)
		}
		for range 10 {
			_, _ = stdout.Write(big)
		}
		return nil
	}
	var out bytes.Buffer
	if err := s.CopyOut(context.Background(), "run-1", "/gate/src/.reliant", &out, 2500); err == nil {
		t.Error("CopyOut over its cap succeeded")
	}
	if out.Len() > 2500 {
		t.Errorf("CopyOut wrote %d bytes past a 2500 cap", out.Len())
	}
	out.Reset()
	if err := s.CopyOut(context.Background(), "run-1", "/gate/src/.reliant", &out, 1<<20); err != nil || out.Len() != 10000 {
		t.Errorf("CopyOut under its cap: %v, %d bytes", err, out.Len())
	}
	if err := s.CopyOut(context.Background(), "run-1", "relative", &out, 1); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("relative path: %v", err)
	}
}

func TestPreflightReport(t *testing.T) {
	f := newFake(t)
	f.sizeFails = true
	r, err := f.backend(SysboxConfig{}).Preflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Availability.Available || r.Egress != nil || r.DiskLimit || r.DiskLimitReason == "" {
		t.Errorf("report %+v", r)
	}
	if !slices.Equal(r.EgressAllow, []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}) {
		t.Errorf("allow = %v", r.EgressAllow)
	}
	if err := r.Ready(); !errors.Is(err, ErrDiskLimitNotEnforced) {
		t.Errorf("Ready without quota support = %v, want ErrDiskLimitNotEnforced", err)
	}
	f.sizeFails = false
	if r, _ := f.backend(SysboxConfig{}).Preflight(context.Background()); r.Ready() != nil {
		t.Errorf("Ready with quota support = %v", r.Ready())
	}
	// Everything the probe created is torn down by its label.
	if !f.ran("ps", "-aq", "--filter") {
		t.Error("preflight left its probe resources")
	}
}

func TestCheckFirewallBackend(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "null": true, `{"Driver":"iptables"}`: true, `{"Driver":"nftables"}`: false} {
		if err := checkFirewallBackend(in); (err == nil) != ok {
			t.Errorf("checkFirewallBackend(%q) = %v", in, err)
		}
	}
}

func TestBootstrapScript(t *testing.T) {
	s := bootstrapScript(45 * time.Second)
	for _, want := range []string{"dockerd --data-root=/var/lib/docker", "-ge 45 ]", "exit 2", "cd /gate/src", `exec "$@"`} {
		if !strings.Contains(s, want) {
			t.Errorf("bootstrap lacks %q:\n%s", want, s)
		}
	}
}

// The root layer is read-only and every writable path is bounded (review B1
// on #3793).
func TestCreateArgsBoundEveryWritablePath(t *testing.T) {
	spec := validSpec()
	spec.Limits.DiskBytes = 10 << 30
	env, err := GateEnv(spec.RunID, spec.Env)
	if err != nil {
		t.Fatal(err)
	}
	args := createArgs(spec, namesFor(spec.RunID), env, SysboxConfig{EngineWait: time.Minute})
	if !slices.Contains(args, "--read-only") {
		t.Errorf("the gate's root layer is writable: %q", args)
	}
	var tmpfs []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tmpfs" {
			tmpfs = append(tmpfs, args[i+1])
		}
	}
	if len(tmpfs) != 3 {
		t.Fatalf("tmpfs mounts = %q, want /run, /var/log and /tmp", tmpfs)
	}
	for _, m := range tmpfs {
		if !strings.Contains(m, ",size=") {
			t.Errorf("tmpfs %q has no size cap", m)
		}
	}
}

// An image-declared VOLUME becomes an anonymous volume nothing bounds: the
// gate is refused before it starts.
func TestRunRefusesAnUnboundedMount(t *testing.T) {
	f := newFake(t)
	f.mounts = []map[string]string{
		{"Type": "volume", "Destination": WorkMount},
		{"Type": "volume", "Destination": EngineDataRoot},
		{"Type": "volume", "Destination": "/data"},
	}
	_, err := f.backend(SysboxConfig{}).Run(context.Background(), testSpec(t))
	if !errors.Is(err, ErrUnboundedMount) || !strings.Contains(err.Error(), "/data") {
		t.Fatalf("Run = %v, want ErrUnboundedMount naming /data", err)
	}
	if f.ran("start", "--attach") {
		t.Error("the gate started despite an unbounded mount")
	}
}
