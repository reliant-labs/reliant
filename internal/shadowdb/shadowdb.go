// Package shadowdb resolves WHICH postgres server hosts the generate-time
// schema-introspection shadow database, and — inside a generate run — makes
// every reader of the applied schema share ONE application of the
// migrations to it (applied.go).
//
// # Why this layer exists
//
// forge derives ORM/CRUD code by applying a project's migrations to a
// throwaway "shadow" postgres and introspecting the live schema
// (pkg/schemadef). That needs a server. Historically the ONLY way to
// use a real running server instead of a fragile embedded one was the
// FORGE_TEST_POSTGRES_URL env var — a TEST-suite variable that leaked into
// being the production codegen config channel. Because it lived OUTSIDE
// forge's config system it silently diverged between steps, and because it
// was the only non-embedded path, embedded became the accidental default —
// which on a shared/resource-starved machine leaks SysV semaphores and
// fails initdb.
//
// This package moves the "which server" DECISION into forge's own
// environment/config layer. schemadef and pkg/pgtest stay dumb mechanics:
// pgtest knows how to make an ephemeral database (embedded, or a scratch DB
// on a given server URL); schemadef applies migrations and introspects
// against whatever database it is handed. The POLICY of which server to
// use, and whether one is reachable at all, lives here — as does how often
// one run applies a migration set to it (once: see applied.go).
package shadowdb

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// defaultEnv is the environment whose config supplies the shadow server.
// `forge generate` is not env-scoped; the dev stack is what a developer has
// running at generate time, so its postgres is the natural shadow host.
const defaultEnv = "dev"

// Resolve returns the base DSN of a REACHABLE postgres SERVER to host the
// generate-time shadow database, or "" when none is available — in which
// case the caller falls back to embedded postgres (true zero-setup / CI).
//
// projectDir is the directory containing forge.yaml.
//
// Resolution order — first usable candidate wins:
//
//  1. FORGE_TEST_POSTGRES_URL — the explicit override / escape hatch. It is
//     honored as-is WITHOUT a reachability probe: an override that is set
//     but wrong must fail loudly downstream (a hard CREATE DATABASE error),
//     not silently fall back to embedded and hide the misconfiguration.
//     forge's own e2e suite sets this to share one server across the
//     parallel corpus, so this path must keep working unchanged.
//
//  2. an exported DATABASE_URL — a dev shell that already points at the
//     running stack.
//
//  3. the project's per-env SECRET dotenv (`.env.<env>` DATABASE_URL) — the
//     dev DB the app itself dials. A scaffolded project marks database_url
//     `sensitive`, so its VALUE lives here (gitignored) and the git-tracked
//     config.k carries only the Secret reference.
//
//  4. the project's per-env config (deploy/kcl/<env>/config.k `database_url`)
//     — the same DB, for a project that did NOT mark the field sensitive.
//
//  5. the forge dev-stack convention — localhost:${POSTGRES_PORT:-5432}
//     with ${POSTGRES_USER:-postgres}/${POSTGRES_PASSWORD:-postgres}, the
//     SAME host coordinates the scaffold's docker-compose maps postgres to.
//
// Candidates 2–5 are reduced to their SERVER coordinates against the
// maintenance database "postgres" and PROBED (pgtest.CanReach) before being
// returned, so the app's own database is never opened or mutated, and a
// candidate that is down or has wrong credentials is skipped rather than
// turned into a hard error. When none is reachable, "" is returned and the
// caller uses embedded postgres.
//
// Note on the scaffold phase: at `forge new` / one-shot scaffold time the
// project's own dev DB is typically NOT up yet, so candidates 3–4 will not
// probe. Candidate 5 still lets a developer whose machine already runs a
// shared dev postgres at the conventional coordinates use it; otherwise the
// resolver returns "" and embedded is used — no configuration required.
func Resolve(projectDir string) string {
	// 1. Explicit override — honored verbatim, unprobed, reduced only so the
	//    scratch DB is created off the maintenance DB.
	if v := getenv(pgtest.EnvBaseURL); v != "" {
		return toMaintenanceServer(v)
	}

	// 2–5. Config-derived candidates, each probed for reachability.
	for _, raw := range candidateDSNs(projectDir) {
		if raw == "" {
			continue
		}
		server := toMaintenanceServer(raw)
		if server == "" {
			continue
		}
		if pgtest.CanReach(server) {
			return server
		}
	}
	return ""
}

// candidateDSNs returns the raw DSN candidates in priority order (2→5).
func candidateDSNs(projectDir string) []string {
	return []string{
		getenv("DATABASE_URL"),
		dotenvDatabaseURL(projectDir, defaultEnv),
		configDatabaseURL(projectDir, defaultEnv),
		devStackConventionDSN(),
	}
}

// dotenvDatabaseURL reads DATABASE_URL from the project's per-env secret
// store — the provider deploy/kcl/<env>/main.k declares, and the source of
// every `sensitive` config field's VALUE. Any error (no store, no key)
// yields "".
//
// Reads the FileSecrets store (secrets/<env>.yaml) with a deliberately
// small line scan rather than a YAML dependency: shadowdb is imported by
// the generate pipeline and stays leaf-only, exactly like
// configDatabaseURL's best-effort text read. Only the flat `KEY: value`
// shape is understood, which is all this store is.
func dotenvDatabaseURL(projectDir, env string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "secrets", env+".yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "DATABASE_URL" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

// kclConfigDBURLRE extracts the `database_url = "value"` assignment from a
// KCL-native per-env config.k AppConfig instance. KCL uses `=`; the `:` form is
// accepted defensively. A best-effort text read is deliberate: the value is a
// plain string literal in a forge-scaffolded instance, and this candidate is
// probed for reachability downstream (a wrong read is skipped, not fatal), so a
// full KCL evaluation would be disproportionate.
var kclConfigDBURLRE = regexp.MustCompile(`(?m)^\s*database_url\s*[:=]\s*"([^"]*)"`)

// configDatabaseURL reads database_url from the project's per-env KCL config
// source (deploy/kcl/<env>/config.k). Any error (no file, no key) yields "".
func configDatabaseURL(projectDir, env string) string {
	path := filepath.Join(projectDir, "deploy", "kcl", env, "config.k")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	m := kclConfigDBURLRE.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// devStackConventionDSN builds the DSN of the postgres the scaffold's
// docker-compose maps to the host: localhost:${POSTGRES_PORT:-5432} with
// ${POSTGRES_USER:-postgres}/${POSTGRES_PASSWORD:-postgres}. These are the
// exact variables (and defaults) compose reads, so the coordinates match
// whatever the dev stack actually runs at.
func devStackConventionDSN() string {
	user := envOr("POSTGRES_USER", "postgres")
	pass := envOr("POSTGRES_PASSWORD", "postgres")
	port := envOr("POSTGRES_PORT", "5432")
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     "localhost:" + port,
		Path:     "/postgres",
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// toMaintenanceServer reduces a DSN to its SERVER coordinates against the
// maintenance database "postgres": scheme, credentials, host and port are
// kept; the path is forced to /postgres and sslmode defaulted to disable
// when absent. This guarantees the scratch shadow DB is created off the
// maintenance DB and the app's own database is never opened. A DSN that
// will not parse (or names no host) yields "".
func toMaintenanceServer(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = "/postgres"
	q := u.Query()
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// getenv is os.Getenv with surrounding whitespace trimmed — a value pasted
// with a trailing newline should not defeat the "is it set" check.
func getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// envOr returns the trimmed env var value or def when unset/blank.
func envOr(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
