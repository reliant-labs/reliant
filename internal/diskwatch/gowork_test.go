// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/proctree"
)

// A work directory goes only once proven dead: no live process references it,
// and every live process that could have created it started after it was
// born. A per-command GOTMPDIR root goes once nothing references it.
func TestTrimGoWorkDirs_RemovesOnlyProvenDead(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	births := map[string]time.Time{}
	mk := func(rel string, born time.Duration) string {
		t.Helper()
		p := filepath.Join(m.tmpdir, rel)
		writeFile(t, filepath.Join(p, "b001", "exe", "a.out"), 4096, m.now.Add(-born))
		births[p] = m.now.Add(-born)
		return p
	}
	dead := mk("go-build1", time.Hour)
	referenced := mk("go-build2", time.Hour)
	creatorAlive := mk("go-build3", 20*time.Minute)
	young := mk("go-build4", 5*time.Minute)
	deadLink := mk("go-link-1", time.Hour)
	liveLink := mk("go-link-2", time.Hour)
	other := mk("not-go", time.Hour)
	base := commandGoTmpBasePrefix + itoa(os.Getuid())
	deadRoot := mk(filepath.Join(base, "gotmp-1-1"), time.Hour)
	liveRoot := mk(filepath.Join(base, "gotmp-1-2"), time.Hour)

	goCmd := procStart{PID: 42, Comm: "go", Start: m.now.Add(-30 * time.Minute)}
	linker := procStart{PID: 43, Comm: "link", Start: m.now.Add(-2 * time.Hour)}
	m.w.trim.birth = func(p string) time.Time { return births[p] }
	m.w.trim.procs = func() (procSnapshot, error) {
		s := procSnapshot{goStarts: []procStart{goCmd}, linkStarts: []procStart{linker}}
		s.addRefs("/proc/9/cwd -> " + referenced + "/b001")
		s.addRefs("GOTMPDIR=" + liveRoot)
		s.addRefs(liveLink + "/go.o")
		return s, nil
	}

	m.w.TrimCaches(context.Background(), false)

	for p, want := range map[string]bool{
		dead: false, referenced: true, creatorAlive: true, young: true,
		deadLink: true, liveLink: true, other: true, deadRoot: false, liveRoot: true,
	} {
		if exists(p) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), exists(p), want)
		}
	}
	// deadLink is kept too: the linker alive since before it was born could
	// be its creator. Without it, it goes.
	linker.Start = m.now
	m.w.TrimCaches(context.Background(), false)
	if exists(deadLink) {
		t.Error("a go-link dir older than every live linker survived")
	}

	a := lastWorkDirAudit(t, m.w)
	if a.Proof != "proc_scan" || a.KeptReferenced != 3 || a.KeptCreatorAlive != 1 || a.KeptYoung != 1 || a.Removed != 1 {
		t.Errorf("audit = %+v", a)
	}
	if a.OldestCreator == "" {
		t.Error("the audit does not name the live go command that kept a directory")
	}
}

// reliant#3522: a live process whose references the kernel withholds (a
// capability holder) can hold only the work directories born at or before it
// started. Those are kept; every younger one is judged as before, so a
// `systemd --user` that has lingered since boot keeps nothing.
func TestTrimGoWorkDirs_CapabilityHolderKeepsOnlyWhatPredatesIt(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	births := map[string]time.Time{}
	base := commandGoTmpBasePrefix + itoa(os.Getuid())
	mk := func(rel string, born time.Duration) string {
		t.Helper()
		p := filepath.Join(m.tmpdir, base, rel)
		writeFile(t, filepath.Join(p, "go-build9", "b001", "a.out"), 4096, m.now.Add(-born))
		births[p] = m.now.Add(-born)
		return p
	}
	predates := mk("gotmp-1-1", time.Hour)
	younger := mk("gotmp-1-2", 20*time.Minute)
	m.w.trim.birth = func(p string) time.Time { return births[p] }
	holder := procStart{PID: 20256, Comm: "systemd", Start: m.now.Add(-30 * time.Minute)}
	m.w.trim.procs = func() (procSnapshot, error) {
		return procSnapshot{capHolders: []procStart{holder}}, nil
	}

	m.w.TrimCaches(context.Background(), false)
	if !exists(predates) || exists(younger) {
		t.Errorf("born before the holder started exists=%v (want true); after exists=%v (want false)", exists(predates), exists(younger))
	}
	a := lastWorkDirAudit(t, m.w)
	if a.Proof != "proc_scan" || a.KeptCapabilityHolder != 1 || a.Removed != 1 || a.Error != "" {
		t.Errorf("audit = %+v, want a proc_scan proof that kept 1 for the holder and removed 1", a)
	}
	if !strings.Contains(a.CapabilityHolders, "pid 20256 (systemd)") {
		t.Errorf("audit capability_holders = %q, want the holder named", a.CapabilityHolders)
	}

	// A holder lingering since boot shields nothing younger than itself.
	holder.Start = m.now.Add(-48 * time.Hour)
	m.w.TrimCaches(context.Background(), false)
	if exists(predates) {
		t.Error("a root born after the holder started outlived the pass")
	}
}

func TestCapHolderShields(t *testing.T) {
	born := time.Unix(1000, 0)
	s := procSnapshot{capHolders: []procStart{{PID: 1, Start: born.Add(-time.Minute)}}}
	if _, ok := s.capHolderShields(born); ok {
		t.Error("a holder that started before the directory was born shields it")
	}
	s.capHolders = append(s.capHolders, procStart{PID: 2, Start: born.Add(time.Hour)}, procStart{PID: 3, Start: born.Add(time.Minute)})
	if h, ok := s.capHolderShields(born); !ok || h.PID != 3 {
		t.Errorf("capHolderShields = %+v %v, want the oldest holder started at or after the birth (pid 3)", h, ok)
	}
	// Within a clock tick before the birth is not before it.
	s.capHolders = []procStart{{PID: 4, Start: born.Add(-creatorSlack / 2)}}
	if _, ok := s.capHolderShields(born); !ok {
		t.Error("a holder within creatorSlack of the birth does not shield it")
	}
}

// lastWorkDirAudit is the newest go_work_dirs entry in the trim audit log.
func lastWorkDirAudit(t *testing.T, w *Watcher) workDirAudit {
	t.Helper()
	f, err := os.Open(filepath.Join(filepath.Dir(w.reaperPolicyPath(w.reaperConfig())), trimAuditName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var last *workDirAudit
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec trimAuditRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Pass == "go_work_dirs" {
			last = rec.WorkDirs
		}
	}
	if last == nil {
		t.Fatal("no go_work_dirs pass in the audit log")
	}
	return *last
}

// TestReaper_ReclaimsGoWorkDirsOnTheGoCacheCadence is the 2026-11-03/04
// machine: killed builds' work dirs took 113 GB of free space to zero in six
// hours while the work-dir pass ran only with the hourly full pass. Between
// full passes, the Go-cache pass's 10m tick reclaims them too.
func TestReaper_ReclaimsGoWorkDirsOnTheGoCacheCadence(t *testing.T) {
	h, goRuns := goCacheHarness(t)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.writeForgeRecord(autoGCRecord{At: h.now, OK: true})
		return reaperRun{}, nil
	}
	tmp := h.w.env.Getenv("TMPDIR")
	births := map[string]time.Time{}
	mk := func(name string) string {
		t.Helper()
		p := filepath.Join(tmp, name)
		writeFile(t, filepath.Join(p, "b001", "exe", "a.out"), 4096, h.now.Add(-time.Hour))
		births[p] = h.now.Add(-time.Hour)
		return p
	}
	h.w.trim.birth = func(p string) time.Time { return births[p] }
	h.w.trim.procs = func() (procSnapshot, error) { return procSnapshot{}, nil }

	if !h.tick() || h.runs() != 1 {
		t.Fatalf("first tick: want the full pass, runs=%d", h.runs())
	}
	build, link := mk("go-build7"), mk("go-link-7")
	h.advance(10 * time.Minute)
	if !h.tick() || len(*goRuns) != 1 || h.runs() != 1 {
		t.Fatalf("10m after: want the Go-cache pass alone, runs=%d go=%d", h.runs(), len(*goRuns))
	}
	if exists(build) || exists(link) {
		t.Errorf("a dead go-build=%v / go-link=%v dir outlived the 10m Go-cache tick; it waited for the hourly full pass", exists(build), exists(link))
	}
	if a := lastWorkDirAudit(t, h.w); a.Removed != 2 || a.Proof != "proc_scan" {
		t.Errorf("audit = %+v, want both dirs removed on a proc_scan proof", a)
	}
}

// TestReaperOff_ReclaimsTheDaemonsOwnGoTmpRoots is ovh-build-1 on 2026-11-22:
// a self-hosted daemon with no cgroup delegation and the storage reaper off
// (auto, not managed) had left 105k per-command GOTMPDIR roots, ~48 GiB,
// because only a cgroup lets proctree remove one and only the reaper ran the
// pass that removes the rest. With the reaper off, the daemon's own roots are
// still reclaimed on the Go-cache cadence, on the same /proc proof; a
// person's go-build* and go-link-* in their own TMPDIR are left alone.
func TestReaperOff_ReclaimsTheDaemonsOwnGoTmpRoots(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.w.ConfigureReaper(ReaperConfig{Enabled: false, Interval: time.Hour, PressureInterval: 10 * time.Minute, PolicyPath: h.policy})
	tmp := h.w.env.Getenv("TMPDIR")
	base := filepath.Join(tmp, commandGoTmpBasePrefix+itoa(os.Getuid()))
	births := map[string]time.Time{}
	mkRoot := func(name string, born time.Duration, withBuild bool) string {
		t.Helper()
		p := filepath.Join(base, name)
		if withBuild {
			writeFile(t, filepath.Join(p, "go-build123", "b001", "exe", "a.out"), 4096, h.now.Add(-born))
		} else if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		births[p] = h.now.Add(-born)
		return p
	}
	deadEmpty := mkRoot("gotmp-21208-1", time.Hour, false)
	deadKilledBuild := mkRoot("gotmp-21208-2", time.Hour, true)
	live := mkRoot("gotmp-21208-3", time.Hour, true)
	young := mkRoot("gotmp-21208-4", 5*time.Minute, false)
	priorDaemon := mkRoot("gotmp-999-7", 48*time.Hour, false)
	personsBuild := filepath.Join(tmp, "go-build77")
	personsLink := filepath.Join(tmp, "go-link-77")
	for _, p := range []string{personsBuild, personsLink} {
		writeFile(t, filepath.Join(p, "b001", "a.out"), 4096, h.now.Add(-24*time.Hour))
		births[p] = h.now.Add(-24 * time.Hour)
	}
	scans := 0
	h.w.trim.birth = func(p string) time.Time { return births[p] }
	h.w.trim.procs = func() (procSnapshot, error) {
		scans++
		var s procSnapshot
		s.addRefs("GOTMPDIR=" + live)
		return s, nil
	}

	if !h.w.reaperOffTick(context.Background()) {
		t.Fatal("with the reaper off, the first poll did not run the pass over the daemon's own roots")
	}
	if h.runs() != 0 {
		t.Fatalf("the reaper ran %d passes while disabled", h.runs())
	}
	for p, want := range map[string]bool{
		deadEmpty: false, deadKilledBuild: false, priorDaemon: false,
		live: true, young: true, personsBuild: true, personsLink: true,
	} {
		if exists(p) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), exists(p), want)
		}
	}
	a := lastWorkDirAudit(t, h.w)
	if a.Scope != "daemon_roots" || a.Proof != "proc_scan" || a.Scanned != 5 || a.Removed != 3 || a.KeptReferenced != 1 || a.KeptYoung != 1 {
		t.Errorf("audit = %+v, want 3 of the daemon's 5 roots removed and nothing else judged", a)
	}
	if !h.logged("Go work dir pass reclaimed dead work directories") || !h.logged("removed=3") {
		t.Errorf("no INFO line with the pass's counts:\n%s", h.logs)
	}

	// Its own cadence: not again within the Go-cache interval, again after.
	again := mkRoot("gotmp-21208-5", time.Hour, false)
	if h.w.reaperOffTick(context.Background()) || !exists(again) {
		t.Fatal("the pass ran again before DefaultGoCacheInterval")
	}
	h.advance(DefaultGoCacheInterval)
	if !h.w.reaperOffTick(context.Background()) || exists(again) {
		t.Fatalf("a dead root outlived the next pass, %s later", DefaultGoCacheInterval)
	}
	if !exists(personsBuild) || !exists(personsLink) {
		t.Error("the reaper-off pass removed a person's own go-build/go-link dir")
	}

	// No root base, no /proc scan: a pass where the daemon made no root costs
	// one lstat per temp dir.
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	before := scans
	h.advance(DefaultGoCacheInterval)
	h.w.reaperOffTick(context.Background())
	if scans != before {
		t.Error("the pass scanned /proc with no GOTMPDIR root base to judge")
	}
}

// With no /proc proof, the reaper-off pass removes no root, however old.
func TestReaperOff_NoProofRemovesNoRoot(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.w.ConfigureReaper(ReaperConfig{Enabled: false, PolicyPath: h.policy})
	root := filepath.Join(h.w.env.Getenv("TMPDIR"), commandGoTmpBasePrefix+itoa(os.Getuid()), "gotmp-1-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	h.w.trim.birth = func(string) time.Time { return h.now.Add(-30 * 24 * time.Hour) }
	h.w.trim.procs = func() (procSnapshot, error) { return procSnapshot{}, errors.New("pid 7: permission denied") }
	h.w.reaperOffTick(context.Background())
	if !exists(root) {
		t.Fatal("a root was removed without a /proc proof")
	}
	if !h.logged("could not prove any GOTMPDIR root dead") {
		t.Errorf("the failed proof is not logged:\n%s", h.logs)
	}
}

func TestCreatorAlive(t *testing.T) {
	born := time.Unix(1000, 0)
	s := procSnapshot{goStarts: []procStart{{PID: 1, Start: born.Add(time.Minute)}, {PID: 2, Start: born.Add(-time.Minute)}, {PID: 3, Start: born.Add(-time.Hour)}}}
	c, ok := s.creatorAlive(goBuildPrefix, born)
	if !ok || c.PID != 3 {
		t.Fatalf("creatorAlive = %+v %v, want the oldest go started before the birth (pid 3)", c, ok)
	}
	// Within a clock tick of the birth is not after it.
	s.goStarts = []procStart{{PID: 4, Start: born.Add(500 * time.Millisecond)}}
	if _, ok := s.creatorAlive(goBuildPrefix, born); !ok {
		t.Error("a go started within the slack of the birth was not counted a possible creator")
	}
	if _, ok := s.creatorAlive(goLinkPrefix, born); ok {
		t.Error("a go command was counted a linker")
	}
}

// The per-command roots proctree makes are the ones this pass reclaims.
func TestCommandGoTmpNamesMatchProctree(t *testing.T) {
	if commandGoTmpBasePrefix != proctree.GoTmpBasePrefix || commandGoTmpPrefix != proctree.GoTmpPrefix {
		t.Fatalf("diskwatch reclaims %s*/%s*, proctree makes %s*/%s*", commandGoTmpBasePrefix, commandGoTmpPrefix, proctree.GoTmpBasePrefix, proctree.GoTmpPrefix)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
