// Copyright (c) 2025 Reliant Labs
package gate

import (
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
)

// The testdata dumps are iptables-save and ip6tables-save output (nf_tables
// backend, iptables 1.8.10) from a throwaway network namespace holding a
// docker-like DOCKER-USER chain, a ufw-like INPUT jump and an ip6tables
// ACCEPT, after ProvisionScript with Allow 10.20.0.0/16 ran in it twice
// (egress_netns_linux_test.go regenerates the same shape for real).
func readDumps(t *testing.T) (string, string) {
	t.Helper()
	v4, err := os.ReadFile("testdata/provisioned-v4.dump")
	if err != nil {
		t.Fatal(err)
	}
	v6, err := os.ReadFile("testdata/provisioned-v6.dump")
	if err != nil {
		t.Fatal(err)
	}
	return string(v4), string(v6)
}

func TestVerifyRulesAcceptsProvisionedRules(t *testing.T) {
	v4, v6 := readDumps(t)
	allow, err := VerifyRules([]string{v4}, []string{v6})
	if err != nil {
		t.Fatalf("VerifyRules on provisioned rules: %v", err)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}; !slices.Equal(allow, want) {
		t.Errorf("allow = %v, want %v", allow, want)
	}
	// A dump from another backend that holds nothing does not spoil it.
	if _, err := VerifyRules([]string{"", v4}, []string{"*filter\nCOMMIT\n", v6}); err != nil {
		t.Errorf("with an empty legacy dump beside: %v", err)
	}
}

func TestVerifyRulesRefusesWeakenedRules(t *testing.T) {
	v4, v6 := readDumps(t)
	jump := "-A DOCKER-USER -i rgate+ -j RELIANT-GATE-G1\n"
	host := "-A INPUT -i rgate+ -m comment --comment \"reliant-gate:host\" -j DROP\n"
	cases := []struct {
		name   string
		v4, v6 string
	}{
		{"no rules at all", "*filter\n:INPUT ACCEPT [0:0]\nCOMMIT\n", v6},
		{"a deny missing", strings.Replace(v4, "-A RELIANT-GATE-G1 -d 169.254.0.0/16 -m comment --comment \"reliant-gate:deny\" -j DROP\n", "", 1), v6},
		{"no jump", strings.Replace(v4, jump, "", 1), v6},
		{"jump after an ACCEPT", strings.Replace(v4, jump, "-A DOCKER-USER -j ACCEPT\n"+jump, 1), v6},
		{"DOCKER-USER not first in FORWARD", strings.Replace(v4, "-A FORWARD -j DOCKER-USER\n", "-A FORWARD -j ACCEPT\n-A FORWARD -j DOCKER-USER\n", 1), v6},
		{"host drop not first in INPUT", strings.Replace(v4, host, "", 1), v6},
		{"host drop after ufw", strings.Replace(v4, host+"-A INPUT -j ufw-before-input\n", "-A INPUT -j ufw-before-input\n"+host, 1), v6},
		{"an ACCEPT inside the chain", strings.Replace(v4, "-A RELIANT-GATE-G1 -d 10.20.0.0/16", "-A RELIANT-GATE-G1 -j ACCEPT\n-A RELIANT-GATE-G1 -d 10.20.0.0/16", 1), v6},
		{"an allow after a deny", strings.Replace(v4, "COMMIT", "-A RELIANT-GATE-G1 -d 192.168.1.0/24 -m comment --comment \"reliant-gate:allow\" -j RETURN\nCOMMIT", 1), v6},
		{"a deny narrowed to one port", strings.Replace(v4, "-d 10.0.0.0/8 -m comment", "-d 10.0.0.0/8 -p tcp -m tcp --dport 80 -m comment", 1), v6},
		{"a jump narrowed to tcp", strings.Replace(v4, jump, "-A DOCKER-USER -i rgate+ -p tcp -j RELIANT-GATE-G1\n", 1), v6},
		{"no IPv6 host drop", v4, strings.Replace(v6, host, "", 1)},
		{"no IPv6 forward drop", v4, strings.Replace(v6, "-A DOCKER-USER -i rgate+ -m comment --comment \"reliant-gate:v6\" -j DROP\n", "", 1)},
		{"no IPv6 dump", v4, ""},
		{"the hooked generation is incomplete, a complete one hooked behind it", strings.Replace(
			strings.Replace(v4, ":RELIANT-GATE-G1 - [0:0]\n", ":RELIANT-GATE-G1 - [0:0]\n:RELIANT-GATE-G2 - [0:0]\n", 1),
			jump, "-A DOCKER-USER -i rgate+ -j RELIANT-GATE-G2\n"+jump, 1) + "*filter\n-A RELIANT-GATE-G2 -d 10.0.0.0/8 -m comment --comment \"reliant-gate:deny\" -j DROP\nCOMMIT\n", v6},
		{"a jump to a chain that is not ours", strings.Replace(v4, jump, "-A DOCKER-USER -i rgate+ -j ACCEPT\n", 1), v6},
	}
	for _, c := range cases {
		var v6Dumps []string
		if c.v6 != "" {
			v6Dumps = []string{c.v6}
		}
		if _, err := VerifyRules([]string{c.v4}, v6Dumps); err == nil {
			t.Errorf("%s: VerifyRules passed", c.name)
		}
	}
}

// Whichever generation the first jump targets is the one checked; an
// unhooked generation beside it (left by an interrupted run) changes nothing.
func TestVerifyRulesChecksTheHookedGeneration(t *testing.T) {
	v4, v6 := readDumps(t)
	stale := strings.Replace(v4, ":RELIANT-GATE-G1 - [0:0]\n", ":RELIANT-GATE-G1 - [0:0]\n:RELIANT-GATE-NEXT - [0:0]\n", 1)
	stale = strings.Replace(stale, "COMMIT", "-A RELIANT-GATE-NEXT -d 10.0.0.0/8 -j ACCEPT\nCOMMIT", 1)
	if _, err := VerifyRules([]string{stale}, []string{v6}); err != nil {
		t.Errorf("an unhooked stale generation beside the hooked one: %v", err)
	}
	renamed := strings.ReplaceAll(v4, "RELIANT-GATE-G1", "RELIANT-GATE-G9")
	if _, err := VerifyRules([]string{renamed}, []string{v6}); err != nil {
		t.Errorf("another generation's name: %v", err)
	}
}

func TestVerifyRulesAcceptsForwardPlacement(t *testing.T) {
	// No DOCKER-USER (an ip6tables the engine does not program): the drop is
	// first in FORWARD itself.
	v4, _ := readDumps(t)
	v6 := "*filter\n:INPUT ACCEPT [0:0]\n:FORWARD DROP [0:0]\n" +
		"-A INPUT -i rgate+ -m comment --comment reliant-gate:host -j DROP\n" +
		"-A FORWARD -i rgate+ -m comment --comment reliant-gate:v6 -j DROP\n-A FORWARD -j ACCEPT\nCOMMIT\n"
	if _, err := VerifyRules([]string{v4}, []string{v6}); err != nil {
		t.Errorf("drop first in FORWARD: %v", err)
	}
}

func TestProvisionScriptCoversEveryDeny(t *testing.T) {
	allow := netip.MustParsePrefix("10.20.0.5/16") // unmasked on purpose
	script := EgressPolicy{Allow: []netip.Prefix{allow}}.ProvisionScript()
	for _, d := range DefaultDeny {
		if !strings.Contains(script, "-d "+d.String()+" -m comment --comment reliant-gate:deny -j DROP") {
			t.Errorf("script does not drop %s", d)
		}
	}
	allowAt := strings.Index(script, "-d 10.20.0.0/16 -m comment --comment reliant-gate:allow -j RETURN")
	firstDeny := strings.Index(script, "reliant-gate:deny")
	if allowAt < 0 || allowAt > firstDeny {
		t.Errorf("the allow must be written, masked, before the denies:\n%s", script)
	}
	for _, want := range []string{"set -eu", "flock -w", "-restore -w --noflush", `echo "-I DOCKER-USER 1 -i rgate+ -j $new"`,
		"$IPT -I INPUT 1 -i rgate+", "$IP6T -I INPUT 1 -i rgate+", "reliant-gate:v6"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	// The swap is one transaction, after the new chain was filled and read
	// back; stale generations go only after it.
	fill := strings.Index(script, `rules "$new"; echo COMMIT; } | $IPT_RESTORE`)
	check := strings.Index(script, `"$(listing "$new")" != "$(rules "$new")"`)
	swap := strings.Index(script, `echo "-I DOCKER-USER 1 -i rgate+ -j $new"; echo "$jumps" | sed -n 's/^-A /-D /p'; echo COMMIT; } | $IPT_RESTORE`)
	drop := strings.Index(script, `$IPT -X "$c"`)
	if fill < 0 || check < fill || swap < check || drop < swap {
		t.Errorf("fill %d, check %d, swap %d, delete stale %d: want them in that order:\n%s", fill, check, swap, drop, script)
	}
	for _, bad := range []string{"$IPT -E ", "-F DOCKER-USER", "$IPT -D DOCKER-USER"} {
		if strings.Contains(script, bad) {
			t.Errorf("script holds %q: an unhook outside the swap transaction", bad)
		}
	}
	for _, d := range []string{"169.254.169.254", "127.0.0.1"} {
		a := netip.MustParseAddr(d)
		covered := false
		for _, p := range DefaultDeny {
			covered = covered || p.Contains(a)
		}
		if !covered {
			t.Errorf("%s is not in DefaultDeny", d)
		}
	}
}

func TestCheckEgressAllow(t *testing.T) {
	p := func(ss ...string) []netip.Prefix { return mustPrefixes(ss...) }
	cases := []struct {
		name                  string
		declared, provisioned []netip.Prefix
		ok                    bool
	}{
		{"none either side", nil, nil, true},
		{"equal", p("10.20.0.0/16"), p("10.20.0.0/16"), true},
		{"declared covers a narrower machine allow", p("10.0.0.0/8"), p("10.20.0.0/16"), false},
		{"machine allows what the gate did not declare", nil, p("10.20.0.0/16"), false},
		{"gate declares what the machine does not allow", p("10.20.0.0/16"), nil, false},
		{"declared narrower than the machine's", p("10.20.1.0/24"), p("10.20.0.0/16"), false},
	}
	for _, c := range cases {
		err := CheckEgressAllow(c.declared, c.provisioned)
		if (err == nil) != c.ok {
			t.Errorf("%s: CheckEgressAllow = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	got := splitArgs(`-A X -m comment --comment "two words" -j DROP`)
	want := []string{"-A", "X", "-m", "comment", "--comment", "two words", "-j", "DROP"}
	if !slices.Equal(got, want) {
		t.Errorf("splitArgs = %q, want %q", got, want)
	}
}
