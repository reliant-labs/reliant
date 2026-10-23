package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The readiness gate already resolves the holder's PID — that lookup is HOW it
// decides the port is foreign rather than ours. Throwing the pid away and
// printing only "held by another process" therefore sent the reader to `lsof
// -i :<port>` to rediscover a fact forge had in hand one line earlier, at the
// exact moment `forge env up` had just failed. These tests pin the pid (and
// the command line when argv is readable) into the message, and pin the
// degradation path: an unreadable argv must still name the pid rather than
// suppress the holder line or trail a dangling separator.

// holderEntities is a single host service declaring the two bind ports the
// readiness gate can fail on: one held by a foreign process, one bound by
// nobody. Only the foreign one has a holder to report.
func holderEntities() *KCLEntities {
	return &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("api", withListenPorts(3091, 6060)),
	}}
}

// holderResolve maps :3091 to the squatting pid; every other port resolves to
// nothing, which is what "listening, but no holder" and "not listening" both
// look like to the gate.
func holderResolve(port int) int {
	if port == 3091 {
		return 200
	}
	return 0
}

// The observed failure this reports on: a stale `kubectl port-forward` left
// running in another terminal holds the port, `forge env up` fails, and the
// message has to be enough to find the process without a second tool.
var holderArgv = []string{"kubectl", "port-forward", "-n", "control-plane-prod", "svc/api", "3091:3091"}

func TestHostReadyErrorNamesTheForeignHolder(t *testing.T) {
	// pid 200 carries no forge marker, so it classifies foreign; its argv is
	// readable.
	f := fakeFacts{
		env:  map[int][]string{200: {"PATH=/usr/bin"}},
		ppid: map[int]int{200: 1},
		args: map[int][]string{200: holderArgv},
	}

	rs := evalHostReadiness(holderEntities(), testProj, "dev", nil, listeningSet(3091), holderResolve, f)
	byPort := map[int]hostReadyResult{}
	for _, r := range rs {
		byPort[r.port] = r
	}

	foreign := byPort[3091]
	if foreign.state != portReadyForeign {
		t.Fatalf(":3091 = %v, want portReadyForeign (the row the holder is reported for)", foreign.state)
	}
	if foreign.holderPID != 200 {
		t.Errorf("holderPID = %d, want 200 — the gate resolved this pid to classify the row and must keep it", foreign.holderPID)
	}
	if want := strings.Join(holderArgv, " "); foreign.holderCmd != want {
		t.Errorf("holderCmd = %q, want %q", foreign.holderCmd, want)
	}
	// A port nobody is listening on has no holder to name — reporting one
	// would be inventing a process.
	if nobody := byPort[6060]; nobody.state != portReadyNobody || nobody.holderPID != 0 || nobody.holderCmd != "" {
		t.Errorf(":6060 = %+v, want portReadyNobody with no holder attributed", nobody)
	}

	msg := hostReadyError("dev", hostReadyUnready(rs), nil).Error()
	for _, want := range []string{"holder: pid 200", "kubectl port-forward", "control-plane-prod"} {
		if !strings.Contains(msg, want) {
			t.Errorf("readiness error missing %q — the reader is still sent to lsof:\n%s", want, msg)
		}
	}
	// Exactly one holder line: the nobody row must not grow one.
	if n := strings.Count(msg, "holder: pid"); n != 1 {
		t.Errorf("got %d holder lines, want 1 (only the foreign row has a holder):\n%s", n, msg)
	}
}

// argv is unreadable for a SIP-redacted system binary, for a process owned by
// another user, and on any platform with no argv source at all. That must
// degrade to the pid alone — never to a suppressed holder line (the pid is the
// most useful half) and never to a line trailing the "  " separator with
// nothing after it.
func TestHostReadyErrorDegradesWhenArgvIsUnreadable(t *testing.T) {
	f := fakeFacts{
		env:  map[int][]string{200: {"PATH=/usr/bin"}},
		ppid: map[int]int{200: 1},
		// args deliberately nil: argv(200) answers ok=false.
	}

	rs := evalHostReadiness(holderEntities(), testProj, "dev", nil, listeningSet(3091), holderResolve, f)
	var foreign hostReadyResult
	for _, r := range rs {
		if r.port == 3091 {
			foreign = r
		}
	}
	if foreign.holderPID != 200 {
		t.Errorf("holderPID = %d, want 200 — an unreadable argv must not cost us the pid too", foreign.holderPID)
	}
	if foreign.holderCmd != "" {
		t.Errorf("holderCmd = %q, want empty when argv is unreadable", foreign.holderCmd)
	}

	msg := hostReadyError("dev", hostReadyUnready(rs), nil).Error()
	if !strings.Contains(msg, "holder: pid 200\n") {
		t.Errorf("the holder line should end at the pid, with no dangling separator:\n%s", msg)
	}
}

// The command line is untrusted-length input: a `docker run` with fifty flags
// would push the actionable part of the message off the screen, so it is
// truncated. Truncating on a BYTE index splits a multi-byte rune — one
// non-ASCII character anywhere near the cut (a home directory with an accent
// is enough) leaves invalid UTF-8 in an error message.
func TestHolderCmdSuffixTruncatesOnARuneBoundary(t *testing.T) {
	// Positioned so a 120-BYTE cut lands inside the two-byte é.
	long := strings.Repeat("a", 119) + "é" + strings.Repeat("b", 60)

	got := holderCmdSuffix(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated command is not valid UTF-8 — the cut split a rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated command should say so; got %q", got)
	}
	if utf8.RuneCountInString(got) > 130 {
		t.Errorf("truncation did not bound the line: %d runes", utf8.RuneCountInString(got))
	}
	// A short command is passed through whole, with the separator that keeps
	// it off the pid.
	if got := holderCmdSuffix("air -c .air.toml"); got != "  air -c .air.toml" {
		t.Errorf("short command = %q, want it rendered verbatim after a separator", got)
	}
	// Nothing to report renders as nothing at all.
	if got := holderCmdSuffix("   "); got != "" {
		t.Errorf("blank command = %q, want empty", got)
	}
}
