// Copyright (c) 2025 Reliant Labs
package gate

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Egress enforcement lives on the host, never inside the gate.
//
// Inside the boundary the gate is root in its own user and network namespaces,
// so any rule placed there it can remove. The rules are therefore in the
// host's netfilter, keyed on the gate networks' bridge names: every gate
// network's bridge is named BridgePrefix plus a hash of its run id
// (bridgeName), so one rule set matching `-i rgate+` covers every gate, past
// and future, without a per-gate privileged step. They are:
//
//   - IPv4 forwarded traffic from a gate bridge passes a generation of
//     EgressChain first (one jump, the first rule of docker's DOCKER-USER
//     chain, replaced only atomically: ProvisionScript): the ranges a
//     machine allows return, then every DefaultDeny range is dropped, and the
//     rest — the internet — is forwarded and masqueraded as usual. The nested
//     engine's containers leave through the gate container's interface, so
//     they are covered too, and so is traffic DNATed to a published port of
//     another container (its address is private).
//   - Traffic from a gate bridge to the host itself (the bridge's gateway, the
//     host's other addresses, the daemon's ports) is dropped in INPUT.
//   - Gates have no IPv6: gate networks are created IPv4-only, and IPv6 from a
//     gate bridge is dropped in INPUT and FORWARD in case the gate enables it
//     on its own interface.
//
// Installing them needs root once per boot and after any firewall reload: the
// gate-check command (pipeline build plan E9c) runs ProvisionScript. The
// daemon cannot install them itself and does not try. What it does is check
// they are live before every gate (VerifyRules over iptables-save, read
// through the engine), and refuse the gate when they are not.

const (
	// BridgePrefix starts every gate network's bridge name.
	BridgePrefix = "rgate"
	// EgressChain names the IPv4 chains holding the allow and deny ranges.
	// Each provisioning run that changes them writes a new generation,
	// EgressChain-G<n>, and swaps the one jump to it atomically; the
	// verifier accepts whichever chain of this name the jump targets.
	EgressChain = "RELIANT-GATE"
	// RulesVersion names this rule set in ProvisionScript's header.
	RulesVersion = 2
	// ProvisionLock is the lock file ProvisionScript holds while it runs
	// (RELIANT_GATE_LOCK overrides it), so two runs never interleave.
	ProvisionLock = "/run/reliant-gate-egress.lock"

	commentAllow = "reliant-gate:allow"
	commentDeny  = "reliant-gate:deny"
	commentHost  = "reliant-gate:host"
	commentV6    = "reliant-gate:v6"
)

// DefaultDeny are the IPv4 ranges no gate reaches unless a machine allows one:
// private and shared address space, loopback, link-local (the cloud metadata
// endpoint, 169.254.169.254, and GCP's resolver), the Azure wire server,
// multicast and reserved space.
var DefaultDeny = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10", // carrier-grade NAT: Tailscale, Alibaba's metadata endpoint
	"127.0.0.0/8",
	"168.63.129.16/32", // Azure wire server
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"224.0.0.0/4",
	"240.0.0.0/4",
)

// EgressPolicy is a machine's egress rule set: DefaultDeny less Allow.
type EgressPolicy struct {
	Allow []netip.Prefix
}

// ProvisionScript is the POSIX shell script that installs p on a host, run as
// root (`sh -s < script`) after the engine has started. IPTABLES and
// IP6TABLES override the commands (their -restore twins are used beside
// them), RELIANT_GATE_LOCK the lock file.
//
// There is no moment, in a run or after one interrupted at any point, at
// which gate traffic is not dropped by a complete rule set, because the only
// thing that hooks the ranges in is one jump, and it is only ever replaced
// atomically:
//
//  1. Runs are serialized by flock(1) on ProvisionLock.
//  2. When the jump first in DOCKER-USER already targets a chain holding
//     exactly these rules, and is the only gate jump, nothing changes.
//  3. Otherwise a chain of a name never used before (EgressChain-G<n>) is
//     filled in one iptables-restore transaction while unhooked, then read
//     back and compared; a mismatch stops the run with nothing swapped.
//  4. One more transaction inserts the jump to it first in DOCKER-USER and
//     deletes every other gate jump, so the old chain guards until the new
//     one does. A transaction applies whole or not at all.
//  5. Only then are gate chains nothing references flushed and deleted:
//     generations left by an earlier run, interrupted or not.
//
// A host rule (INPUT, IPv6) already first in its chain is left alone, and
// otherwise one is inserted first, a single atomic change; a copy of it left
// further down by an earlier run is a second drop, so it is left in place.
// A run that fails part-way exits non-zero with every rule already in force
// still in force.
func (p EgressPolicy) ProvisionScript() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	in := BridgePrefix + "+"
	w("#!/bin/sh")
	w("# reliant gate egress rules, version %d. Generated by internal/credsandbox/gate; do not edit.", RulesVersion)
	w("# Idempotent. Run as root after the container engine starts, and again after any")
	w("# firewall reload or engine restart. It never leaves gate traffic unguarded.")
	w("set -eu")
	w(`IPT_BIN="${IPTABLES:-iptables}"`)
	w(`IP6T_BIN="${IP6TABLES:-ip6tables}"`)
	w(`IPT="$IPT_BIN -w"`)
	w(`IPT_RESTORE="${IPT_BIN}-restore -w --noflush"`)
	w(`IP6T="$IP6T_BIN -w"`)
	w(`LOCK="${RELIANT_GATE_LOCK:-%s}"`, ProvisionLock)
	w("")
	w(`command -v flock >/dev/null 2>&1 || { echo "reliant gate: flock(1) is required" >&2; exit 1; }`)
	w(`exec 9>>"$LOCK"`)
	w(`flock -w 300 9 || { echo "reliant gate: $LOCK is still held by another run" >&2; exit 1; }`)
	w("")
	w("# first_is CMD CHAIN RULE: is RULE the first rule of CHAIN?")
	w("first_is() {")
	w(`	[ "$($1 -S "$2" 2>/dev/null | sed -n 2p | tr -d '"')" = "$3" ]`)
	w("}")
	w("# listing CHAIN: the live rules of IPv4 CHAIN, as rules() prints them.")
	w("listing() {")
	w(`	$IPT -S "$1" 2>/dev/null | sed 1d | tr -d '"'`)
	w("}")
	w("# rules CHAIN: the rules CHAIN must hold: allowed ranges return, denied ranges drop.")
	w("rules() {")
	for _, a := range sortedPrefixes(p.Allow) {
		w(`	echo "-A $1 -d %s -m comment --comment %s -j RETURN"`, a, commentAllow)
	}
	for _, d := range DefaultDeny {
		w(`	echo "-A $1 -d %s -m comment --comment %s -j DROP"`, d, commentDeny)
	}
	w("}")
	w("")
	w("# IPv4 forwarding.")
	w("$IPT -n -L DOCKER-USER >/dev/null 2>&1 || $IPT -N DOCKER-USER")
	w(`ours=$($IPT -S | sed -n 's/^-N \(%s[A-Za-z0-9-]*\)$/\1/p')`, EgressChain)
	w(`jumps=$(listing DOCKER-USER | grep -E '^-A DOCKER-USER -i rgate\+ -j %s[A-Za-z0-9-]*$' || true)`, EgressChain)
	w(`first=$(listing DOCKER-USER | sed -n 1p)`)
	w(`hooked=${first##* }`)
	w(`current=no`)
	w(`if [ -n "$first" ] && [ "$first" = "-A DOCKER-USER -i %s -j $hooked" ] && [ "$(echo "$jumps" | wc -l)" -eq 1 ] &&`, in)
	w(`	[ "$(listing "$hooked")" = "$(rules "$hooked")" ]; then`)
	w(`	current=yes`)
	w(`fi`)
	w(`if [ "$current" = no ]; then`)
	w(`	n=1`)
	w(`	for c in $ours; do`)
	w(`		k=${c#%s-G}`, EgressChain)
	w(`		case "$k" in ''|*[!0-9]*) continue ;; esac`)
	w(`		[ "$k" -lt "$n" ] || n=$((k + 1))`)
	w(`	done`)
	w(`	new=%s-G$n`, EgressChain)
	w(`	{ echo '*filter'; echo ":$new - [0:0]"; rules "$new"; echo COMMIT; } | $IPT_RESTORE`)
	w(`	if [ "$(listing "$new")" != "$(rules "$new")" ]; then`)
	w(`		echo "reliant gate: $new does not hold the rules written to it; the rules in force are unchanged" >&2`)
	w(`		exit 1`)
	w(`	fi`)
	w(`	{ echo '*filter'; echo "-I DOCKER-USER 1 -i %s -j $new"; echo "$jumps" | sed -n 's/^-A /-D /p'; echo COMMIT; } | $IPT_RESTORE`, in)
	w(`	hooked=$new`)
	w(`fi`)
	w(`for c in $ours; do`)
	w(`	[ "$c" != "$hooked" ] || continue`)
	w(`	refs=$($IPT -n -L "$c" | sed -n '1s/.*(\([0-9]*\) references).*/\1/p')`)
	w(`	if [ "$refs" = 0 ]; then`)
	w(`		$IPT -F "$c"`)
	w(`		$IPT -X "$c"`)
	w(`	else`)
	w(`		echo "reliant gate: leaving $c in place: something else still references it" >&2`)
	w(`	fi`)
	w(`done`)
	w("")
	w("# The host itself: nothing from a gate bridge.")
	hostRule := fmt.Sprintf("-i %s -m comment --comment %s -j DROP", in, commentHost)
	w(`first_is "$IPT" INPUT "-A INPUT %s" || $IPT -I INPUT 1 %s`, hostRule, hostRule)
	w("")
	w("# IPv6: gates have none.")
	w(`first_is "$IP6T" INPUT "-A INPUT %s" || $IP6T -I INPUT 1 %s`, hostRule, hostRule)
	v6Rule := fmt.Sprintf("-i %s -m comment --comment %s -j DROP", in, commentV6)
	w("if $IP6T -n -L DOCKER-USER >/dev/null 2>&1; then chain=DOCKER-USER; else chain=FORWARD; fi")
	w(`first_is "$IP6T" "$chain" "-A $chain %s" || $IP6T -I "$chain" 1 %s`, v6Rule, v6Rule)
	return b.String()
}

// VerifyRules checks the IPv4 and IPv6 filter-table dumps (iptables-save and
// ip6tables-save output, possibly several of each, from both the nft and the
// legacy backends) for the rule set ProvisionScript installs, in force: hooked
// first where it is hooked, every DefaultDeny range dropped, and nothing in
// EgressChain but allow and deny ranges. It returns the ranges the machine
// allows. A rule set in either backend counts, because a drop in any
// backend's chain at a hook is final.
func VerifyRules(v4Dumps, v6Dumps []string) ([]netip.Prefix, error) {
	var allow []netip.Prefix
	var v4Errs []error
	ok4 := false
	for _, d := range v4Dumps {
		a, err := verifyV4(parseFilter(d))
		if err == nil {
			allow, ok4 = a, true
			break
		}
		v4Errs = append(v4Errs, err)
	}
	if !ok4 {
		if len(v4Errs) == 0 {
			return nil, errors.New("no IPv4 filter table could be read")
		}
		return nil, fmt.Errorf("IPv4 gate rules not in force: %w", errors.Join(v4Errs...))
	}
	var v6Errs []error
	for _, d := range v6Dumps {
		err := verifyV6(parseFilter(d))
		if err == nil {
			return allow, nil
		}
		v6Errs = append(v6Errs, err)
	}
	if len(v6Errs) == 0 {
		return nil, errors.New("no IPv6 filter table could be read")
	}
	return nil, fmt.Errorf("IPv6 gate rules not in force: %w", errors.Join(v6Errs...))
}

// CheckEgressAllow compares what a gate declared with what the machine's
// rules allow. Each must cover the other: a machine allowing more than the
// gate declared would open ranges the repository never asked for (refused for
// safety), and a gate declaring more than the machine allows would fail in
// ways that look like test failures (refused for clarity).
func CheckEgressAllow(declared, provisioned []netip.Prefix) error {
	var errs []error
	for _, p := range provisioned {
		if !coveredBy(p, declared) {
			errs = append(errs, fmt.Errorf("this machine lets gates reach %s, which the gate did not declare", p))
		}
	}
	for _, d := range declared {
		if !coveredBy(d, provisioned) {
			errs = append(errs, fmt.Errorf("the gate declares %s, which this machine's rules do not allow (re-run gate-check with it)", d))
		}
	}
	return errors.Join(errs...)
}

func coveredBy(p netip.Prefix, set []netip.Prefix) bool {
	for _, s := range set {
		if s.Bits() <= p.Bits() && s.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

func verifyV4(t filterTable) ([]netip.Prefix, error) {
	if !t.present {
		return nil, errors.New("no filter table in dump")
	}
	jump := func(r rule) bool {
		return r.in == BridgePrefix+"+" && isEgressChain(r.target) && r.dst == "" && len(r.other) == 0
	}
	if err := t.hookedFirst(jump, EgressChain+" jump"); err != nil {
		return nil, err
	}
	host := func(r rule) bool { return r.isOurDrop(commentHost) }
	if !t.firstIn("INPUT", host) {
		return nil, fmt.Errorf("the INPUT drop for %s+ is not the first INPUT rule", BridgePrefix)
	}
	// The chain that guards is the one the first jump targets; any other
	// generation is unhooked and does nothing.
	chain := t.firstHookTarget(jump)
	rules, ok := t.chains[chain]
	if !ok {
		return nil, fmt.Errorf("chain %s is missing", chain)
	}
	var allow []netip.Prefix
	denied := map[netip.Prefix]bool{}
	for i, r := range rules {
		p, err := netip.ParsePrefix(r.dst)
		if err != nil || r.in != "" || len(r.other) != 0 {
			return nil, fmt.Errorf("%s rule %d is not a range rule: %s", chain, i+1, r.raw)
		}
		p = p.Masked()
		switch {
		case r.target == "RETURN" && r.comment == commentAllow:
			if len(denied) > 0 {
				return nil, fmt.Errorf("%s allows %s after a deny: allows must come first", chain, p)
			}
			allow = append(allow, p)
		case r.target == "DROP" && r.comment == commentDeny:
			denied[p] = true
		default:
			return nil, fmt.Errorf("%s rule %d is neither an allow nor a deny: %s", chain, i+1, r.raw)
		}
	}
	var missing []string
	for _, d := range DefaultDeny {
		if !denied[d] {
			missing = append(missing, d.String())
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s does not drop %s", chain, strings.Join(missing, ", "))
	}
	return allow, nil
}

func verifyV6(t filterTable) error {
	if !t.present {
		return errors.New("no filter table in dump")
	}
	host := func(r rule) bool { return r.isOurDrop(commentHost) }
	if !t.firstIn("INPUT", host) {
		return fmt.Errorf("the IPv6 INPUT drop for %s+ is not the first INPUT rule", BridgePrefix)
	}
	fwd := func(r rule) bool { return r.isOurDrop(commentV6) }
	return t.hookedFirst(fwd, "IPv6 forward drop")
}

// rule is one `-A` line of an iptables-save dump, reduced to the fields gate
// rules use. Anything else on the line lands in other, so a rule narrowed by a
// protocol or port never passes for one of ours.
type rule struct {
	raw     string
	in      string
	dst     string
	target  string
	comment string
	other   []string
}

func (r rule) isOurDrop(comment string) bool {
	return r.in == BridgePrefix+"+" && r.target == "DROP" && r.comment == comment && r.dst == "" && len(r.other) == 0
}

type filterTable struct {
	present bool
	chains  map[string][]rule
}

func (t filterTable) firstIn(chain string, match func(rule) bool) bool {
	rules := t.chains[chain]
	return len(rules) > 0 && match(rules[0])
}

// isEgressChain: name is a generation of EgressChain (EgressChain itself,
// or EgressChain-<suffix>).
func isEgressChain(name string) bool {
	return name == EgressChain || strings.HasPrefix(name, EgressChain+"-")
}

// firstHookTarget is the target of the first rule forwarded traffic meets,
// given hookedFirst(match) holds.
func (t filterTable) firstHookTarget(match func(rule) bool) string {
	if t.firstIn("FORWARD", match) {
		return t.chains["FORWARD"][0].target
	}
	return t.chains["DOCKER-USER"][0].target
}

// hookedFirst: forwarded traffic meets the rule before any other — it is the
// first FORWARD rule, or the first DOCKER-USER rule while DOCKER-USER is
// FORWARD's first.
func (t filterTable) hookedFirst(match func(rule) bool, what string) error {
	if t.firstIn("FORWARD", match) {
		return nil
	}
	toUser := func(r rule) bool {
		return r.target == "DOCKER-USER" && r.in == "" && r.dst == "" && len(r.other) == 0
	}
	if t.firstIn("FORWARD", toUser) && t.firstIn("DOCKER-USER", match) {
		return nil
	}
	return fmt.Errorf("the %s is not the first rule forwarded traffic meets (first in DOCKER-USER, itself first in FORWARD)", what)
}

// parseFilter reads the *filter section of an iptables-save dump.
func parseFilter(dump string) filterTable {
	t := filterTable{chains: map[string][]rule{}}
	inFilter := false
	sc := bufio.NewScanner(strings.NewReader(dump))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "*filter":
			inFilter, t.present = true, true
		case strings.HasPrefix(line, "*"), line == "COMMIT":
			inFilter = false
		case inFilter && strings.HasPrefix(line, ":"):
			name, _, _ := strings.Cut(line[1:], " ")
			if _, ok := t.chains[name]; !ok {
				t.chains[name] = nil
			}
		case inFilter && strings.HasPrefix(line, "-A "):
			chain, r := parseRule(line)
			t.chains[chain] = append(t.chains[chain], r)
		}
	}
	return t
}

func parseRule(line string) (string, rule) {
	toks := splitArgs(line)
	r := rule{raw: line}
	chain := ""
	if len(toks) > 1 {
		chain = toks[1]
	}
	for i := 2; i < len(toks); i++ {
		tok := toks[i]
		val := ""
		if i+1 < len(toks) {
			val = toks[i+1]
		}
		switch tok {
		case "-i":
			r.in, i = val, i+1
		case "-d":
			r.dst, i = val, i+1
		case "-j":
			r.target, i = val, i+1
		case "-m":
			if val == "comment" {
				i++
				continue
			}
			r.other = append(r.other, tok)
		case "--comment":
			r.comment, i = val, i+1
		default:
			r.other = append(r.other, tok)
		}
	}
	return chain, r
}

// splitArgs splits an iptables-save line on spaces, honouring double quotes
// (the comment match quotes its text).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inQuote && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote, have = !inQuote, true
		case c == ' ' && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func sortedPrefixes(ps []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, len(ps))
	for i, p := range ps {
		out[i] = p.Masked()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}
