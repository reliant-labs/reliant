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
	"reflect"
	"strings"
	"testing"
)

// fakeCoverage stands in for runtime/coverage: it writes the files a -cover
// build would, so every answer of the handler is pinned without one.
type fakeCoverage struct {
	instrumented bool
	countersErr  error
	// reads counts the calls that read the process's coverage.
	reads int
}

func (f *fakeCoverage) Instrumented() bool { return f.instrumented }
func (f *fakeCoverage) WriteMetaDir(dir string) error {
	f.reads++
	return os.WriteFile(filepath.Join(dir, "covmeta.abc123"), []byte("meta"), 0o644)
}
func (f *fakeCoverage) WriteCountersDir(dir string) error {
	if f.countersErr != nil {
		return f.countersErr
	}
	return os.WriteFile(filepath.Join(dir, "covcounters.abc123.42.1700000000"), []byte("counters"), 0o644)
}

// serveCoverage sends one request as a local tool does: to a loopback Host,
// with no browser headers. header adds name, value pairs.
func serveCoverage(t *testing.T, rt coverageRuntime, method, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:6060"
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	coverageHandler{rt: rt}.ServeHTTP(rec, req)
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

}

// [B1] The endpoint is read-only, structurally: the slice of
// runtime/coverage it can call reads and never writes, so no request can
// change the process's counters, whoever sends it. A reset is the client's
// baseline (forge test collect --reset).
func TestCoverageRuntimeCannotChangeCounters(t *testing.T) {
	typ := reflect.TypeFor[coverageRuntime]()
	for i := range typ.NumMethod() {
		switch m := typ.Method(i).Name; m {
		case "Instrumented", "WriteMetaDir", "WriteCountersDir":
		default:
			t.Errorf("coverageRuntime has %s: the endpoint must not be able to change the counters (no auth can make that safe on a debug listener a web page can reach)", m)
		}
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
}

// [B1] Every request that is not a plain read from a local tool is refused
// before the counters are read: a POST or a ?reset (the forms a reset once
// took) is 405 and resets nothing, and anything browser-originated is 403.
func TestCoverageHandler_RequestPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		target string
		host   string
		header []string
		code   int
		says   string
	}{
		{name: "POST is 405 and names the client-side reset", method: http.MethodPost, target: CoveragePath, code: http.StatusMethodNotAllowed, says: "forge test collect"},
		{name: "POST with the old reset header is 405", method: http.MethodPost, target: CoveragePath, header: []string{"X-Forge-Coverage-Reset", "1"}, code: http.StatusMethodNotAllowed},
		{name: "GET ?reset=1 is 405", method: http.MethodGet, target: CoveragePath + "?reset=1", code: http.StatusMethodNotAllowed, says: "read-only"},
		{name: "GET ?reset with any value is 405", method: http.MethodGet, target: CoveragePath + "?reset=true", code: http.StatusMethodNotAllowed},
		{name: "a read with Origin: 403", method: http.MethodGet, target: CoveragePath, header: []string{"Origin", "http://localhost:3000"}, code: http.StatusForbidden, says: "Origin"},
		{name: "a cross-site read: 403", method: http.MethodGet, target: CoveragePath, header: []string{"Sec-Fetch-Site", "cross-site"}, code: http.StatusForbidden, says: "Sec-Fetch-Site"},
		{name: "a read from another localhost port (same-site): 403", method: http.MethodGet, target: CoveragePath, header: []string{"Sec-Fetch-Site", "same-site"}, code: http.StatusForbidden},
		{name: "a read from a rebound Host: 403", method: http.MethodGet, target: CoveragePath, host: "rebind.evil.example", code: http.StatusForbidden, says: "Host"},
		{name: "a typed-in navigation (Sec-Fetch-Site none) reads", method: http.MethodGet, target: CoveragePath, header: []string{"Sec-Fetch-Site", "none"}, code: http.StatusOK},
		{name: "a read from localhost reads", method: http.MethodGet, target: CoveragePath, host: "localhost:6060", code: http.StatusOK},
		{name: "a read from [::1] reads", method: http.MethodGet, target: CoveragePath, host: "[::1]:6060", code: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeCoverage{instrumented: true}
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Host = "127.0.0.1:6060"
			if tc.host != "" {
				req.Host = tc.host
			}
			for i := 0; i+1 < len(tc.header); i += 2 {
				req.Header.Set(tc.header[i], tc.header[i+1])
			}
			rec := httptest.NewRecorder()
			coverageHandler{rt: rt}.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.code, rec.Body)
			}
			if tc.says != "" && !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("body %q does not name %q", rec.Body, tc.says)
			}
			if tc.code != http.StatusOK && (rt.reads != 0 || strings.Contains(rec.Header().Get("Content-Type"), "tar")) {
				t.Error("a refused request read the counters")
			}
		})
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
