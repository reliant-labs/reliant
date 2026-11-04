// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
