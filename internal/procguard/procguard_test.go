package procguard

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// table is an injected process table: pid -> entry.
type table map[int]Entry

func (t table) lookup(pid int) (Entry, bool) {
	e, ok := t[pid]
	return e, ok
}

func TestWalk_FollowsParentsToInit(t *testing.T) {
	// launchd(1) -> forge env up host (100) -> agent server (200, group 200)
	// -> shell (300, group 200) -> forge env down (400, group 400)
	tbl := table{
		1:   {PPID: 0, PGID: 1},
		100: {PPID: 1, PGID: 100},
		200: {PPID: 100, PGID: 200},
		300: {PPID: 200, PGID: 200},
		400: {PPID: 300, PGID: 400},
		// unrelated: a sibling session under the same server, and another stack
		310: {PPID: 200, PGID: 200},
		900: {PPID: 1, PGID: 900},
	}
	l := Walk(400, tbl.lookup)

	if got, want := l.PIDs(), []int{400, 300, 200, 100, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lineage = %v, want %v", got, want)
	}
	for _, pid := range []int{400, 300, 200, 100, 1} {
		if !l.Contains(pid) {
			t.Errorf("lineage must contain %d", pid)
		}
	}
	for _, pid := range []int{310, 900, 0, -1} {
		if l.Contains(pid) {
			t.Errorf("lineage must NOT contain %d (a sibling or a stranger)", pid)
		}
	}
	if !l.Self(400) || l.Self(300) {
		t.Error("Self must name only the process the walk started from")
	}
	if !l.ContainsGroup(200) || !l.ContainsGroup(400) {
		t.Error("lineage groups must include every member's process group")
	}
	if l.ContainsGroup(900) {
		t.Error("a group no member belongs to must not be protected")
	}
}

func TestWalk_StopsOnCycleUnknownAndSelfParent(t *testing.T) {
	tests := []struct {
		name string
		tbl  table
		self int
		want []int
	}{
		{
			name: "cycle",
			tbl:  table{10: {PPID: 20}, 20: {PPID: 30}, 30: {PPID: 10}},
			self: 10,
			want: []int{10, 20, 30},
		},
		{
			name: "unknown parent ends the walk after recording it",
			tbl:  table{10: {PPID: 20}},
			self: 10,
			want: []int{10, 20},
		},
		{
			name: "self-parented process",
			tbl:  table{10: {PPID: 10}},
			self: 10,
			want: []int{10},
		},
		{
			name: "self unknown to the table is still protected",
			tbl:  table{},
			self: 10,
			want: []int{10},
		},
		{
			name: "untrusted parent (reported as 0) ends the walk",
			tbl:  table{10: {PPID: 0}},
			self: 10,
			want: []int{10},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Walk(tc.self, tc.tbl.lookup).PIDs(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lineage = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSelf_ContainsThisProcessAndItsParent runs the real OS lookup: whatever
// else the table says, this process and the one that started it must be in
// its lineage — they are exactly what a teardown must never signal.
func TestSelf_ContainsThisProcessAndItsParent(t *testing.T) {
	l := Self()
	if !l.Contains(os.Getpid()) {
		t.Fatalf("Self() = %v does not contain this process (%d)", l.PIDs(), os.Getpid())
	}
	if !l.Self(os.Getpid()) {
		t.Errorf("Self() must start at this process, got %v", l.PIDs())
	}
	if ppid := os.Getppid(); ppid > 0 && !l.Contains(ppid) {
		t.Errorf("Self() = %v does not contain the parent %d", l.PIDs(), ppid)
	}
}

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Entry
		ok   bool
	}{
		{"plain", "1234 (bash) S 1200 1234 1234 34816 1234 4194304", Entry{PPID: 1200, PGID: 1234}, true},
		{"comm with spaces and parens", "77 (my (weird) prog) R 1 77 77 0", Entry{PPID: 1, PGID: 77}, true},
		{"truncated", "77 (x) S", Entry{}, false},
		{"no comm", "garbage", Entry{}, false},
		{"non-numeric", "77 (x) S abc 1", Entry{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcStat([]byte(tc.in))
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseProcStat(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTrustedParent(t *testing.T) {
	early := time.Unix(1000, 0)
	late := time.Unix(2000, 0)
	if !trustedParent(late, early, true, true) {
		t.Error("a parent created before its child is the real parent")
	}
	if !trustedParent(early, early, true, true) {
		t.Error("equal creation times (clock resolution) must still trust the link")
	}
	if trustedParent(early, late, true, true) {
		t.Error("a 'parent' created AFTER the child is a recycled pid — a stranger")
	}
	if trustedParent(late, early, true, false) || trustedParent(late, early, false, true) {
		t.Error("an unreadable creation time must not be trusted")
	}
}

func TestLineageError(t *testing.T) {
	err := error(&LineageError{PID: 1234, Command: "forge env up dev"})
	if !errors.Is(err, ErrLineage) {
		t.Fatal("LineageError must wrap ErrLineage")
	}
	msg := err.Error()
	for _, want := range []string{"pid 1234", "(forge env up dev)", "ancestor of this command", "end the session running you"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}
