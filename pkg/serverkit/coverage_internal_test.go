package serverkit

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCoverage stands in for runtime/coverage: it writes the files a -cover
// build would, so every answer of the handler is pinned without one.
type fakeCoverage struct {
	instrumented bool
	countersErr  error
	clearErr     error
	cleared      int
}

func (f *fakeCoverage) Instrumented() bool { return f.instrumented }
func (f *fakeCoverage) WriteMetaDir(dir string) error {
	return os.WriteFile(filepath.Join(dir, "covmeta.abc123"), []byte("meta"), 0o644)
}
func (f *fakeCoverage) WriteCountersDir(dir string) error {
	if f.countersErr != nil {
		return f.countersErr
	}
	return os.WriteFile(filepath.Join(dir, "covcounters.abc123.42.1700000000"), []byte("counters"), 0o644)
}
func (f *fakeCoverage) ClearCounters() error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared++
	return nil
}

func serveCoverage(t *testing.T, rt coverageRuntime, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	coverageHandler{rt: rt}.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func untar(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("response is not a tar: %v", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(b)
	}
}

func TestCoverageHandler_ServesMetaAndCountersAsTar(t *testing.T) {
	rt := &fakeCoverage{instrumented: true}
	rec := serveCoverage(t, rt, http.MethodGet, CoveragePath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("Content-Type %q, want application/x-tar", ct)
	}
	files := untar(t, rec.Body.Bytes())
	if files["covmeta.abc123"] != "meta" || files["covcounters.abc123.42.1700000000"] != "counters" || len(files) != 2 {
		t.Errorf("tar holds %v, want exactly the meta and counter files GOCOVERDIR would", files)
	}
	if rt.cleared != 0 {
		t.Error("a plain pull cleared the counters")
	}
}

func TestCoverageHandler_ResetClearsAfterSnapshot(t *testing.T) {
	rt := &fakeCoverage{instrumented: true}
	rec := serveCoverage(t, rt, http.MethodGet, CoveragePath+"?reset=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if rt.cleared != 1 {
		t.Errorf("ClearCounters called %d times, want 1", rt.cleared)
	}
	if files := untar(t, rec.Body.Bytes()); files["covcounters.abc123.42.1700000000"] == "" {
		t.Errorf("reset returned no counters: the pull before the reset must not be lost; got %v", files)
	}
}

func TestCoverageHandler_NotACoverBuildIs404(t *testing.T) {
	rec := serveCoverage(t, &fakeCoverage{}, http.MethodGet, CoveragePath)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 for a binary built without -cover", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "-cover") {
		t.Errorf("404 body %q does not say why", rec.Body)
	}
}

// A build in set or count mode has meta-data but no live counters Go will
// write or clear: the caller must rebuild with -covermode=atomic.
func TestCoverageHandler_NonAtomicModeIsAConflict(t *testing.T) {
	modeErr := errors.New("WriteCountersDir invoked for program built with -covermode=set (please use -covermode=atomic)")
	rec := serveCoverage(t, &fakeCoverage{instrumented: true, countersErr: modeErr}, http.MethodGet, CoveragePath)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "atomic") {
		t.Fatalf("status %d body %q, want 409 naming -covermode=atomic", rec.Code, rec.Body)
	}

	clearErr := errors.New("ClearCounters invoked for program built with -covermode=count (please use -covermode=atomic)")
	rec = serveCoverage(t, &fakeCoverage{instrumented: true, clearErr: clearErr}, http.MethodGet, CoveragePath+"?reset=1")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reset") {
		t.Fatalf("status %d body %q, want 409 for a reset Go refused", rec.Code, rec.Body)
	}
}

func TestCoverageHandler_RefusesOtherMethods(t *testing.T) {
	rec := serveCoverage(t, &fakeCoverage{instrumented: true}, http.MethodDelete, CoveragePath)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// The endpoint rides the debug listener's mux, never the public one, and
// only when that listener is bound to loopback: it reads and resets the
// process's counters with no auth. A reachable bind withholds it, and says so
// once.
func TestPprofMuxMountsCoverageOnlyOnLoopback(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		mounted bool
	}{
		{"127.0.0.1:6060", true},
		{"127.0.0.5:0", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"0.0.0.0:6060", false},
		{":6060", false},
		{"[::]:6060", false},
		{"10.0.0.7:6060", false},
		{"pprof.internal:6060", false},
	} {
		var logs bytes.Buffer
		p := NewPprof(Config{PprofAddr: tc.addr}, slog.New(slog.NewTextHandler(&logs, nil)))
		_, pattern := p.srv.Handler.(*http.ServeMux).Handler(httptest.NewRequest(http.MethodGet, CoveragePath, nil))
		if got := pattern == CoveragePath; got != tc.mounted {
			t.Errorf("%s: coverage mounted = %v, want %v", tc.addr, got, tc.mounted)
		}
		withheld := strings.Count(logs.String(), "coverage endpoint withheld")
		if want := map[bool]int{true: 0, false: 1}[tc.mounted]; withheld != want {
			t.Errorf("%s: logged the withheld endpoint %d times, want %d:\n%s", tc.addr, withheld, want, logs.String())
		}
	}
	// pprof itself is unaffected: a reachable bind is the author's choice for it.
	p := NewPprof(Config{PprofAddr: "0.0.0.0:6060"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, pattern := p.srv.Handler.(*http.ServeMux).Handler(httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil)); pattern != "/debug/pprof/" {
		t.Errorf("pprof routes /debug/pprof/heap to %q", pattern)
	}
}
