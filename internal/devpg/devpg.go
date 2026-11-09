// Package devpg owns ONE fact: the host coordinates of the dev postgres a
// forge project's docker-compose publishes.
//
// # Why this package exists
//
// That fact used to be declared in two places that could disagree:
//
//   - docker-compose.yml published postgres on `${POSTGRES_PORT:-5432}`, and
//   - the scaffolded DATABASE_URL secret pinned an ABSOLUTE DSN on a
//     hardcoded port (5434).
//
// The absolute URL wins at runtime, so the two disagreed by DEFAULT — and
// silently. `POSTGRES_PORT=15433 forge run` started THIS project's postgres on
// 15433 and then connected the app to whatever happened to be listening on
// 5434: another stack's database. forge created the database there, migrated
// it, seeded it, printed a healthy banner and exited 0. The only evidence was
// rows in a database the project did not own.
//
// The fix is to stop declaring the fact twice. This package derives the dev
// postgres coordinates from the SAME environment variables (and the same
// defaults) that docker-compose itself reads, so the scaffolded DSN and the
// container's published port are two renderings of one input rather than two
// independent claims. Reconcile then checks that they still agree at run time,
// because a project scaffolded under one POSTGRES_PORT can be run under
// another — a correct default alone cannot close that gap.
package devpg

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// The compose variables and defaults. These MUST stay identical to the
// scaffold's docker-compose.yml postgres service
// (internal/templates/project/docker-compose.yml.tmpl); the whole point of
// this package is that both sides read one set of names and defaults.
const (
	// PortEnv is the env var compose expands for postgres's published host port.
	PortEnv = "POSTGRES_PORT"
	// DefaultPort is compose's `${POSTGRES_PORT:-5432}` fallback.
	DefaultPort = "5432"

	// UserEnv / DefaultUser and PasswordEnv / DefaultPassword mirror the
	// POSTGRES_USER / POSTGRES_PASSWORD the compose postgres is initialised with.
	UserEnv         = "POSTGRES_USER"
	DefaultUser     = "postgres"
	PasswordEnv     = "POSTGRES_PASSWORD"
	DefaultPassword = "postgres"
)

// Port is the host port the dev postgres publishes: $POSTGRES_PORT, else
// compose's 5432 default.
func Port() string { return envOr(PortEnv, DefaultPort) }

// User is the dev postgres superuser: $POSTGRES_USER, else "postgres".
func User() string { return envOr(UserEnv, DefaultUser) }

// Password is the dev postgres password: $POSTGRES_PASSWORD, else "postgres".
func Password() string { return envOr(PasswordEnv, DefaultPassword) }

// DSN is the connection string for the named database on the dev postgres —
// the value the scaffold seeds into the env's secret store as DATABASE_URL. Built from
// Port/User/Password so it names the port compose actually publishes.
func DSN(database string) string {
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(User(), Password()),
		Host:     "localhost:" + Port(),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// PortOf extracts the host port from a postgres DSN. A DSN that will not
// parse, names no host, or carries no explicit port yields "".
func PortOf(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Port()
}

// HostOf extracts the hostname (no port) from a postgres DSN, or "".
func HostOf(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// DatabaseOf extracts the database name from a postgres DSN, or "".
func DatabaseOf(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// isLoopback reports whether a DSN host names this machine. The reconcile
// below only governs DSNs that point at the LOCAL compose stack; a dev DSN
// aimed at a remote/managed postgres is a deliberate choice and compose's
// published port says nothing about it.
func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// Reconcile reports whether dsn agrees with the port compose publishes for
// this project's postgres, returning a runbook error when it does not.
//
// This is the guard that makes the corruption impossible rather than
// unlikely. A project scaffolded when POSTGRES_PORT was unset carries a DSN
// on 5432; run it later under POSTGRES_PORT=15433 and compose moves the
// database while the DSN does not follow. Rather than let the app connect to
// whatever else is listening on 5432 — on a developer machine running several
// stacks, that is reliably SOMEONE ELSE'S postgres — forge stops and names
// both values.
//
// composePort is the port the project's compose file publishes (see
// ComposePublishedPort). An empty composePort or a non-loopback / port-less
// DSN means there is nothing to reconcile against, and the check passes.
func Reconcile(dsn, composePort string) error {
	if composePort == "" {
		return nil
	}
	dsnPort := PortOf(dsn)
	if dsnPort == "" || !isLoopback(HostOf(dsn)) {
		return nil
	}
	if dsnPort == composePort {
		return nil
	}
	return &MismatchError{
		DSNPort:     dsnPort,
		ComposePort: composePort,
		Database:    DatabaseOf(dsn),
	}
}

// MismatchError reports a DATABASE_URL whose port is not the port this
// project's compose publishes postgres on. Its message is a runbook: it names
// both values, says what would have happened, and gives the literal fix.
type MismatchError struct {
	// DSNPort is the port DATABASE_URL names.
	DSNPort string
	// ComposePort is the port docker-compose publishes postgres on.
	ComposePort string
	// Database is the database name in the DSN (may be "").
	Database string
}

func (e *MismatchError) Error() string {
	db := e.Database
	if db == "" {
		db = "<database>"
	}
	return fmt.Sprintf(
		"dev database port disagreement — refusing to continue.\n"+
			"  DATABASE_URL names port %s\n"+
			"  docker-compose publishes this project's postgres on port %s\n"+
			"\n"+
			"Continuing would connect to whatever else is listening on %s and create,\n"+
			"migrate and seed database %q THERE — in another stack's postgres, while this\n"+
			"project's own database stays empty. That write would succeed silently, so\n"+
			"forge stops here instead.\n"+
			"\n"+
			"Fix — make the two agree (pick one):\n"+
			"  * point the DSN at compose:  forge secret set --env dev DATABASE_URL (port %s)\n"+
			"  * point compose at the DSN:  re-run with %s=%s\n",
		e.DSNPort, e.ComposePort, e.DSNPort, db, e.ComposePort, PortEnv, e.DSNPort)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
