package codegen

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyModuleProxy is a GOPROXY serving one module, example.com/dep v1.0.0,
// that drops the connection of the first `fail` requests — what a "TLS
// handshake timeout" to proxy.golang.org looks like to cmd/go: a transport
// failure it does not retry. fail < 0 drops every request.
func flakyModuleProxy(t *testing.T, fail int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	const mod = "module example.com/dep\n\ngo 1.21\n"
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, body := range map[string]string{
		"go.mod": mod,
		"dep.go": "package dep\n\n// Thing is what internal/app reads off the module.\ntype Thing struct{ N int }\n",
	} {
		w, err := zw.Create("example.com/dep@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := served.Add(1)
		if fail < 0 || n <= int64(fail) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		switch r.URL.Path {
		case "/example.com/dep/@v/list":
			_, _ = w.Write([]byte("v1.0.0\n"))
		case "/example.com/dep/@v/v1.0.0.info":
			_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`))
		case "/example.com/dep/@v/v1.0.0.mod":
			_, _ = w.Write([]byte(mod))
		case "/example.com/dep/@v/v1.0.0.zip":
			_, _ = w.Write(zbuf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

// coldModuleProject is a project whose internal/app imports example.com/dep,
// laid out as `forge project new` leaves it before its first generate: a
// go.work, a go.mod requiring the module, no go.sum, and a module cache that
// has never seen it.
func coldModuleProject(t *testing.T, proxy string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.work":             "go 1.21\n\nuse .\n",
		"go.mod":              "module example.com/p\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n",
		"internal/app/app.go": "package app\n\nimport \"example.com/dep\"\n\n// Graph holds the module's type.\ntype Graph struct{ T dep.Thing }\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", proxy)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOTOOLCHAIN", "local")
	return dir
}

func shrinkGraphLoadBackoff(t *testing.T) {
	t.Helper()
	prev := graphLoadBackoff
	graphLoadBackoff = 10 * time.Millisecond
	t.Cleanup(func() { graphLoadBackoff = prev })
}

// TestGraphUniverseRetriesAModuleFetchThatFailedInTransport pins bug
// d2a5b5ba: on a cold module cache the composition graph's type load is the
// first thing to download the project's modules, and a single dropped fetch
// left internal/app untyped — the first `forge project new` then wrote a
// server root that reached no database and config_gen.k refused the
// scaffold's own DATABASE_URL. The load retries a transport failure, so a
// flaky network converges on the first run.
func TestGraphUniverseRetriesAModuleFetchThatFailedInTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("execs go list against a local module proxy; runs in task test")
	}
	shrinkGraphLoadBackoff(t)
	srv, served := flakyModuleProxy(t, 1)
	dir := coldModuleProject(t, srv.URL)

	u := loadGraphUniverse(dir, nil)
	if u.fetchErr != nil {
		t.Fatalf("fetchErr = %v; a fetch that failed once must be retried, not reported", u.fetchErr)
	}
	if u.app == nil || u.app.Types == nil {
		t.Fatal("internal/app did not load")
	}
	if errs := u.app.Errors; len(errs) > 0 {
		t.Fatalf("internal/app loaded with errors after the retry: %v", errs)
	}
	if obj := u.app.Types.Scope().Lookup("Graph"); obj == nil || !strings.Contains(obj.Type().Underlying().String(), "example.com/dep.Thing") {
		t.Fatalf("app.Graph is not typed through the downloaded module: %v", obj)
	}
	if served.Load() < 2 {
		t.Fatalf("the proxy saw %d request(s); the test needs the first to fail and a later one to succeed", served.Load())
	}
}

// TestGraphUniverseReportsAModuleFetchThatKeepsFailing: when every attempt
// fails in transport the universe says so, and assembleGraph refuses to
// project a graph from it rather than writing a wrong one.
func TestGraphUniverseReportsAModuleFetchThatKeepsFailing(t *testing.T) {
	if testing.Short() {
		t.Skip("execs go list against a local module proxy; runs in task test")
	}
	shrinkGraphLoadBackoff(t)
	srv, _ := flakyModuleProxy(t, -1)
	dir := coldModuleProject(t, srv.URL)

	u := loadGraphUniverse(dir, nil)
	if u.fetchErr == nil {
		t.Fatal("fetchErr = nil; a module that never downloads must be reported")
	}
	if !strings.Contains(u.fetchErr.Error(), "example.com/dep") {
		t.Fatalf("fetchErr does not name the module: %v", u.fetchErr)
	}
}

func TestTransportFailureMatchesOnlyNetworkErrors(t *testing.T) {
	for msg, want := range map[string]bool{
		`internal/app/providers.go:30:2: connectrpc.com/connect@v1.19.0: Get "https://proxy.golang.org/connectrpc.com/connect/@v/v1.19.0.mod": net/http: TLS handshake timeout`: true,
		`x.go:1:2: example.com/dep@v1.0.0: Get "http://127.0.0.1:1/example.com/dep/@v/v1.0.0.mod": EOF`:                                                                         true,
		`example.com/dep@v1.0.0: reading https://proxy.golang.org/example.com/dep/@v/v1.0.0.mod: 502 Bad Gateway`:                                                               true,
		`example.com/dep@v1.0.0: reading https://proxy.golang.org/example.com/dep/@v/v1.0.0.mod: 404 Not Found`:                                                                 false,
		`no required module provides package example.com/dep; to add it: go get example.com/dep`:                                                                                false,
		`missing go.sum entry for module providing package example.com/dep`:                                                                                                     false,
	} {
		if got := transportFailure.MatchString(msg); got != want {
			t.Errorf("transportFailure(%q) = %v, want %v", msg, got, want)
		}
	}
}
