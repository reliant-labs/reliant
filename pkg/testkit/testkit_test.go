package testkit_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestDiscardLogger_DropsRecords(t *testing.T) {
	t.Parallel()
	logger := testkit.DiscardLogger()
	if logger == nil {
		t.Fatal("DiscardLogger returned nil")
	}
	// Doesn't panic with concurrent writes.
	logger.Info("ignore me", "k", "v")
	logger.Error("also ignore", "err", "boom")
	logger.With("scope", "test").Warn("with-attrs path")
}

// requirePG skips DB-backed testkit tests under -short: they boot the
// shared ephemeral postgres (pkg/pgtest), well over the 2s short budget.
func requirePG(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
}

func TestNewPostgresDB_ReturnsUsableContext(t *testing.T) {
	requirePG(t)
	t.Parallel()
	db := testkit.NewPostgresDB(t)
	if db == nil {
		t.Fatal("NewPostgresDB returned nil")
	}
	// Round-trip a row.
	if _, err := db.Exec(context.Background(), `CREATE TABLE t (id BIGSERIAL PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO t (v) VALUES ($1)`, "hello"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	row := db.QueryRow(context.Background(), `SELECT v FROM t WHERE id = 1`)
	var v string
	if err := row.Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if v != "hello" {
		t.Fatalf("got %q, want %q", v, "hello")
	}
}

func TestNewPostgresDB_IsolatedPerCall(t *testing.T) {
	requirePG(t)
	t.Parallel()
	a := testkit.NewPostgresDB(t)
	b := testkit.NewPostgresDB(t)
	if _, err := a.Exec(context.Background(), `CREATE TABLE only_in_a (id BIGSERIAL PRIMARY KEY)`); err != nil {
		t.Fatalf("create in a: %v", err)
	}
	// b is a separate database — it must not see a's table.
	_, err := b.Exec(context.Background(), `SELECT * FROM only_in_a`)
	if err == nil {
		t.Fatal("expected error querying a's table from b's database, got nil")
	}
}

func TestNewPostgresDB_ClosedAfterTest(t *testing.T) {
	requirePG(t)
	t.Parallel()
	var captured = make(chan struct{}, 1)
	t.Run("inner", func(inner *testing.T) {
		db := testkit.NewPostgresDB(inner)
		// Use it once to make sure it's live.
		if _, err := db.Exec(context.Background(), `CREATE TABLE x (id BIGSERIAL PRIMARY KEY)`); err != nil {
			inner.Fatalf("exec: %v", err)
		}
		inner.Cleanup(func() { captured <- struct{}{} })
	})
	select {
	case <-captured:
	default:
		t.Fatal("inner test cleanup did not run")
	}
	// We can't directly observe testkit's t.Cleanup-registered close (it
	// runs against the inner t, not ours), but the inner test would have
	// failed if NewPostgresDB panicked or leaked a connection that
	// couldn't be closed.
}

func TestNewTestServer_RegistersRoutes(t *testing.T) {
	t.Parallel()
	srv := testkit.NewTestServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
	})
	if srv == nil {
		t.Fatal("NewTestServer returned nil")
	}
	resp, err := http.Get(srv.URL + "/echo")
	if err != nil {
		t.Fatalf("GET /echo: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("got body %q, want %q", body, "ok")
	}
}

func TestNewTestServer_ClosedAfterTest(t *testing.T) {
	t.Parallel()
	var url string
	t.Run("inner", func(inner *testing.T) {
		srv := testkit.NewTestServer(inner, func(mux *http.ServeMux) {
			mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "live")
			})
		})
		url = srv.URL
		// Confirm live during inner test.
		resp, err := http.Get(url)
		if err != nil {
			inner.Fatalf("inner GET: %v", err)
		}
		_ = resp.Body.Close()
	})
	// Outer test: server must be closed.
	_, err := http.Get(url)
	if err == nil {
		t.Fatal("expected GET to fail after inner test cleanup, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") &&
		!strings.Contains(err.Error(), "EOF") &&
		!strings.Contains(err.Error(), "use of closed") {
		// Tolerate any net-error variant — the assertion is just "server
		// is no longer accepting requests".
		t.Logf("note: server-closed produced %v", err)
	}
}

func TestNewMigratedPostgresDB_AppliesUpMigrationsInVersionOrder(t *testing.T) {
	requirePG(t)
	t.Parallel()
	// Embed-shaped FS: files under migrations/, golang-migrate naming.
	// 10_ sorts before 2_ lexically — numeric version order is required
	// for the INSERT (10) to find the column added in (2).
	mfs := fstest.MapFS{
		"migrations/1_init.up.sql":       {Data: []byte(`CREATE TABLE users (id BIGINT PRIMARY KEY);`)},
		"migrations/1_init.down.sql":     {Data: []byte(`DROP TABLE users;`)},
		"migrations/2_add_name.up.sql":   {Data: []byte(`ALTER TABLE users ADD COLUMN name TEXT;`)},
		"migrations/2_add_name.down.sql": {Data: []byte(`ALTER TABLE users DROP COLUMN name;`)},
		"migrations/10_seed.up.sql":      {Data: []byte(`INSERT INTO users (id, name) VALUES (1, 'ada');`)},
	}
	db := testkit.NewMigratedPostgresDB(t, mfs)
	row := db.QueryRow(context.Background(), `SELECT name FROM users WHERE id = 1`)
	var name string
	if err := row.Scan(&name); err != nil {
		t.Fatalf("scan: %v (migrations not applied in numeric order?)", err)
	}
	if name != "ada" {
		t.Fatalf("name = %q, want %q", name, "ada")
	}
	// down.sql must never run: the table still exists (proven above) and
	// a second insert still works.
	if _, err := db.Exec(context.Background(), `INSERT INTO users (id, name) VALUES (2, 'lin')`); err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}
}

// TestNewMigratedPostgresDB_AppliesTimestampVersionsAfterSequential is the
// mid-adoption project: a migrations dir holding both 5-digit sequential files
// and 14-digit UTC timestamp ones.
//
// The ordering must be NUMERIC across both spellings — 14 digits exceed 5, so
// every timestamp migration applies after every sequential one. A version
// parser that overflowed, or that fell back to lexicographic order, would run
// the timestamp file first and the INSERT would fail on a column that does not
// exist yet. That is exactly the failure this pins.
func TestNewMigratedPostgresDB_AppliesTimestampVersionsAfterSequential(t *testing.T) {
	requirePG(t)
	t.Parallel()
	mfs := fstest.MapFS{
		"migrations/00001_init.up.sql": {Data: []byte(`CREATE TABLE accounts (id BIGINT PRIMARY KEY);`)},
		// Timestamp-versioned, and it depends on the sequential one above.
		"migrations/20260101120000_add_email.up.sql": {Data: []byte(`ALTER TABLE accounts ADD COLUMN email TEXT;`)},
		"migrations/20260101120001_seed.up.sql":      {Data: []byte(`INSERT INTO accounts (id, email) VALUES (1, 'ada@example.com');`)},
	}
	db := testkit.NewMigratedPostgresDB(t, mfs)
	row := db.QueryRow(context.Background(), `SELECT email FROM accounts WHERE id = 1`)
	var email string
	if err := row.Scan(&email); err != nil {
		t.Fatalf("scan: %v (timestamp migrations not applied after sequential ones?)", err)
	}
	if email != "ada@example.com" {
		t.Fatalf("email = %q, want %q", email, "ada@example.com")
	}
}

func TestNewMigratedPostgresDB_RootLevelFS(t *testing.T) {
	requirePG(t)
	t.Parallel()
	// No migrations/ wrapper dir — files at the FS root still apply.
	mfs := fstest.MapFS{
		"0001_init.up.sql": {Data: []byte(`CREATE TABLE things (id BIGSERIAL PRIMARY KEY, v TEXT);`)},
	}
	db := testkit.NewMigratedPostgresDB(t, mfs)
	if _, err := db.Exec(context.Background(), `INSERT INTO things (v) VALUES ('x')`); err != nil {
		t.Fatalf("schema not applied from root-level FS: %v", err)
	}
}

func TestNewMigratedPostgresDB_EmptyFSYieldsEmptySchema(t *testing.T) {
	requirePG(t)
	t.Parallel()
	db := testkit.NewMigratedPostgresDB(t, fstest.MapFS{})
	// Behaves like NewPostgresDB: usable, no tables.
	if _, err := db.Exec(context.Background(), `CREATE TABLE t (id BIGSERIAL PRIMARY KEY)`); err != nil {
		t.Fatalf("exec on empty-migrations DB: %v", err)
	}
}

// localClaimsKey mimics the project-local unexported context key the
// generated pkg/middleware/claims.go uses — AuthedContext must install
// claims through the caller-supplied setter, never a key of its own.
type localClaimsKey struct{}

func localWithClaims(ctx context.Context, c *auth.Claims) context.Context {
	return context.WithValue(ctx, localClaimsKey{}, c)
}

func localClaimsFrom(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(localClaimsKey{}).(*auth.Claims)
	return c, ok
}

func TestAuthedContext_DefaultsLandViaProjectSetter(t *testing.T) {
	t.Parallel()
	ctx := testkit.AuthedContext(t, localWithClaims)
	claims, ok := localClaimsFrom(ctx)
	if !ok || claims == nil {
		t.Fatal("claims not retrievable through the project-shaped lookup")
	}
	if claims.UserID != "test-user" {
		t.Fatalf("UserID = %q, want %q", claims.UserID, "test-user")
	}
	if claims.Email != "test-user@example.test" {
		t.Fatalf("Email = %q", claims.Email)
	}
	if claims.Role != "admin" || len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("Role/Roles = %q/%v, want admin/[admin]", claims.Role, claims.Roles)
	}
}

func TestAuthedContext_OptionsOverrideDefaults(t *testing.T) {
	t.Parallel()
	ctx := testkit.AuthedContext(t, localWithClaims,
		testkit.WithUserID("u-42"),
		testkit.WithEmail("u42@corp.test"),
		testkit.WithOrgID("org-7"),
		testkit.WithRoles("viewer", "auditor"),
	)
	claims, _ := localClaimsFrom(ctx)
	if claims.UserID != "u-42" || claims.Email != "u42@corp.test" || claims.OrgID != "org-7" {
		t.Fatalf("identity fields = %+v", claims)
	}
	if claims.Role != "viewer" || len(claims.Roles) != 2 {
		t.Fatalf("Role/Roles = %q/%v, want viewer/[viewer auditor]", claims.Role, claims.Roles)
	}
}

func TestAuthedContext_WithClaimsReplacesWholesale(t *testing.T) {
	t.Parallel()
	ctx := testkit.AuthedContext(t, localWithClaims,
		testkit.WithClaims(auth.Claims{UserID: "only-me"}),
	)
	claims, _ := localClaimsFrom(ctx)
	if claims.UserID != "only-me" || claims.Email != "" || claims.Role != "" {
		t.Fatalf("WithClaims must replace the defaults wholesale, got %+v", claims)
	}
}

// The two declarations below mirror, exactly, the shapes the generated
// per-component test harness emits. They exist because both helpers below
// compiled fine against a hand-written approximation and then FAILED against a
// real scaffolded project — the approximation used an unnamed func type and a
// non-variadic Register, and neither difference is visible until a real
// generated file calls them.

// TestOption mirrors the generated `type TestOption func(*testConfig)`. The
// NAMED type is the point: Go does not assign []TestOption to
// []func(*testConfig), so ApplyOptions must accept it via a ~func constraint.
type harnessOption func(*harnessConfig)

type harnessConfig struct{ n int }

// harnessRegister mirrors a generated service's Register method, which takes
// variadic connect.HandlerOption alongside the mux.
func harnessRegister(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.HandleFunc("/probe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

type harnessClient struct{ baseURL string }

// newHarnessClient mirrors a generated `New<Svc>Client` constructor.
func newHarnessClient(_ connect.HTTPClient, baseURL string, _ ...connect.ClientOption) harnessClient {
	return harnessClient{baseURL: baseURL}
}

func TestApplyOptions_AcceptsANamedOptionType(t *testing.T) {
	t.Parallel()
	opts := []harnessOption{
		func(c *harnessConfig) { c.n++ },
		func(c *harnessConfig) { c.n += 10 },
	}
	got := testkit.ApplyOptions(&harnessConfig{}, opts...)
	if got.n != 11 {
		t.Fatalf("options applied in order should yield 11, got %d", got.n)
	}
}

// TestApplyOptions_EmptySliceReturnsTheDefaults covers the "caller passed no
// options" path the way the GENERATED code reaches it: forwarding a possibly
// empty `opts...` slice, which carries the option type even when it has no
// elements.
//
// Calling testkit.ApplyOptions(cfg) with no variadic argument at all does not
// compile — there is nothing to infer O from — which is why the helper's doc
// comment tells callers to forward their slice. Every generated factory does
// exactly that, so the uninferable spelling never arises in a scaffolded
// project.
func TestApplyOptions_EmptySliceReturnsTheDefaults(t *testing.T) {
	t.Parallel()
	var none []harnessOption
	if got := testkit.ApplyOptions(&harnessConfig{n: 7}, none...); got.n != 7 {
		t.Fatalf("with no options the config must come back untouched, got %d", got.n)
	}
}

// principalProcedure is a real Connect unary endpoint whose handler echoes the
// principal it SEES — the server-side view a handler calling
// middleware.GetUser gets, which is the only view an over-the-wire test can
// exercise.
const principalProcedure = "/testkit.test.v1.PrincipalService/WhoAmI"

func principalRegister(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(principalProcedure, connect.NewUnaryHandler(
		principalProcedure,
		func(ctx context.Context, _ *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			claims, ok := localClaimsFrom(ctx)
			if !ok || claims == nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no principal"))
			}
			return connect.NewResponse(&structpb.Struct{Fields: map[string]*structpb.Value{
				"user_id": structpb.NewStringValue(claims.UserID),
				"role":    structpb.NewStringValue(claims.Role),
			}}), nil
		},
		opts...,
	))
}

type principalClient struct {
	call *connect.Client[structpb.Struct, structpb.Struct]
}

func newPrincipalClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) principalClient {
	return principalClient{call: connect.NewClient[structpb.Struct, structpb.Struct](httpClient, baseURL+principalProcedure, opts...)}
}

// TestServerPrincipal_ReachesTheHandlerOverTheWire is the reason
// ServerPrincipal exists. AuthedContext installs claims on a CLIENT-side
// context, and a context does not cross HTTP — so a test that calls a
// NewTest<Svc>Server through its typed client can never reach an auth-gated
// handler that way. The principal has to be installed where the handler runs.
func TestServerPrincipal_ReachesTheHandlerOverTheWire(t *testing.T) {
	t.Parallel()

	_, bare := testkit.NewConnectClient(t, principalRegister, newPrincipalClient)
	// A client-side AuthedContext is exactly what the generated doc comment
	// used to recommend; pin that it does NOT authenticate the wire call.
	if _, err := bare.call.CallUnary(testkit.AuthedContext(t, localWithClaims), connect.NewRequest(&structpb.Struct{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a bare server must not see a client-side principal; got err=%v", err)
	}

	_, authed := testkit.NewConnectClient(t, principalRegister, newPrincipalClient,
		testkit.ServerPrincipal(localWithClaims, testkit.WithUserID("u-42"), testkit.WithRoles("viewer")))
	resp, err := authed.call.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if err != nil {
		t.Fatalf("handler behind ServerPrincipal saw no principal: %v", err)
	}
	if got := resp.Msg.GetFields()["user_id"].GetStringValue(); got != "u-42" {
		t.Fatalf("user_id = %q, want the ClaimsOption override %q", got, "u-42")
	}
	if got := resp.Msg.GetFields()["role"].GetStringValue(); got != "viewer" {
		t.Fatalf("role = %q, want %q", got, "viewer")
	}
}

// TestServerPrincipal_DefaultsMatchAuthedContext: one default principal for
// both halves of the harness, so an in-process row and an over-the-wire row
// of the same test agree on who is calling.
func TestServerPrincipal_DefaultsMatchAuthedContext(t *testing.T) {
	t.Parallel()
	_, client := testkit.NewConnectClient(t, principalRegister, newPrincipalClient,
		testkit.ServerPrincipal(localWithClaims))
	resp, err := client.call.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := localClaimsFrom(testkit.AuthedContext(t, localWithClaims))
	if got := resp.Msg.GetFields()["user_id"].GetStringValue(); got != want.UserID {
		t.Fatalf("user_id = %q, want AuthedContext's default %q", got, want.UserID)
	}
	if got := resp.Msg.GetFields()["role"].GetStringValue(); got != want.Role {
		t.Fatalf("role = %q, want AuthedContext's default %q", got, want.Role)
	}
}

// kindProcedure / kindStreamProcedure return a bare svcerr kind — the handler
// shape the api skill documents — from a unary and a server-streaming rpc.
const (
	kindProcedure       = "/testkit.test.v1.KindService/Fail"
	kindStreamProcedure = "/testkit.test.v1.KindService/FailStream"
)

func kindRegister(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(kindProcedure, connect.NewUnaryHandler(kindProcedure,
		func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return nil, svcerr.InvalidArgument("name is required")
		}, opts...))
	mux.Handle(kindStreamProcedure, connect.NewServerStreamHandler(kindStreamProcedure,
		func(context.Context, *connect.Request[structpb.Struct], *connect.ServerStream[structpb.Struct]) error {
			return svcerr.NotFound("process")
		}, opts...))
}

type kindClient struct {
	unary  *connect.Client[structpb.Struct, structpb.Struct]
	stream *connect.Client[structpb.Struct, structpb.Struct]
}

func newKindClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) kindClient {
	return kindClient{
		unary:  connect.NewClient[structpb.Struct, structpb.Struct](httpClient, baseURL+kindProcedure, opts...),
		stream: connect.NewClient[structpb.Struct, structpb.Struct](httpClient, baseURL+kindStreamProcedure, opts...),
	}
}

// TestNewConnectClient_ServesBehindSvcerrInterceptor: the test server ends its
// chain with svcerr.Interceptor, as production's chain builders do, so a bare
// svcerr kind reaches the client with its code — unary and streaming. Without
// it the client read unknown, and only under test.
func TestNewConnectClient_ServesBehindSvcerrInterceptor(t *testing.T) {
	t.Parallel()
	_, client := testkit.NewConnectClient(t, kindRegister, newKindClient, testkit.ServerPrincipal(localWithClaims))

	_, err := client.unary.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument || ce.Message() != "name is required" {
		t.Fatalf("unary: client read %v, want invalid_argument \"name is required\"", err)
	}

	stream, err := client.stream.CallServerStream(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if stream.Receive() {
		t.Fatal("stream yielded a message")
	}
	if got := connect.CodeOf(stream.Err()); got != connect.CodeNotFound {
		t.Fatalf("stream: client read %v (%v), want not_found", got, stream.Err())
	}
}

func TestNewConnectClient_ServesTheRegisteredMuxAndBuildsAClient(t *testing.T) {
	t.Parallel()
	srv, client := testkit.NewConnectClient(t, harnessRegister, newHarnessClient)

	// The client is built against the server that is actually serving.
	if client.baseURL != srv.URL {
		t.Fatalf("client baseURL = %q, want the test server's %q", client.baseURL, srv.URL)
	}

	// And register really was mounted — a client pointed at a server with no
	// handlers would satisfy the URL check above while being useless.
	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("GET the registered route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("registered handler did not serve; body = %q", body)
	}
}

// Two tests over the same migrations get independent databases, and the second
// does not replay the chain (it is cloned from the first's template).
func TestNewMigratedPostgresDB_ClonesAreIsolated(t *testing.T) {
	mfs := fstest.MapFS{
		"migrations/0001_t.up.sql": {Data: []byte(`CREATE TABLE clone_probe (v int); INSERT INTO clone_probe VALUES (1);`)},
	}
	a := testkit.NewMigratedPostgresDB(t, mfs)
	b := testkit.NewMigratedPostgresDB(t, mfs)
	for name, db := range map[string]orm.Context{"a": a, "b": b} {
		if _, err := db.(interface{ DB() *sql.DB }).DB().Exec(`INSERT INTO clone_probe VALUES (2)`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var n int
	if err := b.(interface{ DB() *sql.DB }).DB().QueryRow(`SELECT count(*) FROM clone_probe`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("clone b saw %d rows (err=%v), want its own 2", n, err)
	}
}

// An edited migration must not be served from the template of the old one.
func TestNewMigratedPostgresDB_ChangedMigrationsGetANewTemplate(t *testing.T) {
	v1 := fstest.MapFS{"migrations/0001_t.up.sql": {Data: []byte(`CREATE TABLE evolve (a int);`)}}
	v2 := fstest.MapFS{"migrations/0001_t.up.sql": {Data: []byte(`CREATE TABLE evolve (a int, b int);`)}}
	testkit.NewMigratedPostgresDB(t, v1)
	db := testkit.NewMigratedPostgresDB(t, v2).(interface{ DB() *sql.DB }).DB()
	if _, err := db.Exec(`INSERT INTO evolve (a, b) VALUES (1, 2)`); err != nil {
		t.Fatalf("the edited migration was not applied (stale template served): %v", err)
	}
}

// The database helpers take testing.TB, so a benchmark gets the same
// migrated, isolated database a test does — measuring a data path on
// anything else measures a different setup.
func BenchmarkNewMigratedPostgresDB(b *testing.B) {
	if testing.Short() {
		b.Skip("boots real postgres; skipped under -short")
	}
	mfs := fstest.MapFS{
		"migrations/1_init.up.sql": {Data: []byte(`CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT);`)},
	}
	db := testkit.NewMigratedPostgresDB(b, mfs)
	ctx := context.Background()
	var id int64
	for b.Loop() {
		id++
		if _, err := db.Exec(ctx, `INSERT INTO users (id, name) VALUES ($1, 'ada')`, id); err != nil {
			b.Fatalf("insert: %v", err)
		}
	}
	if empty := testkit.NewPostgresDB(b); empty == nil {
		b.Fatal("NewPostgresDB returned nil")
	}
}
