package pgtest_test

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// These tests boot real embedded postgres instances that overlap in time —
// in one process and across two — because the defect they pin only exists
// between concurrent starts: embedded-postgres creates one
// $TMPDIR/embedded_postgres_log* file per Start and REOPENS it by name on
// every flush (Start, Stop). A Start that deleted files it merely saw appear
// during its own window therefore broke whichever concurrent instance's file
// it caught: "unable to process postgres logs: open …: no such file".

const (
	helperEnv   = "FORGE_PGTEST_EMBEDDED_HELPER"
	helperTmp   = "FORGE_PGTEST_EMBEDDED_HELPER_TMP"
	helperReady = "EMBEDDED-READY"
)

// privateTmp points os.TempDir() at a directory only this test uses, so the
// log files visible in it are exactly the ones these instances create. Short,
// under /tmp: the sockets postgres opens have a path length limit.
func privateTmp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pgl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	return dir
}

func logsIn(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "embedded_postgres_log*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// instanceConfig is a self-contained embedded-postgres config: its own port,
// its own runtime dir under root, and a NON-NIL logger — the logger is what
// makes the library reopen its log file by name on every flush.
func instanceConfig(t testing.TB, root, name string, logger *bytes.Buffer) embeddedpostgres.Config {
	t.Helper()
	port, err := pgtest.ReserveLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.PostgresVersion(pgtest.DefaultVersion)).
		Port(uint32(port)).
		RuntimePath(filepath.Join(root, name)).
		Locale("C").
		Encoding("UTF8").
		StartParameters(map[string]string{
			"shared_buffers":             "32MB",
			"fsync":                      "off",
			"dynamic_shared_memory_type": "mmap",
			"shared_memory_type":         "mmap",
		}).
		StartTimeout(120 * time.Second).
		Logger(logger)
	if c, err := os.UserCacheDir(); err == nil {
		cfg = cfg.CachePath(filepath.Join(c, "forge", "embedded-postgres"))
	}
	return cfg
}

// startRetryingPort starts an instance, building a fresh config (and so a
// fresh port) again if another process on this busy machine claimed the
// reserved port between its reservation and postgres binding it. That is the
// only error retried: anything else is the behaviour under test.
func startRetryingPort(mk func() embeddedpostgres.Config) (*pgtest.Embedded, error) {
	var err error
	for range 5 {
		var ep *pgtest.Embedded
		ep, err = pgtest.StartEmbedded(mk())
		if err == nil || !strings.Contains(err.Error(), "process already listening on port") {
			return ep, err
		}
	}
	return nil, err
}

// waitForLog blocks until at least one log file exists in dir.
func waitForLog(t *testing.T, dir string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(logsIn(t, dir)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no embedded_postgres_log file appeared in %s within %s", dir, within)
}

// TestStartEmbedded_ConcurrentInstancesInOneProcess: B starts inside A's
// start window (A is still in initdb when B creates its log). A finishing
// must not take B's log with it, so both Stop cleanly while the other runs,
// and no log is left behind afterwards.
func TestStartEmbedded_ConcurrentInstancesInOneProcess(t *testing.T) {
	testkit.Slow(t, "boots two real embedded postgres servers")
	tmp := privateTmp(t)
	root := filepath.Join(tmp, "rt")

	var logA, logB bytes.Buffer
	mkA := func() embeddedpostgres.Config { return instanceConfig(t, root, "a", &logA) }
	mkB := func() embeddedpostgres.Config { return instanceConfig(t, root, "b", &logB) }

	type result struct {
		ep  interface{ Stop() error }
		err error
	}
	aDone := make(chan result, 1)
	go func() {
		ep, err := startRetryingPort(mkA)
		if err != nil {
			aDone <- result{err: err}
			return
		}
		aDone <- result{ep: ep}
	}()

	// A has created its log and is somewhere in initdb; B starts now.
	waitForLog(t, tmp, 30*time.Second)
	epB, errB := startRetryingPort(mkB)
	resA := <-aDone
	if resA.err != nil {
		t.Fatalf("instance A: Start: %v", resA.err)
	}
	if errB != nil {
		_ = resA.ep.Stop()
		t.Fatalf("instance B: Start: %v", errB)
	}

	// Stop A while B keeps running, then B.
	if err := resA.ep.Stop(); err != nil {
		_ = epB.Stop()
		t.Fatalf("instance A: Stop: %v", err)
	}
	if err := epB.Stop(); err != nil {
		t.Fatalf("instance B: Stop: %v (its log file was removed while it ran)", err)
	}

	if left := logsIn(t, tmp); len(left) != 0 {
		t.Errorf("log files left after both instances stopped: %v", left)
	}
}

// TestStartEmbedded_ConcurrentInstancesAcrossProcesses: the same overlap with
// the second instance in another process, which is how the reliant test
// suite and a sibling `go test` package met it. The child boots inside this
// process's start window and must still Stop cleanly after the parent's
// Start has finished.
func TestStartEmbedded_ConcurrentInstancesAcrossProcesses(t *testing.T) {
	testkit.Slow(t, "boots two real embedded postgres servers, one in a child process")
	tmp := privateTmp(t)
	root := filepath.Join(tmp, "rt")

	var parentLog bytes.Buffer
	mk := func() embeddedpostgres.Config { return instanceConfig(t, root, "parent", &parentLog) }

	type result struct {
		ep  interface{ Stop() error }
		err error
	}
	parentDone := make(chan result, 1)
	go func() {
		ep, err := startRetryingPort(mk)
		if err != nil {
			parentDone <- result{err: err}
			return
		}
		parentDone <- result{ep: ep}
	}()
	waitForLog(t, tmp, 30*time.Second)

	child := exec.Command(os.Args[0], "-test.run=^TestEmbeddedChildProcess$", "-test.v")
	child.Env = append(os.Environ(), helperEnv+"=1", helperTmp+"="+root, "TMPDIR="+tmp)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var childErr bytes.Buffer
	child.Stderr = &childErr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	var childOut strings.Builder
	ready := make(chan struct{})
	var readyOnce sync.Once
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			childOut.WriteString(line + "\n")
			if strings.Contains(line, helperReady) {
				readyOnce.Do(func() { close(ready) })
			}
		}
		childDone <- child.Wait()
	}()
	stopChild := func() error {
		_ = stdin.Close()
		select {
		case err := <-childDone:
			return err
		case <-time.After(120 * time.Second):
			_ = child.Process.Kill()
			return fmt.Errorf("child did not exit")
		}
	}

	res := <-parentDone
	if res.err != nil {
		_ = stopChild()
		t.Fatalf("parent: Start: %v", res.err)
	}
	select {
	case <-ready:
	case err := <-childDone:
		_ = res.ep.Stop()
		t.Fatalf("child exited before it was ready (%v):\n%s\n%s", err, childOut.String(), childErr.String())
	case <-time.After(120 * time.Second):
		_ = child.Process.Kill()
		_ = res.ep.Stop()
		t.Fatalf("child never became ready:\n%s\n%s", childOut.String(), childErr.String())
	}

	// The parent's Start is long over; the child is running. Stop the
	// parent, then the child, each while the other is still up.
	if err := res.ep.Stop(); err != nil {
		_ = stopChild()
		t.Fatalf("parent: Stop: %v", err)
	}
	if err := stopChild(); err != nil {
		t.Fatalf("child failed to Stop cleanly (%v) — its log file was removed by the other process:\n%s\n%s",
			err, childOut.String(), childErr.String())
	}
	if left := logsIn(t, tmp); len(left) != 0 {
		t.Errorf("log files left after both processes stopped: %v", left)
	}
}

// TestEmbeddedChildProcess is the second process of the test above. Skipped
// unless launched by it.
func TestEmbeddedChildProcess(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper process; launched by TestStartEmbedded_ConcurrentInstancesAcrossProcesses")
	}
	var log bytes.Buffer
	ep, err := startRetryingPort(func() embeddedpostgres.Config {
		return instanceConfig(t, os.Getenv(helperTmp), "child", &log)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "child Start: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(helperReady)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // returns at EOF: the parent is done
	if err := ep.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "child Stop: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
