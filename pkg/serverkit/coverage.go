package serverkit

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/coverage"
	"sort"
	"strings"
)

// CoveragePath is where the debug listener (Config.PprofAddr) serves the
// process's Go coverage data. `forge test collect` reaches it the way it
// reaches pprof: localhost on a host, `kubectl port-forward` in a cluster.
const CoveragePath = "/debug/coverage"

// CoverageHandler serves a binary's coverage data, for a server built with
// `go build -cover` (forge builds one for local, ephemeral and preview envs).
// A long-lived process never returns from main, so the counters Go writes to
// GOCOVERDIR at exit are only half the story: pulling them while it runs
// loses nothing to a later SIGKILL.
//
//	GET /debug/coverage   a tar of covmeta.<hash> + covcounters.<hash>.<pid>.<ts>
//
// The tar holds exactly what Go would write to GOCOVERDIR, so
// `go tool covdata textfmt -i=<dir>` reads it once unpacked. The meta file's
// hash is the build's fingerprint: data from two builds never merges.
//
// It is read-only: nothing a request sends changes the process's counters.
// "Start the counts from zero" is the client's job: `forge test collect
// --reset` pulls the counters as a baseline and subtracts it from every
// later pull. A POST (the form a reset once took) answers 405 and clears
// nothing, and so does a GET with ?reset.
//
// It answers 404 when the binary was not built with -cover (a `go test -c
// -cover` binary counts as one: Go prepares a test binary's meta-data only
// at exit), and 409 when it was built in a mode other than atomic: Go writes
// live counters only in atomic mode (`-covermode=atomic`, which every forge
// cover build uses). It is mounted on the debug listener only, never the
// public edge, and only when that listener is bound to loopback.
//
// Loopback keeps other machines out, not the browser on this one: a page can
// make requests to 127.0.0.1, and the counters it would read describe the
// process's code paths. So a request with an Origin header, a Sec-Fetch-Site
// other than none or same-origin, or a Host that is not a loopback name (DNS
// rebinding) is refused with 403.
func CoverageHandler() http.Handler {
	return coverageHandler{rt: goCoverage{}}
}

// coverageRuntime is the slice of runtime/coverage the handler drives, so a
// test can exercise every answer without a -cover build.
type coverageRuntime interface {
	// Instrumented reports whether the binary was built with -cover.
	Instrumented() bool
	WriteMetaDir(dir string) error
	WriteCountersDir(dir string) error
}

type goCoverage struct{}

// Instrumented asks for the meta-data, which only a -cover build has. It
// is the one runtime/coverage call that succeeds in every cover mode.
func (goCoverage) Instrumented() bool                { return coverage.WriteMeta(io.Discard) == nil }
func (goCoverage) WriteMetaDir(dir string) error     { return coverage.WriteMetaDir(dir) }
func (goCoverage) WriteCountersDir(dir string) error { return coverage.WriteCountersDir(dir) }

type coverageHandler struct{ rt coverageRuntime }

func (h coverageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Query().Has("reset") {
		w.Header().Set("Allow", "GET")
		http.Error(w, "coverage: this endpoint is read-only, and nothing was reset; "+
			"`forge test collect <env> --reset` starts a run from zero by recording these counters as a baseline it subtracts later", http.StatusMethodNotAllowed)
		return
	}
	if why := browserOriginated(r); why != "" {
		http.Error(w, "coverage: refused: "+why+"; this endpoint serves local tools, never a web page", http.StatusForbidden)
		return
	}
	if !h.rt.Instrumented() {
		http.Error(w, "this binary was not built with -cover, so it has no coverage data", http.StatusNotFound)
		return
	}
	dir, err := os.MkdirTemp("", "forge-coverage-")
	if err != nil {
		http.Error(w, "coverage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := h.rt.WriteMetaDir(dir); err != nil {
		http.Error(w, "coverage: write meta-data: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Live counters exist only in atomic mode; any other mode is a build the
	// caller has to change, not a server fault.
	if err := h.rt.WriteCountersDir(dir); err != nil {
		http.Error(w, "coverage: write counters: "+err.Error(), http.StatusConflict)
		return
	}
	var buf bytes.Buffer
	if err := tarDir(&buf, dir); err != nil {
		http.Error(w, "coverage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// browserOriginated says why r looks like a browser's request, or "" when
// it does not. A browser sends Origin on every cross-origin request and every
// POST, and Sec-Fetch-Site on every request it makes; a DNS-rebound page is
// same-origin to itself, so its Host names the attacker's domain, not a
// loopback name. A local tool (curl, forge test collect, a port-forward)
// sends none of these.
func browserOriginated(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		return fmt.Sprintf("the request carries Origin %q", o)
	}
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "", "none", "same-origin":
	default:
		return fmt.Sprintf("the request carries Sec-Fetch-Site %q", site)
	}
	if !loopbackHostHeader(r.Host) {
		return fmt.Sprintf("Host %q is not a loopback name", r.Host)
	}
	return ""
}

// loopbackHostHeader reports whether a Host header names this machine:
// localhost, a loopback IP, or nothing at all (HTTP/1.0).
func loopbackHostHeader(host string) bool {
	if host == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// tarDir writes every regular file directly in dir, by name, to w.
func tarDir(w io.Writer, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	tw := tar.NewWriter(w)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: e.Name(), Mode: 0o644, Size: int64(len(body)), ModTime: info.ModTime(), Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	return tw.Close()
}
