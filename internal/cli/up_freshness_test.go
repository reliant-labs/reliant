package cli

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMarkedServiceGroups checks the process-table sweep groups this
// project+env's marked processes by their FORGE_UP_SERVICE marker, and skips
// foreign / legacy (project-less) / wrong-env processes — the same ownership
// predicate the reclaim guard uses.
func TestMarkedServiceGroups(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: marker("dev", "worker"),              // ours: worker
			101: marker("dev", "worker"),              // ours: worker (a second one)
			200: marker("dev", "api"),                 // ours: api
			300: markerProj("projB", "dev", "worker"), // different project — foreign
			400: legacyMarker("dev", "worker"),        // pre-fix (no project) — foreign
			500: marker("staging", "worker"),          // wrong env — not in this group
			600: {"PATH=/usr/bin"},                    // no marker
		},
		ppid: map[int]int{100: 1, 101: 100, 200: 1, 300: 1, 400: 1, 500: 1, 600: 1},
	}
	groups := markedServiceGroups(testProj, "dev", []int{100, 101, 200, 300, 400, 500, 600}, f)

	if len(groups) != 2 {
		t.Fatalf("groups: got %d (%v), want 2 (worker, api)", len(groups), groups)
	}
	w := append([]int(nil), groups["worker"]...)
	sort.Ints(w)
	if len(w) != 2 || w[0] != 100 || w[1] != 101 {
		t.Errorf("worker group: got %v, want [100 101]", w)
	}
	if a := groups["api"]; len(a) != 1 || a[0] != 200 {
		t.Errorf("api group: got %v, want [200]", a)
	}
}

// TestLeafPIDs is the duplicate-detection core: a healthy runner→server tree
// yields ONE leaf; the air leak (a second server, whether still a child of the
// runner or an orphan reparented to init) yields TWO — the flag.
func TestLeafPIDs(t *testing.T) {
	// Healthy: air(100) → server(101). One leaf.
	healthy := fakeFacts{ppid: map[int]int{100: 1, 101: 100}}
	if got := leafPIDs([]int{100, 101}, healthy); len(got) != 1 || got[0] != 101 {
		t.Errorf("healthy: got %v, want [101] (the server under air)", got)
	}

	// Leak A — both servers still under the runner: air(100) → server_old(101),
	// server_new(102). Two leaves.
	leakChild := fakeFacts{ppid: map[int]int{100: 1, 101: 100, 102: 100}}
	if got := leafPIDs([]int{100, 101, 102}, leakChild); len(got) != 2 {
		t.Errorf("leak (both under runner): got %v, want 2 leaves", got)
	}

	// Leak B — the old server orphaned (reparented to init): air(100) →
	// server_new(102); server_old(101) now has ppid 1. Still two leaves.
	leakOrphan := fakeFacts{ppid: map[int]int{100: 1, 101: 1, 102: 100}}
	got := leafPIDs([]int{100, 101, 102}, leakOrphan)
	if len(got) != 2 || got[0] != 101 || got[1] != 102 {
		t.Errorf("leak (orphaned old server): got %v, want [101 102]", got)
	}

	// Single-process runner (binary runner): one pid, both root and leaf.
	if got := leafPIDs([]int{100}, fakeFacts{ppid: map[int]int{100: 1}}); len(got) != 1 || got[0] != 100 {
		t.Errorf("single binary: got %v, want [100]", got)
	}
}

// TestEnrichServingDuplicateFlag drives the pure grouping+leaf path the way
// enrichServing does — marker sweep → per-service leaves — and asserts the
// worker service (two servers) is flagged duplicate while the single-server api
// is not. (The OS-facing build-freshness fill is exercised separately; this
// covers the detection logic.)
func TestEnrichServingDuplicateFlag(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			10: marker("dev", "worker"), // air (runner) for worker
			11: marker("dev", "worker"), // server_old (leaked)
			12: marker("dev", "worker"), // server_new
			20: marker("dev", "api"),    // air (runner) for api
			21: marker("dev", "api"),    // its single server
		},
		ppid: map[int]int{10: 1, 11: 10, 12: 10, 20: 1, 21: 20},
	}
	groups := markedServiceGroups(testProj, "dev", []int{10, 11, 12, 20, 21}, f)

	if leaves := leafPIDs(groups["worker"], f); len(leaves) <= 1 {
		t.Errorf("worker leaves: got %v, want >1 (duplicate)", leaves)
	}
	if leaves := leafPIDs(groups["api"], f); len(leaves) != 1 {
		t.Errorf("api leaves: got %v, want 1 (healthy)", leaves)
	}
}

// TestBinaryStale covers the three independent staleness signals folded into
// servingProc.Stale.
func TestBinaryStale(t *testing.T) {
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	newer := base.Add(time.Hour)
	older := base.Add(-time.Hour)

	// deleted binary → stale regardless of times.
	if !binaryStale(time.Time{}, time.Time{}, time.Time{}, true) {
		t.Error("deleted binary should be stale")
	}
	// binary older than HEAD commit → stale.
	if !binaryStale(older, base, newer /*head*/, false) {
		t.Error("binary predating HEAD commit should be stale")
	}
	// binary rebuilt after the process started → straggler → stale.
	if !binaryStale(newer /*mtime*/, base /*start*/, time.Time{}, false) {
		t.Error("binary newer than process start should be stale (straggler)")
	}
	// fresh: binary built at/after HEAD and process started at/after the build.
	if binaryStale(base /*mtime*/, newer /*start after build*/, older /*head*/, false) {
		t.Error("current binary should not be stale")
	}
	// unstatable (zero mtime), not deleted → cannot judge → not stale.
	if binaryStale(time.Time{}, base, base, false) {
		t.Error("zero mtime (unstatable) should not be flagged stale")
	}
}

// TestProcessCommandIdentity covers the argv → command-identity derivation that
// duplicate attribution groups by: the executable's base name plus the leading
// non-flag arguments, stopping at the first flag so that two processes running
// the same subcommand with DIFFERENT flags (a rebuild on a fresh ephemeral
// port) still share one identity and are therefore still seen as a duplicate.
func TestProcessCommandIdentity(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"absolute path", []string{"/usr/local/bin/reliant", "server", "worker"}, "reliant server worker"},
		{"flags excluded", []string{"/opt/reliant", "server", "worker", "--queue=main", "extra"}, "reliant server worker"},
		{"different subcommand", []string{"/usr/local/bin/reliant", "server", "api"}, "reliant server api"},
		{"bare binary", []string{"/tmp/go-build123/exe/main"}, "main"},
		{"single dash flag stops", []string{"./srv", "-v"}, "srv"},
		{"empty argv is unattributable", nil, ""},
		{"empty exe is unattributable", []string{""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processCommand(tc.argv); got != tc.want {
				t.Errorf("processCommand(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}

	// The load-bearing property: differing flags must NOT split one duplicate
	// into two singletons, or a real duplicate stops being reported.
	old := processCommand([]string{"/old/reliant", "server", "worker", "--port=1111"})
	fresh := processCommand([]string{"/new/reliant", "server", "worker", "--port=2222"})
	if old != fresh {
		t.Errorf("rebuild with different flags split the identity: %q vs %q", old, fresh)
	}
}

// TestAttributeDuplicatesNamesTheRealCommand is the regression for the defect:
// a second `reliant server worker` was DETECTED as a duplicate but REPORTED
// against the api-server row, because grouping used only the ownership marker
// (which rides the environment and so propagates to descendants) and never
// looked at argv.
//
// The assertion derives the expected attribution from the argv the producer
// actually computed — it never greps rendered output — so rewording the warning
// leaves it green while misattributing a process turns it red.
func TestAttributeDuplicatesNamesTheRealCommand(t *testing.T) {
	// One api-server row whose marker group also contains TWO worker processes
	// (they inherited FORGE_UP_SERVICE=api-server from their parent).
	procs := []servingProc{
		{PID: 100, Command: processCommand([]string{"/bin/reliant", "server", "api"})},
		{PID: 150, Command: processCommand([]string{"/bin/reliant", "server", "worker"})},
		{PID: 205, Command: processCommand([]string{"/bin/reliant", "server", "worker"})},
	}
	groups, undetermined := attributeDuplicates(procs)
	if undetermined {
		t.Fatal("attribution should be determined: every process has readable argv")
	}
	// Fail loudly when the derived set is empty — an attribution that finds
	// nothing must not read as "all clear" for a fixture that HAS a duplicate.
	if len(groups) == 0 {
		t.Fatal("derived duplicate set is EMPTY, but the fixture contains two `reliant server worker` processes")
	}
	if len(groups) != 1 {
		t.Fatalf("got %d duplicate groups (%+v), want exactly 1", len(groups), groups)
	}
	if groups[0].Command != "reliant server worker" {
		t.Errorf("duplicate attributed to %q, want %q — the WORKER is what is duplicated, not the api-server row it inherited its marker from",
			groups[0].Command, "reliant server worker")
	}
	if len(groups[0].PIDs) != 2 || groups[0].PIDs[0] != 150 || groups[0].PIDs[1] != 205 {
		t.Errorf("duplicate pids = %v, want [150 205]", groups[0].PIDs)
	}
	// The single api process must not be reported as duplicated.
	for _, g := range groups {
		if g.Command == "reliant server api" {
			t.Errorf("api reported as duplicated with pids %v, but only one api process exists", g.PIDs)
		}
	}
}

// TestAttributeDuplicatesUndeterminedWithoutArgv pins the honest-degradation
// contract: where argv is unavailable (a non-darwin/linux platform, or a
// process whose cmdline the kernel withholds) the report must say it could not
// determine attribution rather than guessing a row. A confident wrong
// attribution is worse than an admitted unknown.
func TestAttributeDuplicatesUndeterminedWithoutArgv(t *testing.T) {
	// No argv at all for either process — the Windows / unreadable case.
	blind := []servingProc{{PID: 150}, {PID: 205}}
	groups, undetermined := attributeDuplicates(blind)
	if !undetermined {
		t.Error("two processes with unreadable argv must report UNDETERMINED")
	}
	if len(groups) != 0 {
		t.Errorf("unreadable argv must not produce an attributed duplicate, got %+v", groups)
	}

	// Partial knowledge: two attributable workers PLUS one unreadable process.
	// The worker duplicate is still named (it is derived), and the unknown is
	// still flagged — one does not suppress the other.
	mixed := []servingProc{
		{PID: 150, Command: "reliant server worker"},
		{PID: 205, Command: "reliant server worker"},
		{PID: 300},
	}
	groups, undetermined = attributeDuplicates(mixed)
	if !undetermined {
		t.Error("an unreadable process alongside attributable ones must still report UNDETERMINED")
	}
	if len(groups) != 1 || groups[0].Command != "reliant server worker" {
		t.Fatalf("attributable duplicate lost: got %+v, want one `reliant server worker` group", groups)
	}

	// A single process is never a duplicate and never an unknown: there is
	// nothing it could be a duplicate OF.
	if g, u := attributeDuplicates([]servingProc{{PID: 1}}); len(g) != 0 || u {
		t.Errorf("single process: got groups=%+v undetermined=%v, want none/false", g, u)
	}
}

// TestEnrichServingAttributesWorkerNotAPIRow drives the WHOLE enrichServing
// path against fixtures — marker sweep, leaf reduction, argv attribution, row
// fields — and reproduces the reported defect end to end: a worker duplicate
// living under the api-server's marker group.
//
// It asserts on the structured row data the renderer consumes, so the test
// survives any rewording of the warning line.
func TestEnrichServingAttributesWorkerNotAPIRow(t *testing.T) {
	// api-server's runner (10) spawned the api server (11) and, because the
	// FORGE_UP_SERVICE marker is inherited through the environment, two worker
	// processes (12, 13) carry the api-server marker too.
	f := fakeFacts{
		env: map[int][]string{
			10: marker("dev", "api-server"),
			11: marker("dev", "api-server"),
			12: marker("dev", "api-server"),
			13: marker("dev", "api-server"),
		},
		ppid: map[int]int{10: 1, 11: 10, 12: 10, 13: 10},
		args: map[int][]string{
			10: {"/usr/local/bin/air"},
			11: {"/usr/local/bin/reliant", "server", "api"},
			12: {"/usr/local/bin/reliant", "server", "worker"},
			13: {"/usr/local/bin/reliant", "server", "worker"},
		},
	}
	rows := []upServiceRow{{Name: "api-server", Kind: "host", Log: "x"}}
	noStarts := func([]int) map[int]time.Time { return map[int]time.Time{} }
	enrichServingWith(rows, testProj, "dev", time.Time{}, f, []int{10, 11, 12, 13}, noStarts)

	if !rows[0].Duplicate {
		t.Fatal("duplicate not detected at all for a row backed by two identical worker processes")
	}
	if rows[0].AttributionUndetermined {
		t.Error("attribution reported undetermined even though every process had readable argv")
	}
	if len(rows[0].Duplicates) == 0 {
		t.Fatal("Duplicate is set but Duplicates is EMPTY — the row claims a duplicate it cannot name")
	}
	if got := rows[0].Duplicates[0].Command; got != "reliant server worker" {
		t.Errorf("row %q attributed its duplicate to %q, want %q — this is the reported defect: "+
			"the WORKER is duplicated, the api-server is not",
			rows[0].Name, got, "reliant server worker")
	}
	if pids := rows[0].Duplicates[0].PIDs; len(pids) != 2 || pids[0] != 12 || pids[1] != 13 {
		t.Errorf("duplicate pids = %v, want [12 13] (the two workers)", pids)
	}
	// The api server process exists but is NOT duplicated; nothing may claim it is.
	for _, d := range rows[0].Duplicates {
		if d.Command == "reliant server api" {
			t.Errorf("api falsely attributed as duplicated: %+v", d)
		}
	}
}

// TestEnrichServingUndeterminedOnPlatformWithoutArgv pins the cross-platform
// degradation through the real entry path: with NO argv available for any pid
// (the procinspect_other.go / Windows case), a multi-process row must report an
// explicit unknown and must NOT name a command.
func TestEnrichServingUndeterminedOnPlatformWithoutArgv(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			10: marker("dev", "api-server"),
			11: marker("dev", "api-server"),
			12: marker("dev", "api-server"),
		},
		ppid: map[int]int{10: 1, 11: 10, 12: 10},
		// args deliberately nil: every argv lookup reports unreadable.
	}
	rows := []upServiceRow{{Name: "api-server", Kind: "host", Log: "x"}}
	noStarts := func([]int) map[int]time.Time { return map[int]time.Time{} }
	enrichServingWith(rows, testProj, "dev", time.Time{}, f, []int{10, 11, 12}, noStarts)

	if len(rows[0].Serving) != 2 {
		t.Fatalf("serving leaves = %d, want 2 — the fixture must still detect the processes", len(rows[0].Serving))
	}
	if !rows[0].AttributionUndetermined {
		t.Error("no argv anywhere, yet the row did not report attribution as undetermined")
	}
	if len(rows[0].Duplicates) != 0 {
		t.Errorf("named a duplicated command with no argv to derive it from: %+v", rows[0].Duplicates)
	}
	if rows[0].Duplicate {
		t.Error("Duplicate must stay false when nothing could be attributed — an unknown is not a finding")
	}
	if !anyAttributionUndetermined(rows) {
		t.Error("anyAttributionUndetermined must surface the row-level unknown to the banner")
	}
}

// TestServingProcJSON pins the additive `serving[]` / `duplicate` JSON contract
// so a consumer can rely on the field names and omitempty behavior.
func TestServingProcJSON(t *testing.T) {
	row := upServiceRow{
		Name: "worker", Kind: "host", Log: ".forge/logs/dev/worker.log",
		Duplicate: true,
		Serving: []servingProc{
			{PID: 150, Path: "tmp/main", BuiltAt: "2026-07-22T10:15:30Z", StartedAt: "2026-07-22T10:12:00Z", Stale: true},
			{PID: 205, Path: "tmp/main", BuiltAt: "2026-07-22T10:15:30Z", StartedAt: "2026-07-22T10:15:31Z"},
		},
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back upServiceRow
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Duplicate || len(back.Serving) != 2 {
		t.Fatalf("round-trip: got duplicate=%v serving=%d; want true, 2", back.Duplicate, len(back.Serving))
	}
	if back.Serving[0].PID != 150 || !back.Serving[0].Stale || back.Serving[1].PID != 205 || back.Serving[1].Stale {
		t.Errorf("serving round-trip mismatch: %+v", back.Serving)
	}

	// A healthy host row (no serving/duplicate populated) must OMIT both keys,
	// so a consumer pinned to the old shape is unaffected.
	plain, _ := json.Marshal(upServiceRow{Name: "api", Kind: "host", Log: "x"})
	if s := string(plain); strings.Contains(s, "serving") || strings.Contains(s, "duplicate") {
		t.Errorf("empty row leaked serving/duplicate keys: %s", s)
	}
}
