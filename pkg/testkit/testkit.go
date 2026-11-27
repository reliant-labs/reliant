// Package testkit provides a small set of test helpers that every
// forge-generated bootstrap_testing.go reinvents: a test-bound logger, a
// real-postgres ORM context (bare, or with the project's embedded
// migrations applied), an httptest harness that wraps a Connect-mounted
// service, and a claims-bearing AuthedContext for handlers that read the
// current user.
//
// # Why not absorb the whole bootstrap_testing.go?
//
// The per-service NewTest<Service>(t, opts...) factory is project-specific:
// it knows the service's Deps shape, its Register method, and the proto
// Connect client type. None of that compresses into a library helper —
// every project's test factory looks slightly different. testkit only
// holds the genuinely shared sub-helpers; the wiring shim stays codegen.
//
// # Usage in generated code
//
// Forge's bootstrap_testing.go template calls into testkit from
// defaultTestConfig:
//
//	cfg := &testConfig{
//	    logger: testkit.Logger(t), // shown only if the test fails
//	    cfg:    config.Defaults(), // the config Load yields with nothing set
//	    db:     testkit.NewPostgresDB(t), // when AnyServiceHasDB
//	}
//
// Projects with embedded migrations also get a migrated variant
// (app.NewMigratedTestDB → NewMigratedPostgresDB) for tests that need the
// real schema.
//
// # Real postgres, not SQLite
//
// forge is postgres-pinned. The DB helpers boot a real ephemeral
// postgres (pkg/pgtest: embedded-postgres by default, or the
// FORGE_TEST_POSTGRES_URL server) and hand each test its own isolated
// database. This is the same engine production runs, so migrations apply
// verbatim and there is no SQLite-portability subset to honor. The first
// call in a process boots the shared server (downloading the pg binary on
// a fresh machine); subsequent calls are cheap per-test databases.
//
// And NewTest<Svc>Server delegates to testkit.NewTestServer(t, register),
// mounting the service bare:
//
//	srv := testkit.NewTestServer(t, func(mux *http.ServeMux) {
//	    svc.Register(mux)
//	})
package testkit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// DiscardLogger returns a slog.Logger that drops every record. Use it as
// the default logger in unit tests where log output would be noise. The
// logger is safe for concurrent use and never returns errors.
//
// Tests that need to assert on log lines should construct their own
// logger backed by a *bytes.Buffer or testing.TB.Log; this helper exists
// for the common "I do not care what gets logged" case.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// NewPostgresDB returns a fresh, isolated real-postgres ORM client
// suitable for hermetic unit tests. The database is empty — callers that
// need a schema should run migrations (NewMigratedPostgresDB) or create
// tables explicitly. The connection and the underlying database are
// cleaned up via t.Cleanup, so the caller does not need to defer a close.
//
// Each call yields its OWN database on the process-shared ephemeral
// postgres (pkg/pgtest), so two NewPostgresDB calls in the same test are
// fully isolated — the right default for table-driven tests that mutate
// rows. The first call in a process boots the shared server (downloading
// the postgres binary on a fresh machine, or connecting to
// FORGE_TEST_POSTGRES_URL); subsequent calls only CREATE DATABASE.
func NewPostgresDB(t testing.TB) orm.Context {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		cleanup()
		t.Fatalf("create ORM client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		cleanup()
	})
	return client
}

// NewMigratedPostgresDB returns a real-postgres ORM client with the
// project's embedded migrations applied, so handler preconditions (tables,
// indexes) hold in unit tests exactly as they do after AutoMigrate in
// production. migrations is typically the project's embedded
// `forgedb.MigrationsFS` (db/embed.go) — the same FS pkg/app/migrate.go
// feeds to golang-migrate. Generated bootstrap testing code exposes this
// as the app.NewMigratedTestDB(t) helper whenever the project has
// migrations.
//
// Files are discovered under a "migrations/" directory inside the FS when
// present (matching the embed layout `//go:embed migrations/*.sql`),
// falling back to the FS root. Only `*.up.sql` files run, ordered by their
// numeric version prefix (the `NNNN_name.up.sql` golang-migrate
// convention), falling back to lexicographic order for non-numeric names.
//
// The SQL executes against real postgres verbatim — the same engine
// production runs — so there is no portability subset to honor. A
// migration that postgres rejects fails loudly here (t.Fatalf names the
// file).
func NewMigratedPostgresDB(t testing.TB, migrations fs.FS) orm.Context {
	t.Helper()
	key, err := migrationsKey(migrations, upSQL)
	if err != nil {
		t.Fatalf("hash embedded migrations: %v", err)
	}
	// The migrations are applied ONCE per server (pgtest.NewFromTemplate) and each
	// test gets a clone, so N tests cost one replay of the chain instead of N.
	db, _, cleanup, err := pgtest.NewFromTemplate(key, func(db *sql.DB, _ string) error {
		if err := applyUpMigrations(db, migrations); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		var me *migrationError
		if errors.As(err, &me) {
			t.Fatalf("apply embedded migrations: %v", err)
		}
		t.Fatalf("open test postgres: %v", err)
	}
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		cleanup()
		t.Fatalf("create ORM client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		cleanup()
	})
	return client
}

// NewMigratedPostgresURL returns the DSN (a postgres:// URL) of a fresh,
// isolated real-postgres database with the project's embedded migrations
// applied — for code that connects by URL rather than taking an orm.Context:
// the whole server's composition graph booted in process (it opens its own
// pool from its config's DATABASE_URL), a second pool, a subprocess.
//
//	dsn := testkit.NewMigratedPostgresURL(t, forgedb.Source())
//	cfg := config.Defaults()
//	cfg.DatabaseUrl = dsn
//
// Unlike NewMigratedPostgresDB, the migrations are applied by pkg/migratekit,
// the migrator a server runs at boot, with its applied record and read-only
// grants. So a server root booted on the DSN with AUTO_MIGRATE on finds the
// schema current and applies nothing, instead of replaying the chain onto
// tables that already exist. migrations is laid out as for
// NewMigratedPostgresDB: the SQL under "migrations/" when present (the
// embedded `forgedb.Source()` layout), else at the root, in golang-migrate's
// NNNN_name.up.sql naming.
//
// The chain runs once per migration set and every call clones it (pgtest's
// templates), so the database costs a CREATE DATABASE. It is dropped via
// t.Cleanup, after every other session on it is terminated.
func NewMigratedPostgresURL(t testing.TB, migrations fs.FS) string {
	t.Helper()
	key, err := migrationsKey(migrations, func(string) bool { return true })
	if err != nil {
		t.Fatalf("hash embedded migrations: %v", err)
	}
	_, dir := migrationsRoot(migrations)
	// A key of its own: a template built by the raw replay NewMigratedPostgresDB
	// runs carries no applied record, and serving one here would make the
	// server's boot migration replay the chain.
	db, dsn, cleanup, err := pgtest.NewFromTemplate("migratekit-"+key, func(db *sql.DB, _ string) error {
		if _, err := migratekit.Apply(context.Background(), db, migrations, dir); err != nil {
			return &migrationError{file: "(migratekit.Apply over " + dir + ")", err: err}
		}
		return nil
	})
	if err != nil {
		var me *migrationError
		if errors.As(err, &me) {
			t.Fatalf("apply embedded migrations: %v", err)
		}
		t.Fatalf("open test postgres: %v", err)
	}
	// The caller connects by DSN; this handle would only hold a session open.
	if err := db.Close(); err != nil {
		cleanup()
		t.Fatalf("close test postgres pool before returning DSN: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// migrationsRoot is where the SQL of an embedded migration set sits: its
// "migrations/" directory when that holds anything, else the FS root. The dir
// it names is the path inside migrations, as migratekit takes it.
func migrationsRoot(migrations fs.FS) (fs.FS, string) {
	if sub, err := fs.Sub(migrations, "migrations"); err == nil {
		if entries, err := fs.ReadDir(sub, "."); err == nil && len(entries) > 0 {
			return sub, "migrations"
		}
	}
	return migrations, "."
}

// upSQL selects the files NewMigratedPostgresDB runs.
func upSQL(p string) bool { return strings.HasSuffix(p, ".up.sql") }

// migrationsKey names a migration set for pgtest.NewFromTemplate: the names
// and bytes of every file include selects (every *.up.sql, for the raw
// replay; every file, grants included, for migratekit), so any edit, addition
// or rename builds a new template and a stale one is never served.
func migrationsKey(migrations fs.FS, include func(path string) bool) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(migrations, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !include(p) {
			return err
		}
		b, err := fs.ReadFile(migrations, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:20], nil
}

// applyUpMigrations runs every *.up.sql file in migrations against db in
// version order. Split out of NewMigratedPostgresDB for direct testing.
//
// It keeps the migrator's two dashboard-role rules (pkg/migratekit,
// dashboard.go), so a migration that works in a test works in production and
// the reverse: the role exists before the first file runs, because a migration
// may GRANT to it, and after the last one it may hold read privileges only.
func applyUpMigrations(db *sql.DB, migrations fs.FS) error {
	ctx := context.Background()
	// The test server's role is a superuser, so the role is always created
	// here; ErrRoleNotCreatable would mean a server of the caller's own, and a
	// migration that needs the role then fails naming it.
	if _, err := dbgrants.EnsureDashboardRole(ctx, db); err != nil && !errors.Is(err, dbgrants.ErrRoleNotCreatable) {
		return err
	}
	if err := runUpMigrations(db, migrations); err != nil {
		return err
	}
	violations, err := dbgrants.DashboardPrivilegeViolations(ctx, db, dbgrants.DashboardRoleName)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return &migrationError{file: "(after the last migration)", err: errors.New(
			dbgrants.DescribeViolations(dbgrants.DashboardRoleName, violations))}
	}
	return nil
}

func runUpMigrations(db *sql.DB, migrations fs.FS) error {
	root, _ := migrationsRoot(migrations)
	names, err := fs.Glob(root, "*.up.sql")
	if err != nil {
		return err
	}
	sort.Slice(names, func(i, j int) bool {
		vi, oki := migrationVersion(names[i])
		vj, okj := migrationVersion(names[j])
		if oki && okj && vi != vj {
			return vi < vj
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		sqlBytes, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return &migrationError{file: name, err: err}
		}
	}
	return nil
}

// migrationError names the failing migration file so the t.Fatalf in
// NewMigratedPostgresDB points straight at the offending SQL.
type migrationError struct {
	file string
	err  error
}

func (e *migrationError) Error() string { return "migration " + e.file + ": " + e.err.Error() }
func (e *migrationError) Unwrap() error { return e.err }

// migrationVersion parses the numeric version prefix from a
// golang-migrate-style filename ("0002_add_users.up.sql" → 2).
func migrationVersion(name string) (int64, bool) {
	idx := strings.IndexByte(name, '_')
	if idx <= 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(name[:idx], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// NewTestServer starts an httptest.Server backed by a fresh
// http.ServeMux and invokes register so callers can mount one or more
// Connect services on the mux. The server is closed via t.Cleanup, so
// the caller does not need to defer srv.Close.
//
// The split — register receives the mux instead of the server — keeps
// testkit independent of any specific service type. A typical call site
// looks like:
//
//	srv := testkit.NewTestServer(t, func(mux *http.ServeMux) {
//	    svc.Register(mux, connect.WithInterceptors(/*...*/))
//	})
//	client := myservicev1connect.NewMyServiceClient(http.DefaultClient, srv.URL)
//
// The client construction stays in the per-service test factory because
// it requires the proto-specific connect package and client constructor.
func NewTestServer(t *testing.T, register func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ApplyOptions applies each functional option to cfg and returns it.
//
// Every generated per-component test factory opens with the same three lines —
// build the default config, range over the variadic options, call each — and it
// does so once per factory (NewTest<Svc>, NewTest<Svc>Server, and any the user
// adds). This is that loop, and it is the one piece of the option machinery
// that is expressible here: it never names the option's config type, only
// applies functions to it.
//
// The options THEMSELVES cannot live here, and the reason is worth stating
// because it is the boundary of what this package can absorb. A per-component
// option is a `func(*testConfig)` where testConfig holds the project's own
// *config.Config and that component's own Deps — project types, unknown to
// forge/pkg. Expressing them generically (Option[C, D]) compiles, but Go infers
// type parameters only from ARGUMENTS, never from the return type, so every
// call site would have to spell them out:
//
//	NewTestOrder(t, testkit.WithLogger[config.Config, Deps](lg))   // generic
//	NewTestOrder(t, WithLogger(lg))                                // today
//
// That is a worse test-writing experience in every project, permanently, to
// delete three-line functions from a generated file. So the options stay
// generated per component and only their application loop is shared.
//
// O is a second type parameter rather than a plain `...func(*C)` because the
// per-component option is a NAMED type (`type TestOption func(*testConfig)`),
// and Go does not assign []TestOption to []func(*testConfig) — a named type and
// its underlying type are not identical in that position. Constraining O by
// `~func(*C)` accepts the named type and infers both parameters from the
// arguments.
//
// Call it by FORWARDING a slice — `ApplyOptions(cfg, opts...)` — which is what
// a variadic factory already has. Passing no variadic argument at all leaves O
// with nothing to infer from and does not compile; forward an empty (even nil)
// slice for the no-options case.
func ApplyOptions[C any, O ~func(*C)](cfg *C, opts ...O) *C {
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// NewConnectClient starts a test server with register mounted on its mux and
// builds a typed Connect client against it via newClient.
//
// It folds together the two halves every generated NewTest<Svc>Server does by
// hand: [NewTestServer] (mux, httptest.Server, t.Cleanup) and the client
// construction, which needs the proto-specific `<svc>v1connect.New<Svc>Client`
// constructor. That constructor is generated code this package cannot name, so
// it arrives as a parameter — and because it is a plain argument, its type
// parameter INFERS at the call site, unlike the option types above:
//
//	srv, client := testkit.NewConnectClient(t,
//	    svc.Register, orderv1connect.NewOrderServiceClient)
//
// The server is returned alongside the client for tests that need its URL or
// want to close it early; it is already registered for cleanup.
// register matches the generated service's own Register method, which takes
// variadic connect.HandlerOption alongside the mux; handlerOpts are forwarded
// to it (e.g. [ServerPrincipal]), followed by svcerr.Interceptor, innermost.
//
// That last one is what production has: forge's chain builders end every
// handler chain with svcerr.Interceptor, so a handler returning a bare
// `svcerr.InvalidArgument("…")` answers invalid_argument. A test server
// without it answered unknown for the same handler — the documented handler
// shape failed only under test, and an unrecognised error published its raw
// text here while production redacts it. Passing svcerr.Interceptor yourself
// stays harmless: Wrap is idempotent.
//
// Outside svcerr sits production's logging interceptor
// (observe.LoggingInterceptor), writing to [Logger](t). So when a call fails
// with a redacted "internal server error", the record of it — the procedure,
// the code, and the `cause` the client was never shown — is printed with the
// test's output if the test fails, and nowhere if it passes.
//
// Production also validates every request against its protovalidate rules
// before svcerr, rejecting an invalid one with invalid_argument. The test
// server checks the same rules but, for now, only logs a violation on t and
// lets the request through: pass [ValidateRequests] to reject it as
// production does. A later release makes rejecting the default (see
// validate.go).
//
// connect-go v2 services use NewConnectClientV2.
func NewConnectClient[C any](
	t *testing.T,
	register func(mux *http.ServeMux, opts ...connect.HandlerOption),
	newClient func(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) C,
	handlerOpts ...connect.HandlerOption,
) (*httptest.Server, C) {
	t.Helper()
	// Production's JSON codec first (serverkit.Chain's), so a JSON request a
	// test sends gets the error a client would; a handlerOpts codec wins.
	opts := append([]connect.HandlerOption{mountkit.JSONCodecOption()}, handlerOpts...)
	if warn := newWarnInvalidRequests(t); warn != nil {
		opts = append(opts, connect.WithInterceptors(warn))
	}
	// Outside svcerr, as in production: it logs the wire error and the cause
	// svcerr withheld from the client, to the test's own log.
	opts = append(opts, connect.WithInterceptors(observe.LoggingInterceptor(Logger(t))))
	opts = append(opts, connect.WithInterceptors(svcerr.Interceptor()))
	srv := NewTestServer(t, func(mux *http.ServeMux) { register(mux, opts...) })
	// Traced, so a test's own span is the parent of the server's.
	return srv, newClient(observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: -1}), srv.URL)
}

// ServerPrincipal returns a handler option that installs a test principal in
// the HANDLER's context on every call — unary and streaming alike — through
// the project's own claims setter (middleware.ContextWithClaims), the same
// function the production auth interceptor calls.
//
// This is the over-the-wire twin of [AuthedContext], and it exists because
// AuthedContext cannot do this job: it puts claims on a context the TEST
// holds, and a context does not cross HTTP. A test that drives a service
// through its typed Connect client therefore needs the principal installed
// where the handler runs, or every auth-gated RPC answers Unauthenticated
// before reaching the logic under test.
//
// It stands in for the production authentication step only — whatever the
// handler does with the principal (its own role checks, org scoping) still
// runs, so a test exercises denials by passing claims that lack the role:
//
//	testkit.ServerPrincipal(middleware.ContextWithClaims, testkit.WithRoles("viewer"))
//
// Defaults are AuthedContext's (UserID "test-user", Role/Roles "admin"), so
// an in-process row and an over-the-wire row agree on who is calling.
//
// connect-go v2 test servers use ServerPrincipalV2.
func ServerPrincipal(withClaims func(context.Context, *auth.Claims) context.Context, opts ...ClaimsOption) connect.HandlerOption {
	return connect.WithInterceptors(serverPrincipal{install: func(ctx context.Context) context.Context {
		claims := defaultTestClaims()
		for _, opt := range opts {
			opt(claims)
		}
		return withClaims(ctx, claims)
	}})
}

// serverPrincipal is the interceptor behind [ServerPrincipal]. Claims are
// rebuilt per call so a handler that mutates the principal it receives cannot
// leak that change into the next call.
type serverPrincipal struct {
	install func(context.Context) context.Context
}

func (p serverPrincipal) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(p.install(ctx), req)
	}
}

// WrapStreamingClient is a pass-through: this option is for handlers, and a
// server never originates a client stream.
func (serverPrincipal) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (p serverPrincipal) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(p.install(ctx), conn)
	}
}

// ClaimsOption mutates the default test claims built by [AuthedContext].
type ClaimsOption func(*auth.Claims)

// WithUserID overrides the test claims' UserID.
func WithUserID(id string) ClaimsOption {
	return func(c *auth.Claims) { c.UserID = id }
}

// WithEmail overrides the test claims' Email.
func WithEmail(email string) ClaimsOption {
	return func(c *auth.Claims) { c.Email = email }
}

// WithOrgID overrides the test claims' OrgID.
func WithOrgID(orgID string) ClaimsOption {
	return func(c *auth.Claims) { c.OrgID = orgID }
}

// WithRoles overrides the test claims' role set. The first role also
// becomes the singular Role field, matching how the auth validator
// populates both.
func WithRoles(roles ...string) ClaimsOption {
	return func(c *auth.Claims) {
		c.Roles = roles
		if len(roles) > 0 {
			c.Role = roles[0]
		} else {
			c.Role = ""
		}
	}
}

// WithAPIKey makes the test principal a caller that authenticated with an
// API key whose lookup prefix ([apikey.LookupPrefix]) is keyPrefix: its
// Credential is auth.APIKeyCredential(keyPrefix), the mark forge's
// Authenticator stamps on every key it checks, so claims.IsAPIKey() and
// authn.IsAPIKey(ctx) report true and Credential.KeyPrefix() returns
// keyPrefix. A key has no person behind it, so UserID becomes
// "apikey:<keyPrefix>" (the auth/api-keys skill's convention) and Email is
// cleared.
//
// The key's role and tenant are whatever your key validator returns for its
// row; set them with the other options, in any order:
//
//	testkit.ServerPrincipalV2(middleware.ContextWithClaims,
//	    testkit.WithAPIKey("abcd1234"),
//	    testkit.WithRoles("dataplane"),
//	    testkit.WithOrgID(tenantID))
//
// Without WithRoles the principal keeps the default role, "admin". WithClaims
// replaces every field, the credential included, so it goes before WithAPIKey.
//
// [apikey.LookupPrefix]: https://pkg.go.dev/github.com/reliant-labs/forge/pkg/apikey#LookupPrefix
func WithAPIKey(keyPrefix string) ClaimsOption {
	return func(c *auth.Claims) {
		c.UserID = "apikey:" + keyPrefix
		c.Email = ""
		c.EmailVerified = false
		c.Credential = auth.APIKeyCredential(keyPrefix)
	}
}

// WithClaims replaces the default test claims wholesale. Later options
// still apply on top.
func WithClaims(claims auth.Claims) ClaimsOption {
	return func(c *auth.Claims) { *c = claims }
}

// AuthedContext returns a context carrying authenticated test claims, so
// handlers that read the current user (middleware.GetUser /
// middleware.ClaimsFromContext) see a real principal instead of failing
// CodeUnauthenticated before reaching business logic.
//
// The claims context key is project-local — it lives in the generated
// pkg/middleware package, deliberately unexported so nothing bypasses the
// middleware. withClaims is therefore the project's own setter, the SAME
// function the production auth interceptor uses to install claims:
//
//	ctx := testkit.AuthedContext(t, middleware.ContextWithClaims)
//
// Generated projects re-export this with the setter pre-bound as
// app.AuthedContext(t, opts...) — prefer that form in project tests.
//
// Default claims: UserID "test-user", Email "test-user@example.test",
// Role/Roles "admin" (a convenient default for handlers that check roles).
// Override via ClaimsOption values.
func AuthedContext(t *testing.T, withClaims func(context.Context, *auth.Claims) context.Context, opts ...ClaimsOption) context.Context {
	t.Helper()
	claims := defaultTestClaims()
	for _, opt := range opts {
		opt(claims)
	}
	return withClaims(context.Background(), claims)
}

// defaultTestClaims is the one default test principal, shared by
// [AuthedContext] (in-process) and [ServerPrincipal] (over the wire).
func defaultTestClaims() *auth.Claims {
	return &auth.Claims{
		UserID: "test-user",
		Email:  "test-user@example.test",
		Role:   "admin",
		Roles:  []string{"admin"},
	}
}
