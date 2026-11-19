package pgtest

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseUsedComment(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if got, ok := parseUsedComment(usedComment(now)); !ok || !got.Equal(now) {
		t.Fatalf("round trip = (%v, %v), want (%v, true)", got, ok, now)
	}
	for _, c := range []string{"", "some other comment", templateUsedComment + "yesterday"} {
		if _, ok := parseUsedComment(c); ok {
			t.Errorf("parseUsedComment(%q) parsed", c)
		}
	}
}

func TestTemplateTTL(t *testing.T) {
	t.Setenv(EnvTemplateTTL, "")
	if got := templateTTL(); got != DefaultTemplateTTL {
		t.Errorf("unset: %v, want %v", got, DefaultTemplateTTL)
	}
	t.Setenv(EnvTemplateTTL, "72h")
	if got := templateTTL(); got != 72*time.Hour {
		t.Errorf("72h: %v", got)
	}
	t.Setenv(EnvTemplateTTL, "0")
	if got := templateTTL(); got != 0 {
		t.Errorf("0 (disabled): %v", got)
	}
	t.Setenv(EnvTemplateTTL, "soon")
	if got := templateTTL(); got != DefaultTemplateTTL {
		t.Errorf("unparsable: %v, want the default", got)
	}
}

// Templates no longer live as long as the server. On an external
// FORGE_TEST_POSTGRES_URL server — shared by every checkout on a machine and
// never torn down — every migration edit used to leave its template behind
// forever. Now a template unused for longer than the TTL is dropped by the
// next process's first NewFromTemplate, an unstamped one is adopted, a dead
// builder's *_build database is dropped, and a process whose cached template
// was reaped under it rebuilds instead of failing.
func TestNewFromTemplate_ReapsStaleTemplates(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	s, err := boot()
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	base := s.baseDB
	exists := func(name string) bool {
		t.Helper()
		var n int
		if err := base.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	comment := func(name string) string {
		t.Helper()
		var c sql.NullString
		if err := base.QueryRow(`SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, name).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c.String
	}
	exec := func(stmt string) {
		t.Helper()
		if _, err := base.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	suffix := fmt.Sprintf("reap_%d", time.Now().UnixNano())
	var builds atomic.Int32
	build := func(db *sql.DB, _ string) error {
		builds.Add(1)
		_, err := db.Exec(`CREATE TABLE seeded (v int)`)
		return err
	}

	// A template this process uses, then ages past the TTL.
	staleKey := "stale_" + suffix
	_, _, cleanup, err := NewFromTemplate(staleKey, build)
	if err != nil {
		t.Fatalf("NewFromTemplate(stale): %v", err)
	}
	cleanup()
	stale := templatePrefix + staleKey
	if _, ok := parseUsedComment(comment(stale)); !ok {
		t.Fatalf("published template %s carries no last-used stamp: %q", stale, comment(stale))
	}
	exec("COMMENT ON DATABASE " + stale + " IS '" + usedComment(time.Now().Add(-2*DefaultTemplateTTL)) + "'")

	// One published before templates carried a stamp, and a dead builder's leftover.
	legacy := templatePrefix + "legacy_" + suffix
	exec("CREATE DATABASE " + legacy)
	exec("ALTER DATABASE " + legacy + " WITH ALLOW_CONNECTIONS false")
	orphan := templatePrefix + "orphan_" + suffix + templateBuildSuffix
	exec("CREATE DATABASE " + orphan)
	t.Cleanup(func() {
		_, _ = base.Exec("DROP DATABASE IF EXISTS " + legacy)
		_, _ = base.Exec("DROP DATABASE IF EXISTS " + orphan)
		_, _ = base.Exec("DROP DATABASE IF EXISTS " + stale)
	})

	// The first NewFromTemplate of a "new process" sweeps the server.
	templateMu.Lock()
	delete(templatesReaped, s.baseURL)
	templateMu.Unlock()
	freshKey := "fresh_" + suffix
	_, _, cleanup, err = NewFromTemplate(freshKey, build)
	if err != nil {
		t.Fatalf("NewFromTemplate(fresh): %v", err)
	}
	cleanup()
	t.Cleanup(func() { _, _ = base.Exec("DROP DATABASE IF EXISTS " + templatePrefix + freshKey) })

	if exists(stale) {
		t.Errorf("template %s unused for 2×TTL survived the sweep", stale)
	}
	if exists(orphan) {
		t.Errorf("dead builder's %s survived the sweep", orphan)
	}
	if !exists(legacy) {
		t.Errorf("unstamped template %s was dropped; it must be adopted first", legacy)
	} else if _, ok := parseUsedComment(comment(legacy)); !ok {
		t.Errorf("unstamped template %s was not adopted: comment %q", legacy, comment(legacy))
	}
	if !exists(templatePrefix + freshKey) {
		t.Errorf("the sweep dropped the template it was called for")
	}

	// This process still has the reaped template's name cached: the next
	// clone rebuilds it rather than failing on a missing template.
	before := builds.Load()
	db, _, cleanup, err := NewFromTemplate(staleKey, build)
	if err != nil {
		t.Fatalf("NewFromTemplate after its template was reaped: %v", err)
	}
	defer cleanup()
	if builds.Load() != before+1 {
		t.Errorf("builds = %d, want one rebuild after the reap", builds.Load()-before)
	}
	if _, err := db.Exec(`INSERT INTO seeded VALUES (1)`); err != nil {
		t.Fatalf("rebuilt clone: %v", err)
	}
}
