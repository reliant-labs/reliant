package shadowdb

import (
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The applied schema, once per run.
//
// Every generate-time reader of the schema used to apply the project's
// migrations to a fresh shadow database of its own: the entity projection
// (behind the frontend pages, nav, service stubs, ORM, CRUD wiring and seed
// step, each calling it separately), the test factories, the frontend mock
// dataset. On control-plane — 168 migrations — that was about half of every
// full `forge generate`, re-deriving one unchanged answer.
//
// Inside a run (BeginRun), Tables and Open apply a given migration set ONCE,
// to a template database (schemadef.Template), and serve every later reader
// from it: Tables as a deep copy of the introspected model, Open as a private
// copy of the template database. Outside a run they behave exactly as they
// always did — one apply per call, nothing kept.
//
// What makes the reuse safe is the key, not the run boundary. A template is
// filed under the digest of the exact migration bytes it applied plus the
// server it lives on, so a reader whose migrations differ in any byte —
// a scaffold that just wrote one, an editor mid-run — misses and gets its
// own apply. The run boundary only bounds how long the template databases
// live: they are dropped when the outermost run ends.

// run is the process's run scope. depth counts nested BeginRun calls; the
// memo lives while it is above zero.
var run struct {
	mu      sync.Mutex
	depth   int
	servers map[string]string
	applied map[appliedKey]*appliedEntry
}

// appliedKey names one template: the shadow server it lives on and the
// digest of the migration bytes applied to it. The server is part of the key
// because the catalog's rendering of a schema is the server's (a
// pg_get_constraintdef spelling can change across major versions).
type appliedKey struct {
	server string
	digest string
}

// appliedEntry holds one template. mu serializes the apply, so concurrent
// readers of one migration set wait for the first apply instead of starting
// their own, and keeps endRun from dropping a template mid-read.
type appliedEntry struct {
	mu     sync.Mutex
	tpl    *schemadef.Template
	closed bool
}

// errOutsideRun reports that no run is open (or it ended mid-call): the
// caller falls back to a private apply.
var errOutsideRun = errors.New("shadowdb: no run open")

// BeginRun opens a run scope: until the returned end is called, Tables and
// Open apply each distinct migration set once and share the result. Scopes
// nest; the templates are dropped when the outermost one ends. end is
// idempotent.
//
// The server each project's shadow lives on is also resolved once per run,
// so every reader in one run sees the same postgres.
func BeginRun() (end func()) {
	run.mu.Lock()
	run.depth++
	if run.depth == 1 {
		run.servers = map[string]string{}
		run.applied = map[appliedKey]*appliedEntry{}
	}
	run.mu.Unlock()
	var once sync.Once
	return func() { once.Do(endRun) }
}

func endRun() {
	run.mu.Lock()
	run.depth--
	if run.depth > 0 {
		run.mu.Unlock()
		return
	}
	applied := run.applied
	run.applied, run.servers = nil, nil
	run.mu.Unlock()
	for _, e := range applied {
		e.mu.Lock()
		e.tpl.Close()
		e.tpl, e.closed = nil, true
		e.mu.Unlock()
	}
}

// Tables returns the applied schema of the migrations under migDir, on the
// shadow server Resolve(projectDir) picks: schemadef.ApplyAndIntrospectAt's
// answer. A missing or empty directory returns (nil, nil) without touching
// postgres. The caller owns the returned slice.
func Tables(projectDir, migDir string) ([]schemadef.Table, error) {
	tpl, done, err := runTemplate(projectDir, migDir)
	if errors.Is(err, errOutsideRun) {
		return schemadef.ApplyAndIntrospectAt(migDir, Resolve(projectDir))
	}
	if err != nil || tpl == nil {
		return nil, err
	}
	defer done()
	return tpl.Tables(), nil
}

// Views returns the applied schema's views (schemadef.IntrospectViews) on
// the same shadow Tables uses, from the same template within a run.
func Views(projectDir, migDir string) ([]schemadef.Table, error) {
	tpl, done, err := runTemplate(projectDir, migDir)
	if errors.Is(err, errOutsideRun) {
		_, shadow, err := schemadef.ApplyAndIntrospectShadowAt(migDir, Resolve(projectDir))
		defer shadow.Close()
		if err != nil || shadow == nil {
			return nil, err
		}
		return schemadef.IntrospectViews(shadow.DB())
	}
	if err != nil || tpl == nil {
		return nil, err
	}
	defer done()
	return tpl.Views(), nil
}

// Open is Tables plus a PRIVATE live database holding the applied schema,
// for a reader that must execute SQL against it (verify a fixture against a
// CHECK, insert a parent row in a rolled-back transaction). The caller owns
// the shadow and must Close it; writes to it reach nothing else.
//
// A missing or empty directory returns (nil, nil, nil). On error nothing is
// left open.
func Open(projectDir, migDir string) ([]schemadef.Table, *schemadef.Shadow, error) {
	tpl, done, err := runTemplate(projectDir, migDir)
	if errors.Is(err, errOutsideRun) {
		return applyPrivate(migDir, Resolve(projectDir))
	}
	if err != nil || tpl == nil {
		return nil, nil, err
	}
	defer done()
	shadow, err := tpl.Open()
	if err != nil {
		// A copy the server refused (a role that may create databases but
		// not copy this one, a stray session on the template) costs this
		// reader its shortcut, not its answer: replay into a fresh
		// database, as an Open outside a run does.
		return applyPrivate(migDir, serverOf(projectDir))
	}
	return tpl.Tables(), shadow, nil
}

// applyPrivate replays migDir into a database of the caller's own.
func applyPrivate(migDir, server string) ([]schemadef.Table, *schemadef.Shadow, error) {
	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(migDir, server)
	if err != nil {
		shadow.Close()
		return nil, nil, err
	}
	return tables, shadow, nil
}

// runTemplate returns this run's template for migDir's current content,
// applying it on first use, with its entry locked; done unlocks it. It
// returns errOutsideRun when no run is open, and a nil template for an empty
// migration set.
func runTemplate(projectDir, migDir string) (tpl *schemadef.Template, done func(), err error) {
	run.mu.Lock()
	inRun := run.depth > 0
	run.mu.Unlock()
	if !inRun {
		return nil, nil, errOutsideRun
	}
	// The set first: a project with no migrations has no schema, and must
	// not cost a server resolution (a network probe) to say so.
	set, err := schemadef.ReadMigrationSet(migDir)
	if err != nil || set.Len() == 0 {
		return nil, nil, err
	}
	server, ok := runServer(projectDir)
	if !ok {
		return nil, nil, errOutsideRun
	}

	key := appliedKey{server: server, digest: set.Digest()}
	run.mu.Lock()
	if run.depth == 0 {
		run.mu.Unlock()
		return nil, nil, errOutsideRun
	}
	e, ok := run.applied[key]
	if !ok {
		e = &appliedEntry{}
		run.applied[key] = e
	}
	run.mu.Unlock()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, nil, errOutsideRun
	}
	if e.tpl != nil {
		recordReuse()
		return e.tpl, e.mu.Unlock, nil
	}
	start := time.Now()
	built, err := set.ApplyTemplateAt(server)
	if err != nil {
		// Not memoized: the next reader tries again. A migration that does
		// not apply fails every reader the same way, and a transient fault
		// (a server that was briefly unreachable) gets its retry.
		e.mu.Unlock()
		return nil, nil, err
	}
	recordApply(time.Since(start))
	e.tpl = built
	return e.tpl, e.mu.Unlock, nil
}

// runServer resolves the shadow server for projectDir once per run, so one
// run never splits its readers across two postgres servers. ok is false
// outside a run.
func runServer(projectDir string) (server string, ok bool) {
	key := projectKey(projectDir)
	run.mu.Lock()
	if run.depth == 0 {
		run.mu.Unlock()
		return "", false
	}
	server, ok = run.servers[key]
	run.mu.Unlock()
	if ok {
		return server, true
	}
	// Resolved without the lock: it probes the network.
	resolved := Resolve(projectDir)
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.depth == 0 {
		return "", false
	}
	if server, ok = run.servers[key]; ok {
		return server, true
	}
	run.servers[key] = resolved
	return resolved, true
}

// serverOf is the server Tables and Open use for projectDir: the run's
// choice inside a run, a fresh resolution outside one.
func serverOf(projectDir string) string {
	if server, ok := runServer(projectDir); ok {
		return server
	}
	return Resolve(projectDir)
}

// projectKey normalizes projectDir so "." and its absolute spelling resolve
// once, not twice.
func projectKey(projectDir string) string {
	if abs, err := filepath.Abs(projectDir); err == nil {
		return abs
	}
	return projectDir
}

// Stats counts what the applied-schema memo did in this process: how many
// migration sets it applied, how long that took, and how many readers it
// served from an existing template instead.
type Stats struct {
	Applies   int
	Reuses    int
	ApplyTime time.Duration
}

// StatsSince is what the memo did after the reading earlier: the process's
// running totals now, minus earlier.
func StatsSince(earlier Stats) Stats {
	now := CurrentStats()
	return Stats{
		Applies:   now.Applies - earlier.Applies,
		Reuses:    now.Reuses - earlier.Reuses,
		ApplyTime: now.ApplyTime - earlier.ApplyTime,
	}
}

var (
	statsMu sync.Mutex
	stats   Stats
)

// CurrentStats returns the process's running totals.
func CurrentStats() Stats {
	statsMu.Lock()
	defer statsMu.Unlock()
	return stats
}

func recordApply(d time.Duration) {
	statsMu.Lock()
	stats.Applies++
	stats.ApplyTime += d
	statsMu.Unlock()
}

func recordReuse() {
	statsMu.Lock()
	stats.Reuses++
	statsMu.Unlock()
}
