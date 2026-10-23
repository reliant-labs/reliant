// File: internal/cli/db_target.go
//
// Reconciling the DSN a db command ACTS ON with the environment it CLAIMS.
//
// The dev gate and the connection string were resolved on two paths that
// never met. `requireDevSeedTarget(env)` classifies the environment by
// reading deploy/kcl/<env>/config.k for `environment = "development"` — it is
// fail-closed and has no override, which is right. But `resolveDSN` took
// --dsn, else $DATABASE_URL, else error, and never consulted --env at all.
//
// So this passed:
//
//	forge db seed reset --env dev --dsn postgres://prod-host/app
//
// The dev CONFIG FILE says development, so the gate opened; the command then
// acted on production. A stale exported DATABASE_URL does the same with no
// flag at all. Validating "is this env dev?" while acting on a connection
// string nobody checked is a guard in name only — and behind a DROP DATABASE
// it is unacceptable.
//
// The close: the DSN must be reconcilable with the DSN the environment
// ITSELF declares. Not equal — a DSN is not a canonical form, and refusing a
// correct configuration teaches people to route around the guard, which is
// permanent where a false alarm was not. Equal in the only sense that
// decides which rows get dropped: SERVER and DATABASE NAME.

package cli

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// databaseIdentity reduces a DSN to the pair that decides WHICH DATABASE it
// names: normalized server coordinates and the database name, as
// "host:port/database". Credentials and query parameters are deliberately
// excluded — connecting as a different role, or with a different sslmode,
// does not make it a different database.
//
// Host normalization folds every spelling of "this machine" onto localhost,
// including the docker/k3d gateway aliases: a DSN written from a POD's point
// of view reaches the developer's machine through host.k3d.internal, and it
// denotes the very database a loopback DSN would. An absent port is the
// postgres default, 5432, so the implicit and explicit spellings agree.
//
// A DSN that will not parse, names no host, or names no database yields "" —
// the caller must treat that as "cannot be reconciled", never as a match.
func databaseIdentity(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return ""
	}
	host := u.Hostname()
	switch host {
	case "127.0.0.1", "::1", "0.0.0.0", "host.docker.internal", k3dHostGatewayAlias:
		host = "localhost"
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return host + ":" + port + "/" + name
}

// reconcileClaimedDSN reports whether the DSN a command is about to act on is
// the database the named environment declares, returning a refusal when they
// cannot be reconciled.
//
// declared is the DSN resolved from the ENVIRONMENT'S OWN configuration (see
// declaredEnvDSN) — deliberately not from $DATABASE_URL, because an ambient
// variable is the very thing being checked. An empty declared is a REFUSAL,
// not a pass: "I could not check" and "I checked and it is fine" are
// different answers, and only one of them may precede a destructive command.
func reconcileClaimedDSN(claimed, declared, env string) error {
	claimedID := databaseIdentity(claimed)
	if claimedID == "" {
		return fmt.Errorf("refusing to act on %s: it does not name a server and database, so forge cannot tell which database it would affect",
			redactDSNForMessage(claimed))
	}

	if declared == "" {
		// revive's error-strings rule wants a lowercase fragment with no
		// terminal punctuation, which is right for an error that will be
		// WRAPPED — the reader sees "doing x: <fragment>". This one is
		// terminal and operator-facing: multiple paragraphs that explain the
		// refusal and name the fix, printed as-is. Stripping its punctuation
		// would damage prose a person reads, to satisfy a convention for
		// strings a program concatenates.
		//nolint:revive,staticcheck // terminal operator-facing prose; see above
		return fmt.Errorf(`refusing to act on %s: environment %q declares no database of its own, so forge cannot confirm this connection string belongs to it.

This check exists because the environment gate and the connection string used to be resolved separately — %q could be confirmed as a dev environment while the DSN pointed somewhere else entirely.

Give the environment a database to be reconciled against: declare DATABASE_URL in deploy/kcl/%s/ (or set it in the env's secret provider), then re-run.`,
			claimedID, env, env, env)
	}

	declaredID := databaseIdentity(declared)
	if declaredID == "" {
		return fmt.Errorf("refusing to act on %s: environment %q declares a database URL forge cannot parse (%s), so the two cannot be reconciled",
			claimedID, env, redactDSNForMessage(declared))
	}

	if claimedID == declaredID {
		return nil
	}

	//nolint:revive,staticcheck // terminal operator-facing prose; see the note above
	return fmt.Errorf(`refusing to act on a database that is not environment %q's.

  you asked for:   %s
  %-14s   %s

These are different databases. Environment %q was confirmed from deploy/kcl/%s/config.k, but the connection string names somewhere else — so the environment check said "dev" while the command would have acted on that other database.

If the connection string is right, the environment is wrong (pass --env). If the environment is right, drop --dsn and unset DATABASE_URL so forge uses what %q declares.`,
		env, claimedID, env+" declares:", declaredID, env, env, env)
}

// redactDSNForMessage masks a password before a DSN reaches an error message.
// A DSN that will not parse is reported opaquely rather than echoed, since an
// unparseable string is exactly the one whose shape is unknown.
func redactDSNForMessage(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "the given connection string"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}

// declaredEnvDSN resolves the DATABASE_URL the ENVIRONMENT declares — the
// value forge reconciles a claimed DSN against.
//
// It deliberately does NOT read $DATABASE_URL. resolveSeedDSN does, and
// correctly: it answers "which database will the app dial", and the shell
// wins there because it wins for the launched process too. This function
// answers a different question — "which database does this environment SAY is
// its own" — and an ambient variable is the thing under suspicion, not
// evidence about it. Reading it here would make the check compare a value
// against itself and pass unconditionally, which is the hole rather than the
// fix.
//
// Sources, in order: the env's rendered KCL (what the workloads are actually
// launched with), then its secret provider (load-bearing for a project whose
// DSN is only a secret), then the per-env config.k `database_url` field, then
// per-env project config. "" when the environment declares none, which callers
// treat as a refusal.
//
// The config.k source is the same one shadowdb.Resolve reads, and it matters
// for the same reason: a project that did NOT mark database_url sensitive
// keeps its real DSN there, and nowhere else. Omitting it made the gate
// fail-closed on a perfectly ordinary project — which is the failure mode that
// teaches people to route around a guard, and the workaround outlives the
// false alarm.
func declaredEnvDSN(ctx context.Context, projectDir, env string) string {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err == nil && entities != nil {
		for _, dsn := range declaredDatabaseURLs(entities) {
			if dsn != "" {
				return dsn
			}
		}
		if provider, perr := secretProviderFromEntities(entities, projectDir); perr == nil {
			if v, ok := provider.Resolve("DATABASE_URL"); ok && v != "" {
				return v
			}
		}
	}
	if v := envConfigDatabaseURL(projectDir, env); v != "" {
		return v
	}
	if m := loadProjectConfigEnv(nil, env); m["DATABASE_URL"] != "" {
		return m["DATABASE_URL"]
	}
	return ""
}

// kclConfigDBURLRE extracts `database_url = "value"` from a per-env config.k
// AppConfig instance. KCL uses `=`; the `:` form is accepted defensively.
// A best-effort text read, matching how the neighbouring MODE gate
// (envModeFromKCLConfig) reads `environment` from the same file: the value is
// a plain string literal in a scaffolded instance, and a full KCL evaluation
// would be disproportionate.
var kclConfigDatabaseURLRE = regexp.MustCompile(`(?m)^\s*database_url\s*[:=]\s*"([^"]*)"`)

// envConfigDatabaseURL reads database_url from deploy/kcl/<env>/config.k, or
// "" when the file or the field is absent.
func envConfigDatabaseURL(projectDir, env string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "deploy", "kcl", env, "config.k"))
	if err != nil {
		return ""
	}
	m := kclConfigDatabaseURLRE.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// resolveEnvDSN is the env-aware replacement for resolveDSN on any command
// that claims an environment. It resolves the connection string the same way
// as before — --dsn, else $DATABASE_URL — and then RECONCILES it against what
// the environment declares, so the two halves of the decision finally meet.
//
// When neither --dsn nor $DATABASE_URL is set, the environment's own declared
// DSN is used. That is the good path: nothing to reconcile, because the value
// came from the environment in the first place.
func resolveEnvDSN(ctx context.Context, flagDSN, projectDir, env string) (string, error) {
	declared := declaredEnvDSN(ctx, projectDir, env)

	claimed := flagDSN
	source := "--dsn"
	if claimed == "" {
		claimed = strings.TrimSpace(os.Getenv("DATABASE_URL"))
		source = "$DATABASE_URL"
	}
	if claimed == "" {
		if declared == "" {
			return "", fmt.Errorf("database connection string required: pass --dsn, set DATABASE_URL, or declare one in deploy/kcl/%s/", env)
		}
		return declared, nil
	}

	if err := reconcileClaimedDSN(claimed, declared, env); err != nil {
		return "", fmt.Errorf("%w\n\n(the connection string came from %s)", err, source)
	}
	return claimed, nil
}

// resolveSeedWriteDSN is the DSN policy for the commands that WRITE seed rows
// (`seed apply`, `seed reset`). It is looser than resolveEnvDSN in exactly one
// place, and deliberately so.
//
// resolveEnvDSN admits only the env's own database. That is right for
// `forge db reset`, which DROPs the database — on a machine running several
// postgres instances that hold real data, "some loopback database" is not a
// target a DROP may guess at. But applied to seeding it left no supported way
// to seed a throwaway database: port 5432 is usually taken, a scratch postgres
// on 55432 was refused for not being the env's, and the dogfood run that hit
// it fell back to hand-written SQL.
//
// So for an explicit --dsn:
//
//   - a LOOPBACK server (localhost, 127.0.0.0/8, ::1, a unix socket) is
//     accepted on any port and any database name — it is this machine, the
//     operator named it on the command line, and the seed itself is
//     row-scoped (INSERT ... ON CONFLICT DO NOTHING, or TRUNCATE of the
//     seeded tables) and still refuses a database whose recorded migration
//     version is behind this project's;
//   - any other server must be the env's declared database, or the operator
//     must say --allow-remote-dsn. That is the guard against a production
//     DSN pasted from a runbook.
//
// The relaxation is for the FLAG only. An ambient $DATABASE_URL is evidence
// nobody chose for this command — a stale export from another project's
// shell — so with no --dsn the strict reconciliation applies as before. The
// dev gate (requireDevSeedTarget) is untouched by all of this.
func resolveSeedWriteDSN(ctx context.Context, flagDSN, projectDir, env string, allowRemote bool) (string, error) {
	if flagDSN == "" {
		return resolveEnvDSN(ctx, "", projectDir, env)
	}
	if isLoopbackDSN(flagDSN) || allowRemote {
		return flagDSN, nil
	}
	declared := declaredEnvDSN(ctx, projectDir, env)
	if id := databaseIdentity(flagDSN); id != "" && id == databaseIdentity(declared) {
		return flagDSN, nil
	}
	declaredNote := "declares no database of its own"
	if declared != "" {
		declaredNote = "declares " + databaseIdentity(declared)
	}
	//nolint:revive,staticcheck // terminal operator-facing prose; see reconcileClaimedDSN
	return "", fmt.Errorf(`refusing to seed %s: it is not a loopback server, and it is not environment %q's database (%q %s).

--dsn may name ANY loopback database — localhost, 127.0.0.1, ::1 or a unix socket, on any port — so a throwaway postgres needs no flag. A remote server is refused unless it is the env's own, because the one remote DSN most likely to be pasted into a seed command is production's.

If you do mean to seed that server, pass --allow-remote-dsn (the environment must still be dev).`,
		redactDSNForMessage(flagDSN), env, env, declaredNote)
}

// isLoopbackDSN reports whether every server a DSN can connect to is on this
// machine: localhost, a loopback IP, or a unix socket.
//
// It asks pgx's own parser — the same one ConnectDB dials with — rather than
// parsing the URL by hand, so it reads the DSN exactly the way the connection
// will: key=value DSNs, a `host=` query parameter, PGHOST and the other libpq
// environment defaults, the unix-socket default for a DSN with no host at
// all, and every multi-host fallback. A DSN pgx cannot parse is not loopback.
func isLoopbackDSN(dsn string) bool {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return false
	}
	if !isLocalPGHost(cfg.Host) {
		return false
	}
	for _, fb := range cfg.Fallbacks {
		if !isLocalPGHost(fb.Host) {
			return false
		}
	}
	return true
}

// isLocalPGHost classifies one resolved pgx host: a unix socket, localhost, or
// any loopback IP (127.0.0.0/8, ::1).
func isLocalPGHost(host string) bool {
	if strings.HasPrefix(host, "/") {
		return true // a unix-domain socket directory
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
