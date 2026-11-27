package vulnscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGrype is a grype that keeps its "database" in memory: db status
// reports it, db update installs whatever the listing server publishes, db
// import installs the build time written in the imported file, and a scan
// prints the sample fixture with the current build stamped in.
type fakeGrype struct {
	t *testing.T

	mu      sync.Mutex
	built   time.Time // zero: no database
	listing *fakeListing
	scanOut []byte

	updates, imports, scans, statuses atomic.Int32
	lastEnv                           []string
	lastDir                           string
	failUpdate                        bool
}

func newFakeGrype(t *testing.T) *fakeGrype {
	t.Helper()
	sample, err := os.ReadFile("testdata/grype-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeGrype{t: t, scanOut: sample}
}

func (g *fakeGrype) setBuilt(b time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.built = b
}

func (g *fakeGrype) tool(context.Context) (string, string, error) {
	return "/fake/grype", "v0.122.1", nil
}

func (g *fakeGrype) run(_ context.Context, c Command) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastEnv, g.lastDir = c.Env, c.Dir
	if len(c.Args) < 3 || c.Args[0] != "-c" || c.Args[2] != "-q" {
		g.t.Errorf("grype run without forge's config and -q: %v", c.Args)
	}
	args := c.Args[3:]
	switch {
	case len(args) >= 2 && args[0] == "db" && args[1] == "status":
		g.statuses.Add(1)
		if g.built.IsZero() {
			_, _ = io.WriteString(c.Stdout, `{"schemaVersion":"","path":"/x/6/vulnerability.db","valid":false,"error":"database does not exist"}`)
			return errors.New("exit status 1")
		}
		_, _ = fmt.Fprintf(c.Stdout, `{"schemaVersion":"v6.1.10","from":"https://example/db.tar.zst","built":%q,"path":"/x/6/vulnerability.db","valid":true}`, g.built.Format(time.RFC3339))
		return nil
	case len(args) >= 2 && args[0] == "db" && args[1] == "update":
		g.updates.Add(1)
		if g.failUpdate {
			_, _ = io.WriteString(c.Stderr, "failed to download database")
			return errors.New("exit status 1")
		}
		if !hasEnv(c.Env, "GRYPE_DB_UPDATE_URL="+g.listing.base()) {
			g.t.Errorf("db update not pointed at the listing server: %v", c.Env)
		}
		g.built = g.listing.built()
		return nil
	case len(args) >= 3 && args[0] == "db" && args[1] == "import":
		g.imports.Add(1)
		b, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
		if err != nil {
			return fmt.Errorf("not an archive: %w", err)
		}
		g.built = ts
		return nil
	case len(args) >= 1 && (strings.HasPrefix(args[0], "sbom:") || strings.HasPrefix(args[0], "dir:")):
		g.scans.Add(1)
		var doc map[string]any
		if err := json.Unmarshal(g.scanOut, &doc); err != nil {
			return err
		}
		desc := doc["descriptor"].(map[string]any)
		desc["db"].(map[string]any)["status"].(map[string]any)["built"] = g.built.Format(time.RFC3339)
		return json.NewEncoder(c.Stdout).Encode(doc)
	}
	g.t.Errorf("unexpected grype invocation %v", args)
	return errors.New("unexpected invocation")
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// fakeListing serves <base>/v6/latest.json.
type fakeListing struct {
	srv   *httptest.Server
	mu    sync.Mutex
	b     time.Time
	fail  bool
	reads atomic.Int32
}

func newFakeListing(t *testing.T, built time.Time) *fakeListing {
	l := &fakeListing{b: built}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.reads.Add(1)
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.fail || r.URL.Path != "/v6/latest.json" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"active","schemaVersion":"v6.1.10","built":%q,"path":"vulnerability-db_v6.1.10.tar.zst","checksum":"sha256:abc"}`, l.b.Format(time.RFC3339))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *fakeListing) base() string { return l.srv.URL }

func (l *fakeListing) built() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b
}

func (l *fakeListing) set(b time.Time, fail bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b, l.fail = b, fail
}

// testNow is the fixed clock of every test: a day after the fixture's DB.
var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// newTestDB is a DB in a temp dir wired to a fake grype and listing.
func newTestDB(t *testing.T, built, listed time.Time) (*DB, *fakeGrype, *fakeListing, *strings.Builder) {
	t.Helper()
	g := newFakeGrype(t)
	g.setBuilt(built)
	l := newFakeListing(t, listed)
	g.listing = l
	var log strings.Builder
	db := &DB{
		Dir:   t.TempDir(),
		URL:   l.base(),
		Grype: g.tool,
		Run:   g.run,
		Now:   func() time.Time { return testNow },
		Log:   &syncWriter{w: &log},
	}
	return db, g, l, &log
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
