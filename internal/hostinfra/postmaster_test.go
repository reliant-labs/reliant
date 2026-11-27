package hostinfra

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writePostmasterPID writes a postmaster.pid the way postgres lays it out:
// pid, data dir, start time, port, socket dir, listen address, shm key+id.
func writePostmasterPID(t *testing.T, dataDir string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dataDir, "data", "postmaster.pid")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDSNCarriesTheDeclaredCredentialsAndPort(t *testing.T) {
	spec := Spec{User: "app", Password: "s3cret", Port: 5499, Database: "appdb"}
	if got, want := spec.DSN(), "postgres://app:s3cret@localhost:5499/appdb?sslmode=disable"; got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
}

func TestPostmasterReadsThePidAndLiveness(t *testing.T) {
	dir := t.TempDir()
	self := writePostmasterPID(t, dir, strconv.Itoa(os.Getpid()))
	if pid, alive := postmaster(self); pid != os.Getpid() || !alive {
		t.Errorf("postmaster of a live pid = %d, %v", pid, alive)
	}
	for name, content := range map[string]string{"garbage": "not a pid\n", "zero": "0\n", "negative": "-4\n", "empty": ""} {
		p := filepath.Join(dir, name+".pid")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if pid, alive := postmaster(p); pid != 0 || alive {
			t.Errorf("%s: postmaster = %d, %v; want not running", name, pid, alive)
		}
	}
	if _, alive := postmaster(filepath.Join(dir, "missing.pid")); alive {
		t.Error("a missing pidfile reads as running")
	}
}

// runningPort distinguishes "our cluster is up where we expect", "up
// somewhere else", "up but mid-boot with no port yet", and "not running".
func TestRunningPortReadsTheFourthLineOfThePidFile(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	cases := []struct {
		name      string
		lines     []string
		wantPort  int
		wantAlive bool
	}{
		{"serving", []string{self, "/data", "1700000000", "5433", "/tmp", "127.0.0.1"}, 5433, true},
		{"mid-boot, no port line yet", []string{self, "/data"}, -1, true},
		{"unparseable port", []string{self, "/data", "1700000000", "oops"}, -1, true},
		{"non-positive port", []string{self, "/data", "1700000000", "0"}, -1, true},
		{"dead postmaster", []string{"999999999", "/data", "1", "5433"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePostmasterPID(t, dir, tc.lines...)
			port, alive := runningPort(dir)
			if port != tc.wantPort || alive != tc.wantAlive {
				t.Errorf("runningPort = %d, %v; want %d, %v", port, alive, tc.wantPort, tc.wantAlive)
			}
		})
	}
	if port, alive := runningPort(t.TempDir()); port != 0 || alive {
		t.Errorf("no cluster = %d, %v", port, alive)
	}
}

func TestPgVersionIsTheClustersOwnRecord(t *testing.T) {
	dir := t.TempDir()
	if got := pgVersion(dir); got != "" {
		t.Errorf("no cluster yet = %q, want empty (not the declared version)", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "PG_VERSION"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pgVersion(dir); got != "16" {
		t.Errorf("pgVersion = %q, want 16", got)
	}
}

func TestStatusPostgresOfANeverStartedAndAForeignInstance(t *testing.T) {
	ctx := context.Background()
	free := Spec{Name: "pg", Engine: EnginePostgres, Port: freeTCPPort(t), User: "u", Password: "p"}
	dir := t.TempDir()
	st, err := StatusOf(ctx, dir, free)
	if err != nil || st.Running || st.Foreign || st.Ready || st.DataDir != free.dataDir(dir) {
		t.Errorf("never started: %+v, %v", st, err)
	}

	// A live postmaster of ours: running, on the port the pidfile names; a
	// listener that is not postgres means it does not ANSWER (not ready).
	held := holdPort(t)
	ours := Spec{Name: "pg", Engine: EnginePostgres, Port: held, User: "u", Password: "p"}
	writePostmasterPID(t, ours.dataDir(dir), strconv.Itoa(os.Getpid()), "/data", "1", strconv.Itoa(held))
	if err := os.WriteFile(filepath.Join(ours.dataDir(dir), "data", "PG_VERSION"), []byte("15\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = StatusOf(ctx, dir, ours)
	if err != nil || !st.Running || st.Port != held || st.Ready || st.Version != "15" {
		t.Errorf("live postmaster that does not answer: %+v, %v", st, err)
	}

	// Something that is not postgres on the declared port, no postmaster of
	// ours: foreign, for a human to resolve.
	other := Spec{Name: "other", Engine: EnginePostgres, Port: held, User: "u", Password: "p"}
	st, err = StatusOf(ctx, t.TempDir(), other)
	if err != nil || st.Running || !st.Foreign {
		t.Errorf("foreign holder: %+v, %v", st, err)
	}
}

// Stale SysV shared-memory segments from a SIGKILLed postmaster are
// reclaimed on the way in, but only from instances that are not running and
// whose pidfile has sat untouched.
func TestReapStaleRuntimesLeavesLiveAndFreshInstancesAlone(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, DefaultDataDirRel)
	mk := func(name string, pid string, age time.Duration) string {
		dir := filepath.Join(root, name)
		p := writePostmasterPID(t, dir, pid, "/d", "1", "5433", "/tmp", "*", "123 999999999")
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	live := mk("live", strconv.Itoa(os.Getpid()), 2*time.Hour)
	fresh := mk("fresh", "999999999", time.Minute)
	stale := mk("stale", "999999999", 2*time.Hour)
	if err := os.WriteFile(filepath.Join(root, "a-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "no-cluster"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Hygiene only: nothing is removed from disk, nothing errors, and a
	// project with no hostinfra directory is a no-op.
	reapStaleRuntimes(project)
	reapStaleRuntimes(t.TempDir())
	for _, p := range []string{live, fresh, stale} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("reapStaleRuntimes removed %s: %v", p, err)
		}
	}
}

func TestReclaimShmSegmentToleratesEveryMalformedPidFile(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing line":   "1\n2\n",
		"one field":      "1\n2\n3\n4\n5\n6\n123\n",
		"non-numeric id": "1\n2\n3\n4\n5\n6\n123 abc\n",
		"zero id":        "1\n2\n3\n4\n5\n6\n123 0\n",
		"unused id":      "1\n2\n3\n4\n5\n6\n123 999999999\n",
	}
	for name, body := range cases {
		p := filepath.Join(dir, name+".pid")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		reclaimShmSegment(p) // must not panic or fail the boot
	}
	reclaimShmSegment(filepath.Join(dir, "absent.pid"))
}

func TestShortPathAndCacheDir(t *testing.T) {
	proj := filepath.Join("a", "proj")
	if got := shortPath(proj, filepath.Join(proj, ".forge", "x")); got != filepath.Join(".forge", "x") {
		t.Errorf("inside the project = %q", got)
	}
	outside := filepath.Join("a", "elsewhere", "x")
	if got := shortPath(proj, outside); got != outside {
		t.Errorf("outside the project = %q, want it unchanged", got)
	}
	if c := cacheDir(); c != "" && filepath.Base(c) != "embedded-postgres" {
		t.Errorf("cacheDir = %q", c)
	}
}

func TestSameDirResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	if !sameDir(dir, link) {
		t.Error("a symlink to a directory is that directory")
	}
	if sameDir(dir, t.TempDir()) {
		t.Error("two different directories compared equal")
	}
	missingA, missingB := filepath.Join(dir, "nope"), filepath.Join(dir, "nope", "..", "nope")
	if !sameDir(missingA, missingB) {
		t.Error("unresolvable paths fall back to a lexical comparison")
	}
}

// holdPort listens on a free loopback port and returns it: a stand-in for
// "something else is on the declared port".
func holdPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}
