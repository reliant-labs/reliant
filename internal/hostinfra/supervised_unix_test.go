//go:build !windows

package hostinfra

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

const fakeEngineScript = "#!/bin/sh\necho " + fakeEngineVerStr + "\n"

// tarGz builds a release archive holding the named files.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRelease is a pinned release of the stand-in engine whose archive is
// served from base. sums pins the checksum of this platform's asset.
func fakeRelease(base string, sums string) binaryRelease {
	asset := func(goos, goarch string) string { return "fakeeng-" + goos + "-" + goarch + ".tar.gz" }
	rel := binaryRelease{
		engine:        fakeEngineName,
		version:       "1.0.0",
		asset:         asset,
		url:           func(a string) string { return base + "/" + a },
		binary:        func(string) string { return fakeEngineName },
		versionArgs:   []string{"--version"},
		versionNeedle: fakeEngineVerStr,
		approxSize:    "~1 KB",
		checksums:     map[string]string{},
	}
	if sums != "" {
		rel.checksums[asset(runtime.GOOS, runtime.GOARCH)] = sums
	}
	return rel
}

func TestEnsureDownloadsVerifiesAndCachesTheBinary(t *testing.T) {
	isolateCache(t)
	archive := tarGz(t, map[string]string{"fakeeng-1/LICENSE": "x", "fakeeng-1/" + fakeEngineName: fakeEngineScript})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	rel := fakeRelease(srv.URL, sha(archive))

	var bin string
	out := captureStdout(t, func() {
		var err error
		if bin, err = rel.ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if bin != rel.binaryPath() {
		t.Errorf("ensure = %q, want the versioned cache path %q", bin, rel.binaryPath())
	}
	if !strings.Contains(out, "fetching fakeeng 1.0.0") {
		t.Errorf("the one-time download should be announced:\n%s", out)
	}
	if err := rel.verify(bin); err != nil {
		t.Errorf("the extracted binary does not verify: %v", err)
	}
	// Cached: a second call re-verifies by running it and never reaches the server.
	if again, err := rel.ensure(context.Background()); err != nil || again != bin || hits.Load() != 1 {
		t.Errorf("second ensure = %q, %v after %d download(s); want the cached binary, one download", again, err, hits.Load())
	}
}

func TestEnsureRefusesADownloadItCannotTrust(t *testing.T) {
	good := tarGz(t, map[string]string{fakeEngineName: fakeEngineScript})
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		sum         string
		want        string
		unavailable bool
	}{
		{"checksum mismatch", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("substituted")) }, sha(good), "checksum mismatch", false},
		{"not found is a wrong pin", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, sha(good), "HTTP 404", false},
		{"server error is an outage", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, sha(good), "HTTP 503", true},
		{"wrong version after extraction", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(tarGz(t, map[string]string{fakeEngineName: "#!/bin/sh\necho other 9.9\n"}))
		}, "", "did not verify", false},
		{"archive without the executable", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(tarGz(t, map[string]string{"README": "x"}))
		}, "", "contained no", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCache(t)
			var served []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.handler(w, r)
			}))
			defer srv.Close()
			sum := tc.sum
			if sum == "" {
				// The archive is checksum-clean, so the failure is its content.
				resp, err := http.Get(srv.URL + "/x")
				if err != nil {
					t.Fatal(err)
				}
				buf := new(bytes.Buffer)
				_, _ = buf.ReadFrom(resp.Body)
				_ = resp.Body.Close()
				served = buf.Bytes()
				sum = sha(served)
			}
			rel := fakeRelease(srv.URL, sum)
			_, err := rel.ensure(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if IsDownloadUnavailable(err) != tc.unavailable {
				t.Errorf("IsDownloadUnavailable = %v, want %v", IsDownloadUnavailable(err), tc.unavailable)
			}
			if _, statErr := os.Stat(rel.binaryPath()); statErr == nil && tc.want != "did not verify" {
				t.Error("a refused download left a binary at the path the next run will trust")
			}
		})
	}
}

func TestEnsureReportsAnUnreachableServerAsUnavailable(t *testing.T) {
	isolateCache(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	_, err := fakeRelease(base, "abc").ensure(context.Background())
	if err == nil || !IsDownloadUnavailable(err) {
		t.Fatalf("err = %v, want a download-unavailable error", err)
	}
	var unavailable *downloadUnavailableError
	if !errors.As(err, &unavailable) || errors.Unwrap(unavailable) == nil {
		t.Errorf("the unavailable error should wrap its cause: %v", err)
	}
}

func TestEnsureRefusesAPlatformWithNoPinnedChecksum(t *testing.T) {
	isolateCache(t)
	_, err := fakeRelease("http://unused.invalid", "").ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "publishes no binary for "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("err = %v", err)
	}
}

func TestEnsureNeedsAUserCacheDirectory(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	rel := fakeRelease("http://unused.invalid", "abc")
	if rel.binaryPathFor("linux", "amd64") != "" {
		t.Skip("this OS has a cache directory without HOME")
	}
	if _, err := rel.ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot determine the user cache directory") {
		t.Errorf("err = %v", err)
	}
	if got := zitadelBinaryPathFor("linux", "amd64"); got != "" {
		t.Errorf("zitadelBinaryPathFor without a cache dir = %q, want empty", got)
	}
	if _, err := ensureZitadelBinary(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot determine the user cache directory") {
		t.Errorf("ensureZitadelBinary err = %v", err)
	}
}

func TestVerifyRejectsWhatItCannotRunOrRecognise(t *testing.T) {
	rel := fakeRelease("", "")
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	if err := rel.verify(absent); err == nil {
		t.Error("a missing binary verified")
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rel.verify(plain); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("a non-executable file: %v", err)
	}
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil { //nolint:gosec // a test stand-in
		t.Fatal(err)
	}
	if err := rel.verify(broken); err == nil || !strings.Contains(err.Error(), "run ") {
		t.Errorf("a binary that fails its version probe: %v", err)
	}
	wrong := filepath.Join(dir, "wrong")
	if err := os.WriteFile(wrong, []byte("#!/bin/sh\necho other 2.0\n"), 0o755); err != nil { //nolint:gosec // a test stand-in
		t.Fatal(err)
	}
	if err := rel.verify(wrong); err == nil || !strings.Contains(err.Error(), "expected version "+fakeEngineVerStr) {
		t.Errorf("a binary of another version: %v", err)
	}
}

// engineCase is one supervised engine the real supervisor can drive with the
// stand-in binary.
type engineCase struct {
	name    string
	engine  string
	rel     binaryRelease
	sup     supervisedEngine
	version string
}

var engineCases = []engineCase{
	{"nats", EngineNATS, natsRelease, natsEngine, NATSVersion},
	{"temporal", EngineTemporal, temporalRelease, temporalEngine, temporalRelease.versionNeedle},
}

// The whole supervised lifecycle against a real child process: start, ready,
// idempotent restart, a moved port refused, stop, and stop again.
func TestSupervisedEngineLifecycleWithAStandInBinary(t *testing.T) {
	for _, tc := range engineCases {
		t.Run(tc.name, func(t *testing.T) {
			testkit.Slow(t, "launches a real child process and polls its readiness")
			isolateCache(t)
			installFake(t, tc.rel.binaryPath(), "serve", tc.version)
			project := t.TempDir()
			port := freeTCPPort(t)
			spec := Spec{Name: "svc", Env: "dev", Engine: tc.engine, Port: port, TemporalMetricsPort: 0}
			ctx := context.Background()
			reapPIDFile(t, tc.sup.pidFile(spec.dataDir(project)))
			t.Cleanup(func() { _, _ = StopReport(project, spec) })

			var startErr error
			out := captureStdout(t, func() { startErr = Start(ctx, project, spec) })
			if startErr != nil {
				t.Fatalf("Start: %v", startErr)
			}
			if !strings.Contains(out, "starting on :") || !strings.Contains(out, "ready on :") {
				t.Errorf("Start should report the launch and the ready state:\n%s", out)
			}
			st, err := StatusOf(ctx, project, spec)
			if err != nil || !st.Running || !st.Ready || st.Port != port || st.Foreign || st.Version != tc.rel.version || st.DataDir != spec.dataDir(project) {
				t.Fatalf("StatusOf = %+v, %v", st, err)
			}

			out = captureStdout(t, func() { startErr = Start(ctx, project, spec) })
			if startErr != nil || !strings.Contains(out, "already ready on :") {
				t.Errorf("a second Start must adopt its own server: %v\n%s", startErr, out)
			}

			moved := spec
			moved.Port = freeTCPPort(t)
			err = Start(ctx, project, moved)
			if err == nil || !strings.Contains(err.Error(), "is already running on :") || !strings.Contains(err.Error(), "forge env down") {
				t.Errorf("a moved port must be refused with the fix named: %v", err)
			}

			stopped, err := StopReport(project, spec)
			if err != nil || !stopped {
				t.Fatalf("StopReport = %v, %v", stopped, err)
			}
			if portListening(port) {
				t.Error("the port is still held after stop")
			}
			if st, _ := StatusOf(ctx, project, spec); st.Running || st.Ready {
				t.Errorf("StatusOf after stop = %+v", st)
			}
			if stopped, err := StopReport(project, spec); stopped || err != nil {
				t.Errorf("a second stop = %v, %v; want nothing to stop", stopped, err)
			}
		})
	}
}

func TestSupervisedEngineStartRefusesAPortSomethingElseHolds(t *testing.T) {
	isolateCache(t)
	port := holdPort(t)
	project := t.TempDir()
	spec := Spec{Name: "svc", Engine: EngineNATS, Port: port}
	err := Start(context.Background(), project, spec)
	if err == nil || !strings.Contains(err.Error(), "held by something this project did not start") {
		t.Fatalf("err = %v", err)
	}
	st, err := StatusOf(context.Background(), project, spec)
	if err != nil || st.Running || !st.Foreign {
		t.Errorf("StatusOf = %+v, %v; want Foreign (a human must free the port)", st, err)
	}
	if st, _ := StatusOf(context.Background(), project, Spec{Name: "free", Engine: EngineNATS, Port: freeTCPPort(t)}); st.Running || st.Foreign {
		t.Errorf("nothing on the port: %+v", st)
	}
}

func TestSupervisedEngineReportsAServerThatDiesDuringStartup(t *testing.T) {
	testkit.Slow(t, "launches a real child process that crashes")
	isolateCache(t)
	installFake(t, natsRelease.binaryPath(), "serve", NATSVersion)
	t.Setenv(fakeExitEnv, "1")
	project := t.TempDir()
	spec := Spec{Name: "svc", Env: "dev", Engine: EngineNATS, Port: freeTCPPort(t)}
	reapPIDFile(t, natsEngine.pidFile(spec.dataDir(project)))
	var err error
	captureStdout(t, func() { err = Start(context.Background(), project, spec) })
	if err == nil || !strings.Contains(err.Error(), "exited during startup") || !strings.Contains(err.Error(), "logs: "+filepath.Join(".forge", "logs", "dev", "svc.log")) {
		t.Errorf("err = %v, want the crash and the log named", err)
	}
}

func sleepEngine(ready func(context.Context, Spec, string) bool, timeout time.Duration) supervisedEngine {
	return supervisedEngine{
		release:      fakeRelease("", ""),
		args:         func(Spec, string) []string { return []string{"run"} },
		ready:        ready,
		readyTimeout: timeout,
	}
}

func TestSupervisedEngineStartGivesUpWhenNeverReadyOrCancelled(t *testing.T) {
	testkit.Slow(t, "launches a real child process and waits out a readiness deadline")
	for name, tc := range map[string]struct {
		ctxTimeout time.Duration
		want       string
	}{
		"deadline":  {0, "did not report ready on :"},
		"cancelled": {50 * time.Millisecond, "context deadline exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			isolateCache(t)
			eng := sleepEngine(func(context.Context, Spec, string) bool { return false }, 300*time.Millisecond)
			installFake(t, eng.release.binaryPath(), "sleep", fakeEngineVerStr)
			project := t.TempDir()
			spec := Spec{Name: "svc", Env: "dev", Engine: "x", Port: freeTCPPort(t)}
			reapPIDFile(t, eng.pidFile(spec.dataDir(project)))
			t.Cleanup(func() { _, _ = eng.stop(project, spec) })
			ctx := context.Background()
			if tc.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.ctxTimeout)
				defer cancel()
			}
			var err error
			captureStdout(t, func() { err = eng.start(ctx, project, spec) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSupervisedEngineStatusAndStopOfRecordedProcesses(t *testing.T) {
	eng := natsEngine
	project := t.TempDir()
	spec := Spec{Name: "svc", Engine: EngineNATS, Port: freeTCPPort(t)}
	pidFile := eng.pidFile(spec.dataDir(project))

	if stopped, err := eng.stop(project, spec); stopped || err != nil {
		t.Errorf("stop with no pidfile = %v, %v", stopped, err)
	}

	// A recorded pid that is gone: nothing to stop, and the stale file goes.
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePIDFile(pidFile, deadPID(t), spec.Port); err != nil {
		t.Fatal(err)
	}
	if stopped, err := eng.stop(project, spec); stopped || err != nil {
		t.Errorf("stop of a dead pid = %v, %v", stopped, err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("the stale pidfile survived")
	}

	// A recycled pid: alive, but not the binary this project started. It is
	// never signalled.
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatalf("sleep must be on every unix host this test builds for: %v", err)
	}
	waited := make(chan struct{})
	go func() { _ = other.Wait(); close(waited) }()
	t.Cleanup(func() { _ = other.Process.Kill(); <-waited })
	if err := writePIDFile(pidFile, other.Process.Pid, spec.Port); err != nil {
		t.Fatal(err)
	}
	if stopped, err := eng.stop(project, spec); stopped || err != nil {
		t.Errorf("stop of a recycled pid = %v, %v; want it left alone", stopped, err)
	}
	if !processAlive(other.Process.Pid) {
		t.Error("a process that is not ours was signalled")
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("the pidfile naming a foreign process should be cleared")
	}

	// status() of a pidfile whose process is alive reports it running even
	// when the readiness probe fails.
	if err := writePIDFile(pidFile, os.Getpid(), spec.Port); err != nil {
		t.Fatal(err)
	}
	st := eng.status(context.Background(), spec, spec.dataDir(project))
	if !st.Running || st.Ready || st.Port != spec.Port {
		t.Errorf("status = %+v, want running and not ready", st)
	}
	if err := os.Remove(pidFile); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSignalsAndLineageGuard(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("sleep must be on every unix host this test builds for: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	pid := child.Process.Pid
	if !processAlive(pid) {
		t.Fatal("a running child is not alive")
	}
	if err := terminateProcess(pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not stop the child")
	}
	if processAlive(pid) {
		t.Error("a reaped child still reads alive")
	}
	if processAlive(0) || processAlive(-5) {
		t.Error("pid <= 0 is never alive")
	}
	for name, err := range map[string]error{"terminate": terminateProcess(pid), "kill": killProcess(pid)} {
		if !isProcessGone(err) {
			t.Errorf("%s of an exited pid: err = %v, want a process-gone error", name, err)
		}
	}
	if !isProcessGone(os.ErrProcessDone) || !isProcessGone(syscall.ESRCH) || isProcessGone(errors.New("other")) {
		t.Error("isProcessGone should accept exactly ESRCH and ErrProcessDone")
	}

	// Never signal this command's own lineage.
	for name, fn := range map[string]func(int) error{"terminate": terminateProcess, "kill": killProcess} {
		if err := fn(os.Getpid()); err == nil || isProcessGone(err) {
			t.Errorf("%s of our own pid = %v, want the lineage refusal", name, err)
		}
	}
}

func TestIsExecutableFileReadsTheModeBits(t *testing.T) {
	dir := t.TempDir()
	for mode, want := range map[os.FileMode]bool{0o755: true, 0o700: true, 0o644: false} {
		p := filepath.Join(dir, fmt.Sprintf("f%o", mode))
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		if got := isExecutableFile(info); got != want {
			t.Errorf("mode %o: isExecutableFile = %v, want %v", mode, got, want)
		}
	}
}

func TestStatusOfAnUnsupportedEngineNamesIt(t *testing.T) {
	st, err := StatusOf(context.Background(), t.TempDir(), Spec{Name: "x", Engine: "cassandra"})
	if !errors.Is(err, ErrUnsupportedEngine) || !strings.Contains(err.Error(), "cassandra") || st.DataDir == "" {
		t.Errorf("StatusOf = %+v, %v", st, err)
	}
}

func TestStartMailpitRefusesAPortWithNoRoomForItsUI(t *testing.T) {
	err := Start(context.Background(), t.TempDir(), Spec{Name: "mail", Engine: EngineMailpit, Port: 65000})
	if err == nil || !strings.Contains(err.Error(), "web UI listens on port+") {
		t.Errorf("err = %v", err)
	}
}

// satisfy the compiler for symbols only some build tags use.
var _ = runtime.GOOS
