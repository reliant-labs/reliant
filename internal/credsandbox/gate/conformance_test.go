// Copyright (c) 2025 Reliant Labs
package gate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The conformance table is what every Backend must pass on a machine that can
// run it: the gate exit contract, and the isolation boundary itself (pipeline
// redesign §3.3, the denial tests of the pipeline build plan's E9d, folded in
// here). It is backend-neutral: the sysbox backend runs it below, a vm backend
// will run the same table through runConformance.
//
// Every denial case has something real to be denied. Before any gate runs,
// the harness plants on the host (hostFixture): a credential file holding a
// secret, a variable only this process's environment holds, HTTP listeners
// on every host address and on loopback, and a private-range peer (a
// container on the host engine's own bridge) — and checks the host itself
// reaches each, so a denial means the boundary held, not that nothing was
// there. The positive controls (the nested engine runs a container, public
// egress works) keep the boundary from passing by breaking the gate.
//
// TestDenialCasesCatchALeakyBoundary runs the denial cases against a
// deliberately leaky backend (plain runc with the host's PID namespace,
// engine socket and credential directory), and each must fail there: the
// table detects the leaks it exists for.
type conformanceCase struct {
	name string
	// denial marks a boundary case: against a leaky boundary it must fail.
	denial bool
	// needs says why the case cannot run on this host ("" when it can).
	needs  func(f *hostFixture) string
	script string // run as `sh -c script`, with the fixture's GATE_T_* environment
	limits func(*Limits)
	check  func(f *hostFixture, res Result, out string) error
}

func conformanceCases() []conformanceCase {
	wantClass := func(c Class) func(*hostFixture, Result, string) error {
		return func(_ *hostFixture, res Result, _ string) error {
			if res.Class != c {
				return fmt.Errorf("class = %s (%+v), want %s", res.Class, res, c)
			}
			return nil
		}
	}
	// lacks fails when out holds any of secrets: what the boundary must keep
	// from the gate.
	lacks := func(out string, secrets ...string) error {
		for _, s := range secrets {
			if s != "" && strings.Contains(out, s) {
				return fmt.Errorf("the gate's output holds %q, which only the host has:\n%s", s, out)
			}
		}
		return nil
	}
	ran := func(res Result, out string) error {
		if res.Class != ClassPass {
			return fmt.Errorf("the probe itself did not run to its end (%+v):\n%s", res, out)
		}
		return nil
	}
	// reach fetches each URL in $URLS from the gate and from a container of
	// the nested engine, printing what answered. A listener answers with the
	// planted secret, so an answer is in the output only if it was reached.
	const reach = `export URLS
fetch='for u in $URLS; do wget -q -T 3 -O - --header "Metadata-Flavor: Google" "$u" 2>/dev/null && echo " <- $u"; done; true'
sh -c "$fetch"
docker run --rm -e URLS="$URLS" "$GATE_T_NESTED" sh -c "$fetch" 2>&1
true`
	// canary prints the canary's line from each environ file given, and
	// nothing else of it: a leaky boundary must not copy the host's real
	// environments into test logs.
	const canary = `canary() { for e in "$@"; do tr '\0' '\n' <"$e" 2>/dev/null | grep "^$GATE_T_CANARY_NAME="; done; true; }
`
	return []conformanceCase{
		{name: "exit 0 passes", script: "exit 0", check: wantClass(ClassPass)},
		{name: "exit 1 fails", script: "exit 1", check: wantClass(ClassFail)},
		{name: "exit 2 is unproven", script: "exit 2", check: wantClass(ClassUnproven)},
		{
			name:   "runs in the tree with the nested engine up",
			script: "test -f marker && pwd && docker info --format '{{.ServerVersion}}'",
			check: func(_ *hostFixture, res Result, out string) error {
				if res.Class != ClassPass || !strings.Contains(out, TreeDir) {
					return fmt.Errorf("result %+v, output %q", res, out)
				}
				return nil
			},
		},
		{
			name:   "positive control: the nested engine runs a container",
			script: `docker run --rm "$GATE_T_NESTED" echo nested-container-ran`,
			check: func(_ *hostFixture, res Result, out string) error {
				if res.Class != ClassPass || !strings.Contains(out, "nested-container-ran") {
					return fmt.Errorf("result %+v, output %q", res, out)
				}
				return nil
			},
		},
		{
			name:   "positive control: public egress works",
			needs:  func(f *hostFixture) string { return f.noPublic },
			script: `wget -q -T 10 -O /dev/null "$GATE_T_PUBLIC" && echo public-reached`,
			check: func(_ *hostFixture, res Result, out string) error {
				if !strings.Contains(out, "public-reached") {
					return fmt.Errorf("the gate cannot reach %s, which the host can (%+v):\n%s", "the public URL", res, out)
				}
				return nil
			},
		},
		{
			name:   "a planted host credential file is unreadable",
			denial: true,
			script: `echo "HOME=$HOME"
cat "$GATE_T_CRED"
for p in .reliant .ssh .docker/config.json .config/gh .kube/config .git-credentials; do
	test -e "$GATE_T_HOSTHOME/$p" && echo "host file visible: $p"
done
true`,
			check: func(f *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				if !strings.Contains(out, "HOME="+HomeDir) {
					return fmt.Errorf("the gate's HOME is not its own:\n%s", out)
				}
				if strings.Contains(out, "host file visible") {
					return fmt.Errorf("the host's credential paths are visible:\n%s", out)
				}
				return lacks(out, f.secret)
			},
		},
		{
			name:   "the host's /proc is invisible",
			denial: true,
			script: canary + `canary "/proc/$GATE_T_HOSTPID/environ" /proc/[0-9]*/environ`,
			check: func(f *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				return lacks(out, f.canary)
			},
		},
		{
			name:   "docker run -v /:/host reaches only the gate's root",
			denial: true,
			script: `docker run --rm -v /:/host -e GATE_T_CRED -e GATE_T_HOSTPID -e GATE_T_CANARY_NAME "$GATE_T_NESTED" sh -c '
test -f /host/gate/src/marker && echo nested-saw-the-gate-root
cat "/host$GATE_T_CRED"
tr "\0" "\n" <"/host/proc/$GATE_T_HOSTPID/environ" | grep "^$GATE_T_CANARY_NAME="
true' 2>&1`,
			check: func(f *hostFixture, res Result, out string) error {
				if err := lacks(out, f.secret, f.canary); err != nil {
					return err
				}
				if !strings.Contains(out, "nested-saw-the-gate-root") {
					return fmt.Errorf("the nested container's / was not the gate's root (%+v):\n%s", res, out)
				}
				return nil
			},
		},
		{
			name:   "host listeners are unreachable, loopback included",
			denial: true,
			script: `gw=$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')
URLS="$GATE_T_HOST_URLS http://$gw:$GATE_T_HOST_PORT/"
` + reach,
			check: func(f *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				return lacks(out, f.secret)
			},
		},
		{
			name:   "private-range and metadata egress are blocked",
			denial: true,
			needs:  func(f *hostFixture) string { return f.noPeer },
			script: `URLS="$GATE_T_PEER http://169.254.169.254/"
` + reach,
			check: func(f *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				if strings.Contains(out, "<- http://169.254.169.254") {
					return fmt.Errorf("the gate reached the metadata endpoint:\n%s", out)
				}
				return lacks(out, f.secret)
			},
		},
		{
			name:   "IPv6 has no address or route out",
			denial: true,
			script: `echo "disable_ipv6=$(cat /proc/sys/net/ipv6/conf/eth0/disable_ipv6 2>/dev/null)"
echo "global-v6: $(ip -6 addr show scope global 2>/dev/null | grep -c inet6)"
echo "default-v6: $(ip -6 route show default 2>/dev/null | grep -c default)"`,
			check: func(_ *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				if strings.Contains(out, "disable_ipv6=1") || (strings.Contains(out, "global-v6: 0") && strings.Contains(out, "default-v6: 0")) {
					return nil
				}
				return fmt.Errorf("the gate has IPv6 out:\n%s", out)
			},
		},
		{
			name:   "no variable from the daemon",
			denial: true,
			script: canary + `env; canary /proc/1/environ`,
			check: func(f *hostFixture, res Result, out string) error {
				if err := ran(res, out); err != nil {
					return err
				}
				if !strings.Contains(out, "RELIANT_GATE=1") {
					return fmt.Errorf("RELIANT_GATE=1 is missing:\n%s", out)
				}
				return lacks(out, f.canary)
			},
		},
		{
			name:   "an exceeded wall time is unproven",
			script: "sleep 600",
			limits: func(l *Limits) { l.Wall = 5 * time.Second },
			check: func(_ *hostFixture, res Result, _ string) error {
				if res.Class != ClassUnproven || !res.TimedOut {
					return fmt.Errorf("result %+v, want unproven by wall time", res)
				}
				return nil
			},
		},
	}
}

// hostFixture is what the harness plants on the host for the denial cases.
type hostFixture struct {
	secret   string // in the planted credential file, and served by every host listener and the peer
	canary   string // a value of this process's environment, and nowhere else
	credPath string
	hostURLs []string // host listeners the host itself reaches: loopback and every address
	hostPort int      // the all-addresses listener's port, for the gate's own gateway
	peerURL  string   // the private-range peer
	nested   string   // the image the nested engine runs
	public   string

	noPeer, noPublic string // why the peer or public egress is not available here
}

func (f *hostFixture) env() map[string]string {
	return map[string]string{
		"GATE_T_CRED":      f.credPath,
		"GATE_T_HOSTHOME":  os.Getenv("HOME"),
		"GATE_T_HOSTPID":   strconv.Itoa(os.Getpid()),
		"GATE_T_HOST_URLS": strings.Join(f.hostURLs, " "),
		"GATE_T_HOST_PORT": strconv.Itoa(f.hostPort),
		"GATE_T_PEER":      f.peerURL,
		"GATE_T_NESTED":    f.nested,
		"GATE_T_PUBLIC":    f.public,
		// The canary's name, never its value.
		"GATE_T_CANARY_NAME": conformanceCanaryVar,
	}
}

const conformanceCanaryVar = "RELIANT_GATE_CONFORMANCE_CANARY"

func randomToken(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// plantHost plants the fixture and proves the host reaches what it planted.
func plantHost(t *testing.T) *hostFixture {
	t.Helper()
	f := &hostFixture{
		secret: randomToken("reliant-planted-secret-"),
		canary: randomToken("reliant-daemon-canary-"),
		nested: os.Getenv("RELIANT_GATE_TEST_NESTED_IMAGE"),
		public: os.Getenv("RELIANT_GATE_TEST_PUBLIC_URL"),
	}
	if f.nested == "" {
		f.nested = "busybox:1.36"
	}
	if f.public == "" {
		f.public = "https://www.google.com/generate_204"
	}
	t.Setenv(conformanceCanaryVar, f.canary)

	dir := t.TempDir()
	f.credPath = filepath.Join(dir, "credentials")
	if err := os.WriteFile(f.credPath, []byte("token = "+f.secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	serve := func(network, addr string) int {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return 0
		}
		srv := &http.Server{
			Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, f.secret) }),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
		return ln.Addr().(*net.TCPAddr).Port
	}
	hostReaches := func(u string) bool {
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get(u)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(b), f.secret)
	}
	if p := serve("tcp4", "127.0.0.1:0"); p != 0 {
		f.hostURLs = append(f.hostURLs, fmt.Sprintf("http://127.0.0.1:%d/", p))
	}
	if p := serve("tcp6", "[::1]:0"); p != 0 {
		f.hostURLs = append(f.hostURLs, fmt.Sprintf("http://[::1]:%d/", p))
	}
	f.hostPort = serve("tcp", ":0")
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			p, err := netip.ParsePrefix(a.String())
			if err != nil || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			f.hostURLs = append(f.hostURLs, "http://"+netip.AddrPortFrom(p.Addr(), uint16(f.hostPort)).String()+"/")
		}
	}
	for _, u := range f.hostURLs {
		if !hostReaches(u) {
			t.Fatalf("positive control: the host does not reach its own planted listener %s", u)
		}
	}

	// The private-range peer: a container on the host engine's own bridge.
	f.noPeer = plantPeer(t, f, hostReaches)

	if !hostReaches204(f.public) {
		f.noPublic = "this host has no public egress to " + f.public
	}
	return f
}

func hostReaches204(u string) bool {
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 400
}

// plantPeer starts the private-range peer and returns why it could not
// ("" when it is up and the host reaches it).
func plantPeer(t *testing.T, f *hostFixture, hostReaches func(string) bool) string {
	t.Helper()
	id := randomToken("conformance-peer-")
	out, err := exec.Command("docker", "run", "-d", "--rm", "--runtime", "runc", "--label", LabelRunID+"="+id,
		f.nested, "sh", "-c", "mkdir /w && echo "+f.secret+" >/w/index.html && exec httpd -f -p 8080 -h /w").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("the host engine cannot run the private-range peer: %v: %s", err, bytes.TrimSpace(out))
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", strings.TrimSpace(string(out))).Run() })
	ipOut, err := exec.Command("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", strings.TrimSpace(string(out))).Output()
	if err != nil {
		return fmt.Sprintf("reading the peer's address: %v", err)
	}
	for _, ip := range strings.Fields(string(ipOut)) {
		a, err := netip.ParseAddr(ip)
		if err != nil || !a.IsPrivate() {
			continue
		}
		u := "http://" + ip + ":8080/"
		for range 20 {
			if hostReaches(u) {
				f.peerURL = u
				return ""
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return fmt.Sprintf("the host does not reach the private-range peer at %q", strings.TrimSpace(string(ipOut)))
}

func conformanceLimits() Limits {
	disk := int64(20 << 30)
	if v, err := strconv.ParseInt(os.Getenv("RELIANT_GATE_TEST_DISK_BYTES"), 10, 64); err == nil && v > 0 {
		disk = v
	}
	return Limits{CPUs: 2, MemoryBytes: 2 << 30, Pids: 2048, DiskBytes: disk, Wall: 3 * time.Minute}
}

// runCase runs one case against b, returning what it found wrong (nil for
// nothing) and a skip reason when it cannot run on this host.
func runCase(b Backend, f *hostFixture, image string, allow []netip.Prefix, src string, c conformanceCase) (failure error, skip string, err error) {
	if c.needs != nil {
		if why := c.needs(f); why != "" {
			return nil, why, nil
		}
	}
	spec := Spec{
		RunID:       randomToken("conformance-"),
		Image:       image,
		Command:     []string{"sh", "-c", c.script},
		SourceDir:   src,
		Env:         f.env(),
		EgressAllow: allow,
		Limits:      conformanceLimits(),
	}
	if c.limits != nil {
		c.limits(&spec.Limits)
	}
	var out bytes.Buffer
	spec.Stdout, spec.Stderr = &out, &out
	defer func() {
		if terr := b.Teardown(context.Background(), spec.RunID); terr != nil && err == nil {
			err = fmt.Errorf("teardown: %w", terr)
		}
	}()
	res, err := b.Run(context.Background(), spec)
	if err != nil {
		return nil, "", fmt.Errorf("Run: %w\n%s", err, out.String())
	}
	return c.check(f, res, out.String()), "", nil
}

func conformanceTree(t *testing.T) string {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// conformanceParallel bounds how many cases run at once: each is a gate.
const conformanceParallel = 4

// runConformance runs the whole table against b: every case must hold.
func runConformance(t *testing.T, b Backend, image string, allow []netip.Prefix) {
	f := plantHost(t)
	src := conformanceTree(t)
	sem := make(chan struct{}, conformanceParallel)
	for _, c := range conformanceCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sem <- struct{}{}
			defer func() { <-sem }()
			failure, skip, err := runCase(b, f, image, allow, src, c)
			switch {
			case err != nil:
				t.Fatal(err)
			case skip != "":
				t.Skip(skip)
			case failure != nil:
				t.Error(failure)
			}
		})
	}
}

// TestSysboxConformance runs the conformance table against real sysbox. It
// needs a machine gate-check has prepared: sysbox-runc registered, the egress
// rules provisioned (ProvisionScript), an enforceable disk limit (xfs project
// quotas, or RELIANT_GATE_TEST_DISK_SLOTS naming its disk slots, with
// RELIANT_GATE_TEST_DISK_BYTES at least a slot's size), and the images:
// RELIANT_GATE_TEST_IMAGE (default docker:27-dind) for the gate,
// RELIANT_GATE_TEST_PROBE_IMAGE (default the same) for the probe and slot
// wipes, RELIANT_GATE_TEST_NESTED_IMAGE (default busybox:1.36) for the
// nested containers and the private-range peer. Elsewhere it skips, naming
// what is missing.
func TestSysboxConformance(t *testing.T) {
	testkit.Slow(t, "starts sysbox containers, each running a nested dockerd")
	image := os.Getenv("RELIANT_GATE_TEST_IMAGE")
	if image == "" {
		image = "docker:27-dind"
	}
	probe := os.Getenv("RELIANT_GATE_TEST_PROBE_IMAGE")
	if probe == "" {
		probe = image
	}
	b := NewSysbox(SysboxConfig{ProbeImage: probe, DiskSlotsDir: os.Getenv("RELIANT_GATE_TEST_DISK_SLOTS")})
	ctx := context.Background()
	if a := b.Detect(ctx); !a.Available {
		t.Skipf("sysbox backend unavailable on this machine: %s", a.Reason)
	}
	r, err := b.Preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Ready(); err != nil {
		t.Skipf("this machine is not prepared for gates (run gate-check): %v", err)
	}
	runConformance(t, b, image, r.EgressAllow)
}

// leakyBackend is a deliberately broken boundary: a plain runc container in
// the host's network and PID namespaces (able to read other processes'
// environments), with the host's engine socket, the credential file's
// directory and this process's canary variable. It is everything cp#3631's review found a gate must not
// have, and the denial cases must catch each of it.
type leakyBackend struct {
	image   string
	credDir string
}

func (l leakyBackend) Name() string                              { return "leaky" }
func (l leakyBackend) Detect(context.Context) Availability       { return Availability{Available: true} }
func (l leakyBackend) Preflight(context.Context) (Report, error) { return Report{}, nil }
func (l leakyBackend) CopyOut(context.Context, string, string, io.Writer, int64) error {
	return errors.New("not supported")
}

func (l leakyBackend) Teardown(_ context.Context, runID string) error {
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+LabelRunID+"="+runID).Output()
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		return exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
	return nil
}

func (l leakyBackend) Run(ctx context.Context, spec Spec) (Result, error) {
	args := []string{"run", "--rm", "--runtime", "runc", "--pid", "host", "--network", "host",
		"--cap-add", "SYS_PTRACE", "--security-opt", "apparmor=unconfined",
		"--label", LabelRunID + "=" + spec.RunID,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", l.credDir + ":" + l.credDir + ":ro",
		"-v", spec.SourceDir + ":" + TreeDir + ":ro",
		"-w", TreeDir, "-e", conformanceCanaryVar, "-e", "RELIANT_GATE=1", "-e", "HOME=" + HomeDir}
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "--entrypoint", spec.Command[0], l.image)
	args = append(args, spec.Command[1:]...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = spec.Stdout, spec.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return Result{Class: ClassPass}, nil
	case errors.As(err, &ee):
		return Result{Class: ClassifyExit(ee.ExitCode()), ExitCode: ee.ExitCode()}, nil
	default:
		return Result{}, err
	}
}

// TestDenialCasesCatchALeakyBoundary: each denial case fails against the
// leaky backend, so a boundary with any of its leaks cannot pass the table.
// It needs only the host engine (plain runc), so it runs where sysbox does
// not. RELIANT_GATE_TEST_LEAKY_IMAGE (default docker:27-cli) needs sh, wget,
// ip and the docker CLI.
func TestDenialCasesCatchALeakyBoundary(t *testing.T) {
	testkit.Slow(t, "runs containers on the host engine for every denial case")
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("no container engine here: %v", err)
	}
	image := os.Getenv("RELIANT_GATE_TEST_LEAKY_IMAGE")
	if image == "" {
		image = "docker:27-cli"
	}
	f := plantHost(t)
	b := leakyBackend{image: image, credDir: filepath.Dir(f.credPath)}
	src := conformanceTree(t)
	sem := make(chan struct{}, conformanceParallel)
	for _, c := range conformanceCases() {
		if !c.denial {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sem <- struct{}{}
			defer func() { <-sem }()
			failure, skip, err := runCase(b, f, image, nil, src, c)
			switch {
			case err != nil:
				t.Fatal(err)
			case skip != "":
				t.Skip(skip)
			case failure == nil:
				t.Error("the case passed against a leaky boundary: it does not detect the leak")
			default:
				t.Logf("caught: %v", firstLine(failure.Error()))
			}
		})
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
