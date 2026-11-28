// Copyright (c) 2025 Reliant Labs

//go:build linux && integration

package gate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run the real ProvisionScript, real iptables and real packets in
// a throwaway network namespace (`unshare --net`, as root or through
// passwordless sudo), so they prove the host half of gate isolation without
// sysbox. The namespace, and everything in it, is gone when the script exits;
// the machine's own tables are never touched.

var netnsAllow = mustPrefixes("10.20.0.0/16")

// netnsScriptTimeout bounds one netns script (each takes 10-30s).
const netnsScriptTimeout = 3 * time.Minute

// inNetns runs script with sh as root in a fresh network namespace and
// returns its output. It skips the test where neither root nor passwordless
// sudo can create one.
func inNetns(t *testing.T, script string) string {
	t.Helper()
	prefix := []string{"sudo", "-n"}
	if os.Geteuid() == 0 {
		prefix = nil
	}
	args := append(append([]string{}, prefix...), "unshare", "--net", "sh", "-c", "iptables -S >/dev/null && ip6tables -S >/dev/null")
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		t.Skipf("cannot create a network namespace with iptables here (needs root or passwordless sudo, unshare and iptables): %v: %s", err, bytes.TrimSpace(out))
	}
	path := filepath.Join(t.TempDir(), "netns.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Bounded, so a stall fails here, naming itself, inside the package's
	// budget. SIGTERM, which sudo relays to the script (unshare execs it),
	// whose EXIT trap then cleans up; WaitDelay bounds the rest.
	ctx, cancel := context.WithTimeout(context.Background(), netnsScriptTimeout)
	defer cancel()
	args = append(append([]string{}, prefix...), "unshare", "--net", "sh", path)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("netns script did not finish within %s:\n%s", netnsScriptTimeout, out)
	}
	if err != nil {
		t.Fatalf("netns script: %v\n%s", err, out)
	}
	return string(out)
}

// baseTables is a docker-like host before provisioning: DOCKER-USER first in
// FORWARD for both families, a ufw-like INPUT jump, an ip6tables ACCEPT.
const baseTables = `iptables -F; iptables -X
iptables-restore <<'EOF'
*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:DOCKER-USER - [0:0]
:ufw-before-input - [0:0]
-A INPUT -j ufw-before-input
-A FORWARD -j DOCKER-USER
-A DOCKER-USER -j RETURN
COMMIT
EOF
ip6tables -F; ip6tables -X
ip6tables-restore <<'EOF'
*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:DOCKER-USER - [0:0]
-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
-A FORWARD -j DOCKER-USER
COMMIT
EOF
`

// chainRules are the iptables commands that fill chain with the first n
// rules of the rule set (all of them when n < 0).
func chainRules(chain string, n int) string {
	var lines []string
	for _, a := range netnsAllow {
		lines = append(lines, fmt.Sprintf("iptables -A %s -d %s -m comment --comment %s -j RETURN", chain, a, commentAllow))
	}
	for _, d := range DefaultDeny {
		lines = append(lines, fmt.Sprintf("iptables -A %s -d %s -m comment --comment %s -j DROP", chain, d, commentDeny))
	}
	if n >= 0 && n < len(lines) {
		lines = lines[:n]
	}
	return "iptables -N " + chain + "\n" + strings.Join(lines, "\n") + "\n"
}

const hostRules = `iptables -I INPUT 1 -i rgate+ -m comment --comment reliant-gate:host -j DROP
ip6tables -I INPUT 1 -i rgate+ -m comment --comment reliant-gate:host -j DROP
ip6tables -I DOCKER-USER 1 -i rgate+ -m comment --comment reliant-gate:v6 -j DROP
`

func hook(chain string) string { return "iptables -I DOCKER-USER 1 -i rgate+ -j " + chain + "\n" }

// The wrapper stands in for iptables, ip6tables and their -restore twins. It
// counts the changes the provisioning run makes and, once the budget is
// spent, kills the run before it makes the next one: the run dies exactly
// between two changes, as a crash or a reboot would leave it.
const wrapper = `#!/bin/sh
real=$(basename "$0")
mut=no
case "$real" in *-restore) mut=yes ;; esac
for a in "$@"; do case "$a" in -N|-A|-I|-D|-F|-X|-E|-R|-P|-Z) mut=yes ;; esac; done
if [ "$mut" = yes ]; then
	[ ! -e "$W/dead" ] || exit 1
	n=$(cat "$W/count")
	if [ "$n" -ge "$(cat "$W/budget")" ]; then
		: >"$W/dead"
		kill -9 "$PPID" 2>/dev/null
		exit 1
	fi
	echo $((n + 1)) >"$W/count"
fi
PATH=/usr/sbin:/sbin:/usr/bin:/bin exec "$real" "$@"
`

// The provisioning run from every state an earlier run can leave — this
// version's, killed between any two of its changes, and the first version's
// interrupted states — never leaves forwarded gate traffic without a
// complete rule set hooked first, ends in the one canonical shape, and
// changes nothing when re-run on it.
func TestProvisionScriptNeverUnguardsInNetns(t *testing.T) {
	script := EgressPolicy{Allow: netnsAllow}.ProvisionScript()
	g1 := EgressChain + "-G1"
	states := []struct {
		name      string
		setup     string
		protected bool // whether the state already guards gates
	}{
		{"fresh host", "", false},
		{"first version, complete", chainRules(EgressChain, -1) + hook(EgressChain) + hostRules, true},
		{"first version, NEXT only (stopped before its rename)", chainRules(EgressChain+"-NEXT", -1) + hook(EgressChain+"-NEXT") + hostRules, true},
		{"first version, both chains hooked", chainRules(EgressChain, -1) + hook(EgressChain) + chainRules(EgressChain+"-NEXT", -1) + hook(EgressChain+"-NEXT") + hostRules, true},
		{"first version, old hooked and a stale partial NEXT", chainRules(EgressChain, -1) + hook(EgressChain) + chainRules(EgressChain+"-NEXT", 3) + hostRules, true},
		{"current", chainRules(g1, -1) + hook(g1) + hostRules, true},
		{"current, jump duplicated", chainRules(g1, -1) + hook(g1) + hook(g1) + hostRules, true},
		{"current plus an unhooked later generation", chainRules(g1, -1) + hook(g1) + chainRules(EgressChain+"-G7", 2) + hostRules, true},
		{"tampered: a deny removed from the hooked chain", chainRules(g1, -1) + hook(g1) + hostRules +
			"iptables -D " + g1 + " -d 169.254.0.0/16 -m comment --comment reliant-gate:deny -j DROP\n", false},
		{"host rules pushed down by a firewall reload", chainRules(g1, -1) + hook(g1) + hostRules +
			"iptables -I INPUT 1 -j ufw-before-input\n", false},
	}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			var sh strings.Builder
			fmt.Fprintf(&sh, "set -u\nW=$(mktemp -d)\nexport W\ntrap 'rm -rf \"$W\"' EXIT\n")
			fmt.Fprintf(&sh, "cat >\"$W/provision.sh\" <<'RELIANT_EOF'\n%sRELIANT_EOF\n", script)
			fmt.Fprintf(&sh, "cat >\"$W/wrapper\" <<'RELIANT_EOF'\n%sRELIANT_EOF\n", wrapper)
			sh.WriteString(`chmod +x "$W/wrapper"
for b in iptables iptables-restore ip6tables ip6tables-restore; do ln -s "$W/wrapper" "$W/$b"; done
reset() {
` + baseTables + st.setup + `}
dump() {
	echo "### reliant-gate-dump iptables"; iptables-save -t filter
	echo "### reliant-gate-dump ip6tables"; ip6tables-save -t filter
}
run() { # budget
	echo "$1" >"$W/budget"; echo 0 >"$W/count"; rm -f "$W/dead"
	IPTABLES="$W/iptables" IP6TABLES="$W/ip6tables" RELIANT_GATE_LOCK="$W/lock" sh "$W/provision.sh" >"$W/out" 2>&1
	rc=$?
	[ ! -e "$W/dead" ] || rc=killed
}
reset >/dev/null
echo "=== step initial"; dump
budget=0
while :; do
	reset >/dev/null
	run "$budget"
	echo "=== step $budget $rc"; dump
	[ "$rc" = killed ] || break
	budget=$((budget + 1))
	[ "$budget" -lt 200 ] || { echo "=== runaway"; break; }
done
cat "$W/out"
run 0
echo "=== step rerun $rc"; dump
cat "$W/out"
`)
			out := inNetns(t, sh.String())
			steps := strings.Split(out, "=== step ")[1:]
			if strings.Contains(out, "=== runaway") || len(steps) < 3 {
				t.Fatalf("the run never finished:\n%s", out)
			}
			for i, s := range steps {
				header, body, _ := strings.Cut(s, "\n")
				v4, v6 := splitDumps(body)
				_, err := VerifyRules(v4, v6)
				last := i == len(steps)-2 || i == len(steps)-1
				fields := strings.Fields(header)
				if last && fields[len(fields)-1] != "0" {
					t.Errorf("step %q: the run exited %s, want 0:\n%s", header, fields[len(fields)-1], body)
				}
				if (st.protected || last) && err != nil {
					t.Errorf("step %q leaves gate traffic unguarded: %v\n%s", header, err, body)
				}
				if last {
					assertCanonical(t, header, v4)
				}
			}
			final, rerun := steps[len(steps)-2], steps[len(steps)-1]
			if a, b := tablesOf(final), tablesOf(rerun); a != b {
				t.Errorf("a re-run changed the tables:\n%s\n---\n%s", a, b)
			}
			t.Logf("%s: %d intermediate states checked", st.name, len(steps)-2)
		})
	}
}

// assertCanonical: one gate chain, hooked once, and no other generation.
func assertCanonical(t *testing.T, header string, v4 []string) {
	t.Helper()
	if len(v4) == 0 {
		t.Errorf("step %q: no IPv4 dump", header)
		return
	}
	tbl := parseFilter(v4[0])
	var ours []string
	for name := range tbl.chains {
		if isEgressChain(name) {
			ours = append(ours, name)
		}
	}
	jumps := 0
	for _, r := range tbl.chains["DOCKER-USER"] {
		if isEgressChain(r.target) {
			jumps++
		}
	}
	if len(ours) != 1 || jumps != 1 {
		t.Errorf("step %q: chains %v and %d gate jumps, want one of each:\n%s", header, ours, jumps, v4[0])
	}
}

// tablesOf strips the dumps' timestamp comments and counters.
func tablesOf(step string) string {
	_, body, _ := strings.Cut(step, "\n")
	var keep []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, dumpMarker) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// What the rules do to real packets. A gate network's bridge (rgate…) holds
// a "gate" namespace with IPv4 and IPv6 addresses; the "host" namespace
// routes to a private-range peer (also holding the metadata address
// 169.254.169.254, and a ULA) and a public-range peer, and listens itself on
// every address. Before provisioning everything is reachable — the positive
// control that makes each denial mean something — and after it, only the
// public peer is, from the gate, while the host still reaches the private
// peer.
func TestProvisionedRulesDenyInNetns(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed for the namespace listeners and probes")
	}
	script := EgressPolicy{Allow: netnsAllow}.ProvisionScript()
	probes := []struct {
		name, addr     string
		port           int
		after          bool // reachable from the gate once provisioned
		fromHostToo    bool // also probed from the host after provisioning
		hostReachAfter bool
	}{
		{name: "host, the bridge's address", addr: "172.30.0.1", port: 9090},
		{name: "host, another of its addresses", addr: "192.168.77.1", port: 9090},
		{name: "host, a public-range address", addr: "203.0.113.1", port: 9090},
		{name: "private-range peer", addr: "192.168.77.2", port: 8080, fromHostToo: true, hostReachAfter: true},
		{name: "metadata endpoint", addr: "169.254.169.254", port: 80},
		{name: "public-range peer", addr: "203.0.113.2", port: 8080, after: true},
		{name: "IPv6 host, the bridge's ULA", addr: "fd00:30::1", port: 9090},
		{name: "IPv6 host, link-local", addr: "fe80::1%eth0", port: 9090},
		{name: "IPv6 ULA peer", addr: "fd00:77::2", port: 8080},
		{name: "IPv6 public-range peer", addr: "2001:db8:113::2", port: 8080},
	}
	var sh strings.Builder
	sh.WriteString(`set -eu
W=$(mktemp -d)
cleanup() { kill $(cat "$W/pids") 2>/dev/null || true; rm -rf "$W"; }
trap cleanup EXIT
trap 'exit 1' INT TERM
: >"$W/pids"
cat >"$W/serve.py" <<'RELIANT_EOF'
import errno, socket, sys, threading, time
port, token = int(sys.argv[1]), sys.argv[2].encode()
def listen(addr):
    fam = socket.AF_INET6 if ":" in addr else socket.AF_INET
    s = socket.socket(fam, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if fam == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    # A just-added address may not be assignable yet: wait for it.
    deadline = time.monotonic() + 10
    while True:
        try:
            s.bind((addr, port)); break
        except OSError as e:
            if e.errno != errno.EADDRNOTAVAIL or time.monotonic() > deadline:
                raise OSError(e.errno, "binding %s:%d: %s" % (addr, port, e.strerror))
            time.sleep(0.05)
    s.listen(16)
    return s
def serve(s):
    while True:
        c, _ = s.accept(); c.sendall(token); c.close()
# Every socket listens before "ready" is printed.
ts = [threading.Thread(target=serve, args=(listen(a),), daemon=True) for a in sys.argv[3:]]
[t.start() for t in ts]
print("ready", flush=True)
[t.join() for t in ts]
RELIANT_EOF
cat >"$W/probe.py" <<'RELIANT_EOF'
import socket, sys, time
# probe.py addr port [seconds]: "yes" once the listener answers, retrying for
# seconds (a positive control waiting out neighbour discovery); "no" if not.
def once():
    try:
        info = socket.getaddrinfo(sys.argv[1], int(sys.argv[2]), type=socket.SOCK_STREAM)[0]
        s = socket.socket(info[0], socket.SOCK_STREAM); s.settimeout(2)
        s.connect(info[4]); return s.recv(64).startswith(b"token-")
    except Exception:
        return False
deadline = time.monotonic() + float(sys.argv[3] if len(sys.argv) > 3 else 0)
ok = once()
while not ok and time.monotonic() < deadline:
    time.sleep(0.2); ok = once()
print("yes" if ok else "no")
RELIANT_EOF
# waitfor DESC CMD...: CMD succeeds within 10s, or the script fails naming DESC.
waitfor() { d=$1; shift; i=0; until "$@"; do i=$((i+1)); [ $i -lt 200 ] || { echo "timed out waiting for $d" >&2; return 1; }; sleep 0.05; done; }
newns() { [ "$(readlink /proc/$1/ns/net 2>/dev/null)" != "" ] && [ "$(readlink /proc/$1/ns/net)" != "$(readlink /proc/self/ns/net)" ]; }
mkns() { unshare --net sleep 600 >/dev/null 2>&1 & echo $! >>"$W/pids"; waitfor "namespace of $!" newns $!; echo $!; }
inns() { p=$1; shift; nsenter -t "$p" -n "$@"; }
serve() { # ns-pid port addrs...
	p=$1; port=$2; shift 2
	if [ "$p" = host ]; then python3 "$W/serve.py" "$port" token-x "$@" >>"$W/serve.log" 2>&1 &
	else nsenter -t "$p" -n python3 "$W/serve.py" "$port" token-x "$@" >>"$W/serve.log" 2>&1 & fi
	echo $! >>"$W/pids"
}
ip link set lo up
sysctl -qw net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1
gate=$(mkns); priv=$(mkns); pub=$(mkns)

ip link add rgatetest0 type bridge
ip link set rgatetest0 up
ip addr add 172.30.0.1/24 dev rgatetest0
ip -6 addr add fd00:30::1/64 dev rgatetest0 nodad
ip -6 addr add fe80::1/64 dev rgatetest0 nodad
ip link add gveth type veth peer name eth0 netns "$gate"
ip link set gveth master rgatetest0 up
inns "$gate" ip link set lo up
inns "$gate" ip link set eth0 up
inns "$gate" ip addr add 172.30.0.2/24 dev eth0
inns "$gate" ip route add default via 172.30.0.1
inns "$gate" ip -6 addr add fd00:30::2/64 dev eth0 nodad
inns "$gate" ip -6 route add default via fd00:30::1

ip link add pveth type veth peer name eth0 netns "$priv"
ip link set pveth up
ip addr add 192.168.77.1/24 dev pveth
ip -6 addr add fd00:77::1/64 dev pveth nodad
ip route add 169.254.169.254/32 via 192.168.77.2
inns "$priv" ip link set lo up
inns "$priv" ip link set eth0 up
inns "$priv" ip addr add 192.168.77.2/24 dev eth0
inns "$priv" ip addr add 169.254.169.254/32 dev eth0
inns "$priv" ip route add default via 192.168.77.1
inns "$priv" ip -6 addr add fd00:77::2/64 dev eth0 nodad
inns "$priv" ip -6 route add default via fd00:77::1

ip link add uveth type veth peer name eth0 netns "$pub"
ip link set uveth up
ip addr add 203.0.113.1/24 dev uveth
ip -6 addr add 2001:db8:113::1/64 dev uveth nodad
inns "$pub" ip link set lo up
inns "$pub" ip link set eth0 up
inns "$pub" ip addr add 203.0.113.2/24 dev eth0
inns "$pub" ip route add default via 203.0.113.1
inns "$pub" ip -6 addr add 2001:db8:113::2/64 dev eth0 nodad
inns "$pub" ip -6 route add default via 2001:db8:113::1

serve host 9090 0.0.0.0 ::
serve "$priv" 8080 192.168.77.2 fd00:77::2
serve "$priv" 80 169.254.169.254
serve "$pub" 8080 203.0.113.2 2001:db8:113::2
ready4() { [ "$(grep -c '^ready$' "$W/serve.log" 2>/dev/null)" -ge 4 ]; }
waitfor "the four listeners" ready4 || { cat "$W/serve.log" >&2; exit 1; }
probe() { # label ns addr port [seconds to keep trying]
	r=$(if [ "$2" = host ]; then python3 "$W/probe.py" "$3" "$4" ${5:-0}; else nsenter -t "$2" -n python3 "$W/probe.py" "$3" "$4" ${5:-0}; fi)
	echo "REACH $1 $3 $r"
}
`)
	// Before provisioning every probe is a positive control, retried until
	// it answers (or 10s pass); after, one try each, since a denial must
	// hold every time.
	writeProbes := func(phase string, retry int) {
		for _, p := range probes {
			fmt.Fprintf(&sh, "probe %s-gate \"$gate\" %s %d %d\n", phase, p.addr, p.port, retry)
			if p.fromHostToo {
				fmt.Fprintf(&sh, "probe %s-host host %s %d %d\n", phase, p.addr, p.port, retry)
			}
		}
	}
	writeProbes("before", 10)
	sh.WriteString(baseTables)
	fmt.Fprintf(&sh, "cat >\"$W/provision.sh\" <<'RELIANT_EOF'\n%sRELIANT_EOF\n", script)
	sh.WriteString("RELIANT_GATE_LOCK=\"$W/lock\" sh \"$W/provision.sh\"\n")
	writeProbes("after", 0)

	out := inNetns(t, sh.String())
	got := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 4 && f[0] == "REACH" {
			got[f[1]+" "+f[2]] = f[3]
		}
	}
	t.Logf("probe results: %v", got)
	check := func(key string, want bool, what string) {
		t.Helper()
		v, ok := got[key]
		switch {
		case !ok:
			t.Errorf("%s: no probe result for %q\n%s", what, key, out)
		case (v == "yes") != want:
			t.Errorf("%s: reachable = %s, want %v", what, v, want)
		}
	}
	for _, p := range probes {
		check("before-gate "+p.addr, true, p.name+", before provisioning (the positive control)")
		check("after-gate "+p.addr, p.after, p.name+", from the gate once provisioned")
		if p.fromHostToo {
			check("after-host "+p.addr, p.hostReachAfter, p.name+", from the host once provisioned")
		}
	}
}
