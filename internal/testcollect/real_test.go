package testcollect

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A real `go build -cover -covermode=atomic` server, end to end: a live pull
// is converted by `go tool covdata`, --reset zeroes it and fences off the
// past, and once the server drains on SIGTERM its exit flush is what the next
// collect reads.
func TestCollect_RealCoverBuild(t *testing.T) {
	testkit.Slow(t, "compiles testdata/covserver with -cover, runs it and converts its covdata with go tool covdata")
	f := newFixture(t)
	bin := filepath.Join(t.TempDir(), "covserver")
	if out, err := exec.Command("go", "build", "-cover", "-covermode=atomic", "-o", bin, "./testdata/covserver").CombinedOutput(); err != nil {
		t.Fatalf("go build -cover: %v\n%s", err, out)
	}
	cover := filepath.Join(f.root, ".forge", "tests", "covdata", "dev", "api")
	if err := os.MkdirAll(cover, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cover)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr := ""
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if a, ok := strings.CutPrefix(sc.Text(), "ADDR "); ok {
			addr = a
			break
		}
	}
	if addr == "" {
		t.Fatal("covserver printed no address")
	}
	get := func(path string) {
		t.Helper()
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	hit := func() { get("/hit") }

	target := Target{Workload: "api", Runtime: "host", Host: &HostTarget{PullAddr: addr, CoverDir: cover}}
	collect := func(reset bool) *Document {
		t.Helper()
		opts := f.options(target)
		opts.Convert = nil // the real `go tool covdata textfmt`
		opts.Reset = reset
		doc, err := collectErr(context.Background(), opts)
		if err != nil {
			t.Fatalf("collect (reset=%v): %v", reset, err)
		}
		return doc
	}

	hit()
	wl := collect(false).Workloads[0]
	if wl.State != StateCollected || wl.Build.Coverage == nil || wl.Build.Coverage.Covered == 0 || wl.Build.Coverage.Statements == 0 {
		t.Fatalf("live pull: %+v build %+v, want converted coverage with statements hit", wl, wl.Build)
	}
	liveCovered := wl.Build.Coverage.Covered

	if wl := collect(true).Workloads[0]; wl.State != StateReset {
		t.Fatalf("reset: %+v", wl)
	}
	// The reset changed nothing in the process: it is the baseline. With no
	// traffic since, only what the pulls themselves ran (serverkit's
	// handler, instrumented with the rest of the module) is counted; main's
	// setup and /hit, which ran before the reset, are not.
	wl = collect(false).Workloads[0]
	if wl.Build.SinceReset == nil || wl.Build.Coverage == nil {
		t.Fatalf("right after the reset: %+v, want the baseline subtracted", wl.Build)
	}
	idle := wl.Build.Coverage.Covered
	if idle >= liveCovered {
		t.Fatalf("right after the reset: %d covered, want fewer than the %d before it", idle, liveCovered)
	}

	// /other runs after the reset, and is counted on top of the pulls' own.
	get("/other")
	wl = collect(false).Workloads[0]
	delta := wl.Build.Coverage.Covered
	if delta <= idle {
		t.Fatalf("after /other: %d covered since the reset, want more than the %d of the pulls alone", delta, idle)
	}

	// The endpoint is read-only: a POST (once the reset) clears nothing.
	resp, err := http.Post("http://"+addr+CoveragePath, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST %s answered %d, want 405", CoveragePath, resp.StatusCode)
	}
	// Cleared counters would fall below the baseline, which then would not
	// apply (the process would read as restarted).
	if wl := collect(false).Workloads[0]; wl.Build.SinceReset == nil || wl.Build.Coverage.Covered < delta {
		t.Errorf("after the POST: since-reset %+v, %d covered, want the baseline still applying and at least %d: nothing was cleared", wl.Build.SinceReset, wl.Build.Coverage.Covered, delta)
	}

	hit()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("covserver did not drain cleanly: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("covserver did not exit on SIGTERM")
	}
	// The exit flush holds the process's counts since it started; the
	// baseline (same build, same pid) still takes off the time before the
	// reset.
	wl = collect(false).Workloads[0]
	if wl.State != StateCollected || wl.Build.CounterFiles != 1 || wl.Build.Coverage == nil || wl.Build.Coverage.Covered == 0 {
		t.Fatalf("exit flush: %+v build %+v, want the one counter file Go wrote at exit", wl, wl.Build)
	}
	if wl.Build.SinceReset == nil || wl.Build.SinceReset.Processes != 1 {
		t.Errorf("exit flush: since-reset %+v, want the process's baseline subtracted from its exit flush", wl.Build.SinceReset)
	}
	for _, s := range wl.Sources {
		if s.Kind == SourcePull && s.Status != SourceUnreachable {
			t.Errorf("pull after exit: %+v, want unreachable", s)
		}
		if s.Kind == SourceExitFlush && (s.Status != SourceOK || s.Files != 2) {
			t.Errorf("exit flush: %+v, want the meta and the one counter file written after the reset", s)
		}
	}
}
