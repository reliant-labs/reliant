//go:build !windows

package hostinfra

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// zitadelFixture is a project that can boot the stand-in IdP: a postgres
// server for the IdP's database, the declarative bootstrap file, an isolated
// cache and PAT home, and the stand-in binary installed where forge caches
// the pinned one.
type zitadelFixture struct {
	project string
	spec    Spec
	home    string
}

func newZitadelFixture(t *testing.T) zitadelFixture {
	t.Helper()
	testkit.Slow(t, "boots a real postgres and a stand-in IdP process")
	isolateCache(t)
	home := withIDPHome(t)
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pgPort, _ := strconv.Atoi(u.Port())
	pass, _ := u.User.Password()

	installFake(t, ZitadelBinaryPath(), "zitadel", "zitadel version "+ZitadelVersion)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "idp-steps.yaml"), []byte("FirstInstance: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Name: "idp", Env: "dev", Engine: EngineZitadel, Port: freeTCPPort(t),
		User: u.User.Username(), Password: pass,
		IDPDatabase: "zitadel_idp", IDPDatabasePort: pgPort, IDPMasterKey: strings.Repeat("k", 32),
	}
	reapPIDFile2(t, filepath.Join(spec.dataDir(project), zitadelPIDFile))
	t.Cleanup(func() { _, _ = stopZitadel(project, spec) })
	return zitadelFixture{project: project, spec: spec, home: home}
}

// reapPIDFile2 is reapPIDFile for the zitadel pidfile, whose pid is the
// first field of the same "<pid> <port>" format.
func reapPIDFile2(t *testing.T, pidFile string) { reapPIDFile(t, pidFile) }

func TestZitadelLifecycleWithAStandInBinary(t *testing.T) {
	f := newZitadelFixture(t)
	ctx := context.Background()

	var err error
	out := captureStdout(t, func() { err = Start(ctx, f.project, f.spec) })
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, out)
	}
	if !strings.Contains(out, "zitadel "+ZitadelVersion+" starting on :") || !strings.Contains(out, "ready on :") {
		t.Errorf("Start should report the launch and ready state:\n%s", out)
	}

	// The IdP's database was created on the app's postgres, and the PAT the
	// first boot wrote beside it was copied to the declared checkout path.
	if err := ensureNamedDatabase(f.spec.zitadelDSNBase(), f.spec.IDPDatabase); err != nil {
		t.Errorf("the IdP database should exist already: %v", err)
	}
	loc := f.spec.IDPPATLocations(f.project)
	if b, err := os.ReadFile(loc.Shared); err != nil || string(b) != "fake-pat" {
		t.Errorf("shared PAT = %q, %v", b, err)
	}
	if b, err := os.ReadFile(loc.Checkout); err != nil || string(b) != "fake-pat" {
		t.Errorf("checkout PAT = %q, %v; want the new token mirrored", b, err)
	}
	if got := loc.Find(); got != loc.Shared {
		t.Errorf("Find = %q", got)
	}

	st, err := StatusOf(ctx, f.project, f.spec)
	if err != nil || !st.Running || !st.Ready || st.Port != f.spec.Port || st.Version != ZitadelVersion || st.Foreign {
		t.Fatalf("StatusOf = %+v, %v", st, err)
	}

	// Our own server is adopted, never started twice.
	out = captureStdout(t, func() { err = Start(ctx, f.project, f.spec) })
	if err != nil || !strings.Contains(out, "already ready on :") {
		t.Errorf("a second Start: %v\n%s", err, out)
	}

	pid, _, _ := readZitadelPID(f.spec.dataDir(f.project))
	stopped, err := StopReport(f.project, f.spec)
	if err != nil || !stopped {
		t.Fatalf("StopReport = %v, %v", stopped, err)
	}
	if processAlive(pid) {
		t.Error("the IdP is still alive after stop")
	}
	if st, _ := StatusOf(ctx, f.project, f.spec); st.Running {
		t.Errorf("StatusOf after stop = %+v", st)
	}
	if stopped, err := StopReport(f.project, f.spec); stopped || err != nil {
		t.Errorf("a second stop = %v, %v", stopped, err)
	}
}

func TestStartZitadelRefusesAForeignHolderAndAMissingBootstrap(t *testing.T) {
	f := newZitadelFixture(t)
	ctx := context.Background()

	missing := f.spec
	missing.IDPStepsFile = "no-such-steps.yaml"
	err := Start(ctx, f.project, missing)
	if err == nil || !strings.Contains(err.Error(), "the declarative bootstrap") || !strings.Contains(err.Error(), "is missing") {
		t.Errorf("a missing bootstrap file: %v", err)
	}

	held := f.spec
	held.Port = holdPort(t)
	err = Start(ctx, f.project, held)
	if err == nil || !strings.Contains(err.Error(), "held by something this project did not start") {
		t.Errorf("a foreign holder: %v", err)
	}
	st, err := StatusOf(ctx, f.project, held)
	if err != nil || st.Running || !st.Foreign {
		t.Errorf("StatusOf with a foreign holder = %+v, %v", st, err)
	}
}

func TestStartZitadelReportsAnIdPThatDiesDuringBoot(t *testing.T) {
	f := newZitadelFixture(t)
	t.Setenv(fakeExitEnv, "1")
	var err error
	captureStdout(t, func() { err = Start(context.Background(), f.project, f.spec) })
	if err == nil || !strings.Contains(err.Error(), "zitadel exited during startup") || !strings.Contains(err.Error(), "logs: ") {
		t.Errorf("err = %v", err)
	}
}

func TestStartZitadelNamesAnUnreachableDatabase(t *testing.T) {
	f := newZitadelFixture(t)
	spec := f.spec
	spec.IDPDatabasePort = 1 // nothing listens
	spec.IDPDatabase = ""
	err := Start(context.Background(), f.project, spec)
	if err == nil || !strings.Contains(err.Error(), "prepare the IdP's database") || !strings.Contains(err.Error(), "no database name declared") {
		t.Errorf("err = %v", err)
	}
}

func TestWaitZitadelReady(t *testing.T) {
	spec := Spec{Port: freeTCPPort(t)}
	if err := waitZitadelReady(context.Background(), spec, 0, time.Second); err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("a dead process: %v", err)
	}
	if err := waitZitadelReady(context.Background(), spec, os.Getpid(), 300*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not report ready") {
		t.Errorf("a live process that never answers: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitZitadelReady(ctx, spec, os.Getpid(), time.Minute); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("a cancelled wait: %v", err)
	}
}

func TestStopZitadelLeavesProcessesThatAreNotOurs(t *testing.T) {
	isolateCache(t)
	project := t.TempDir()
	spec := Spec{Name: "idp", Engine: EngineZitadel, Port: 1}
	dataDir := spec.dataDir(project)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// This test process is alive but is not the cached zitadel binary: a
	// recycled pid, which must never be signalled.
	if err := writeZitadelPID(dataDir, os.Getpid(), 1); err != nil {
		t.Fatal(err)
	}
	stopped, err := stopZitadel(project, spec)
	if stopped || err != nil {
		t.Errorf("stopZitadel of a foreign pid = %v, %v", stopped, err)
	}
	if _, _, ok := readZitadelPID(dataDir); ok {
		t.Error("the stale pidfile should be cleared")
	}
	if stopped, err := stopZitadel(project, spec); stopped || err != nil {
		t.Errorf("stop with no pidfile = %v, %v", stopped, err)
	}
}

func TestStatusZitadelOfAForeignListener(t *testing.T) {
	spec := Spec{Name: "idp", Engine: EngineZitadel, Port: holdPort(t)}
	st := statusZitadel(context.Background(), spec, t.TempDir())
	if st.Running || !st.Foreign || st.Version != ZitadelVersion {
		t.Errorf("statusZitadel = %+v", st)
	}
}

func TestIdentifyZitadelHolderTreatsAMidBootInstanceAsOurs(t *testing.T) {
	spec := Spec{Port: freeTCPPort(t)}
	dataDir := t.TempDir()
	if got := identifyZitadelHolder(context.Background(), spec, dataDir); got != holderNone {
		t.Errorf("nothing running = %v", got)
	}
	if err := writeZitadelPID(dataDir, os.Getpid(), spec.Port); err != nil {
		t.Fatal(err)
	}
	if got := identifyZitadelHolder(context.Background(), spec, dataDir); got != holderOurs {
		t.Errorf("a recorded live pid on the declared port that is not ready yet = %v, want ours (it is mid-boot)", got)
	}
}

func TestEnsureNamedDatabaseCreatesOnceAndSurvivesRetries(t *testing.T) {
	f := newZitadelFixture(t)
	dsn := f.spec.zitadelDSNBase()
	for i := 0; i < 2; i++ {
		if err := ensureNamedDatabase(dsn, "idp_second"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := ensureNamedDatabase(dsn, ""); err == nil || !strings.Contains(err.Error(), "no database name declared") {
		t.Errorf("an empty name: %v", err)
	}
	if err := ensureNamedDatabase(dsn, "bad\x00name"); err == nil {
		t.Error("an unusable name must fail rather than be created")
	}
}
