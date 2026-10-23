package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	templateMu    sync.Mutex
	templateNames = map[string]string{} // key -> published template database
	templateIdent = regexp.MustCompile(`[^a-z0-9_]+`)
	// templatesReaped records the servers (by base URL) this process has
	// already swept for stale templates; see reapStaleTemplates.
	templatesReaped = map[string]bool{}
)

// EnvTemplateTTL names the env var that overrides DefaultTemplateTTL: a Go
// duration ("72h"). Zero or negative disables reaping.
const EnvTemplateTTL = "FORGE_TEST_TEMPLATE_TTL"

// DefaultTemplateTTL is how long a template database may go unused before
// the next NewFromTemplate on the same server drops it.
//
// A template is keyed by what built it — usually a hash of the migration
// files — so every migration edit publishes a new one and the previous one
// is never asked for again. On the embedded server that is harmless: the
// server is torn down when its last user detaches, templates with it. On an
// external FORGE_TEST_POSTGRES_URL server, shared by every checkout and
// every agent on a machine and never torn down, each one used to persist
// forever. A day is long enough that a key in active use is never dropped
// (each process that uses a template restamps it) and short enough that a
// shared server does not accumulate a database per migration edit.
const DefaultTemplateTTL = 24 * time.Hour

// templateTTL resolves EnvTemplateTTL, falling back to DefaultTemplateTTL
// when it is unset or unparsable.
func templateTTL() time.Duration {
	if v := os.Getenv(EnvTemplateTTL); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return DefaultTemplateTTL
}

// templatePrefix is the name every published template carries; a template
// still being built carries templateBuildSuffix as well.
const (
	templatePrefix      = "forge_tmpl_"
	templateBuildSuffix = "_build"
)

// templateUsedComment is the COMMENT ON DATABASE a template carries: when a
// process last used it. Postgres records no creation or access time for a
// database that an unprivileged role can read, so the template carries its
// own.
const templateUsedComment = "forge-pgtest template; last_used="

func usedComment(now time.Time) string {
	return templateUsedComment + now.UTC().Format(time.RFC3339)
}

// parseUsedComment returns the last-used time a template's comment records.
func parseUsedComment(comment string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(comment, templateUsedComment)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, rest)
	return t, err == nil
}

// NewFromTemplate is New for a database that already has a schema: the schema is
// built ONCE per (server, key) by build and every caller gets its own clone
// (CREATE DATABASE ... TEMPLATE), which copies files instead of replaying the
// migrations. For a project with a few hundred migrations and a few hundred
// tests that is the difference between seconds and minutes of test time.
//
// key names what build produces: change it whenever the schema build changes (a
// hash of the migration files is the usual choice) and a stale template is never
// served. The template lives on the shared server, so every process of a
// fan-out — one `go test` binary per package — builds it once between them: a
// postgres advisory lock serializes the builders, the finished template is
// published under its final name only when complete (a reader never sees a
// half-built one), and a builder that dies mid-way leaves a *_build database the
// next builder drops.
//
// Templates are reaped by age. Each process that uses a template stamps it as
// used, and the first NewFromTemplate a process makes on a server drops every
// template there unused for longer than DefaultTemplateTTL (EnvTemplateTTL
// overrides it), plus any *_build database whose builder is gone. A key in use
// is therefore never dropped, and a superseded one — the migration hash before
// the last edit — is gone a day later instead of living as long as the server.
// A template reaped between two clones is rebuilt on demand.
//
// build receives an open handle on the empty database to fill, and that
// database's DSN for a builder that connects itself (a migrator that takes a
// URL). Connections it leaves open — through either — are terminated before
// the template is published, and the DSN names a database that stops existing
// under that name when build returns, so build must not need either afterwards.
//
// NewFromTemplate returns the clone twice, as an open handle and as its DSN (a
// postgres:// URL, the shape NewURL returns), because a test usually needs
// both: the handle for its own queries, the DSN for whatever connects by URL —
// a second pool, a migrator, a server it boots as a subprocess. cleanup
// closes the handle, terminates every other session on the clone, and drops
// it.
func NewFromTemplate(key string, build func(db *sql.DB, dsn string) error) (db *sql.DB, dsn string, cleanup func(), err error) {
	s, err := boot()
	if err != nil {
		return nil, "", nil, err
	}
	name := scratchName(scratchTestPrefix)
	if err := cloneTemplate(s, key, build, name); err != nil {
		return nil, "", nil, err
	}
	baseDB := s.baseDB
	drop := func() {
		_, _ = baseDB.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		_, _ = baseDB.Exec("DROP DATABASE IF EXISTS " + name)
	}
	dsn = replaceDBName(s.baseURL, name)
	db, err = open(dsn)
	if err != nil {
		drop()
		return nil, "", nil, err
	}
	if err := pingWithRetry(db); err != nil {
		_ = db.Close()
		drop()
		return nil, "", nil, fmt.Errorf("pgtest: ping %s: %w", name, err)
	}
	return db, dsn, func() { _ = db.Close(); drop() }, nil
}

// cloneTemplate creates database name as a copy of key's template, building
// the template first when needed. A template another process reaped after
// this one cached its name is rebuilt once and the clone retried.
func cloneTemplate(s *server, key string, build func(*sql.DB, string) error, name string) error {
	for attempt := 0; ; attempt++ {
		tmpl, err := ensureTemplate(s, key, build)
		if err != nil {
			return err
		}
		_, err = s.baseDB.Exec("CREATE DATABASE " + name + " TEMPLATE " + tmpl)
		if err == nil {
			return nil
		}
		var sqlState interface{ SQLState() string }
		if attempt == 0 && errors.As(err, &sqlState) && sqlState.SQLState() == "3D000" {
			forgetTemplate(s, key)
			continue
		}
		return fmt.Errorf("pgtest: clone template %s into %s: %w", tmpl, name, err)
	}
}

// forgetTemplate drops key's cached template name, so the next ensureTemplate
// looks on the server again.
func forgetTemplate(s *server, key string) {
	templateMu.Lock()
	defer templateMu.Unlock()
	delete(templateNames, s.baseURL+"\x00"+key)
}

func ensureTemplate(s *server, key string, build func(*sql.DB, string) error) (string, error) {
	templateMu.Lock()
	defer templateMu.Unlock()
	// The cache is per (server, key): a Shutdown followed by a boot may be a
	// different server, and names are not unique across servers.
	cacheKey := s.baseURL + "\x00" + key
	if name, ok := templateNames[cacheKey]; ok {
		return name, nil
	}
	ctx := context.Background()
	final := templatePrefix + templateIdent.ReplaceAllString(strings.ToLower(key), "_")
	building := final + templateBuildSuffix

	// Advisory locks are session-scoped: hold one dedicated connection, and the
	// lock releases itself if this process dies mid-build.
	conn, err := s.baseDB.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", final); err != nil {
		return "", err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", final) }()

	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", final).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		// Holding the lock, nobody else is building: a *_build database is the
		// remains of a builder that died.
		if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)"); err != nil {
			return "", err
		}
		if _, err := conn.ExecContext(ctx, "CREATE DATABASE "+building); err != nil {
			return "", fmt.Errorf("pgtest: create template database: %w", err)
		}
		fail := func(err error) (string, error) {
			_, _ = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)")
			return "", err
		}
		buildDSN := replaceDBName(s.baseURL, building)
		bdb, err := open(buildDSN)
		if err != nil {
			return fail(err)
		}
		if err := pingWithRetry(bdb); err != nil {
			_ = bdb.Close()
			return fail(err)
		}
		berr := build(bdb, buildDSN)
		_ = bdb.Close()
		if berr != nil {
			return fail(berr)
		}
		// CREATE DATABASE ... TEMPLATE refuses a source anyone is connected to.
		if _, err := conn.ExecContext(ctx,
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", building); err != nil {
			return fail(err)
		}
		if _, err := conn.ExecContext(ctx, "ALTER DATABASE "+building+" WITH ALLOW_CONNECTIONS false"); err != nil {
			return fail(err)
		}
		// The rename is the publish.
		if _, err := conn.ExecContext(ctx, "ALTER DATABASE "+building+" RENAME TO "+final); err != nil {
			return fail(fmt.Errorf("pgtest: publish template database: %w", err))
		}
	}
	// Stamp it used — by this process, now — so the reaper keeps it. Best
	// effort: a role that does not own the template cannot comment on it, and
	// that must not fail a test.
	_, _ = conn.ExecContext(ctx, "COMMENT ON DATABASE "+final+" IS '"+usedComment(time.Now())+"'")
	if !templatesReaped[s.baseURL] {
		templatesReaped[s.baseURL] = true
		reapStaleTemplates(ctx, conn, final, templateTTL(), time.Now())
	}
	templateNames[cacheKey] = final
	return final, nil
}

// reapStaleTemplates drops, on conn's server, every published template whose
// last-used stamp is older than ttl, and every *_build database whose builder
// is gone. keep — the template the caller holds the lock for and is about to
// clone — is never touched. A template with no stamp (published before
// templates carried one) is adopted: stamped now, and reaped a ttl later if
// nobody uses it. Best effort throughout: a failure leaves the database for
// the next sweep and never fails the caller.
//
// Each candidate is dropped only while holding its key's advisory lock, taken
// with pg_try_advisory_lock so a sweep never waits: a held lock means a
// process is building, checking or stamping that template right now, which is
// exactly when it must not disappear.
func reapStaleTemplates(ctx context.Context, conn *sql.Conn, keep string, ttl time.Duration, now time.Time) {
	if ttl <= 0 {
		return
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT datname, COALESCE(shobj_description(oid, 'pg_database'), '')
		FROM pg_database
		WHERE starts_with(datname, $1) AND datname <> $2`, templatePrefix, keep)
	if err != nil {
		return
	}
	type candidate struct{ name, comment string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.name, &c.comment) == nil {
			candidates = append(candidates, c)
		}
	}
	_ = rows.Close()

	for _, c := range candidates {
		lockName, isBuild := strings.CutSuffix(c.name, templateBuildSuffix)
		if !isBuild {
			used, stamped := parseUsedComment(c.comment)
			if !stamped {
				_, _ = conn.ExecContext(ctx, "COMMENT ON DATABASE "+quoteIdent(c.name)+" IS '"+usedComment(now)+"'")
				continue
			}
			if now.Sub(used) < ttl {
				continue
			}
		}
		var locked bool
		if conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", lockName).Scan(&locked) != nil || !locked {
			continue
		}
		// WITH (FORCE): a *_build database may still hold the dead builder's
		// session; a published template allows no connections at all.
		_, _ = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(c.name)+" WITH (FORCE)")
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", lockName)
	}
}
