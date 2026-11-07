package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestValidateDBQueryAddr(t *testing.T) {
	for name, tc := range map[string]struct {
		o       dbQueryOptions
		wantErr string
	}{
		"no addr":                  {dbQueryOptions{}, ""},
		"host:port":                {dbQueryOptions{addr: "localhost:5432"}, ""},
		"ipv6":                     {dbQueryOptions{addr: "[::1]:5432"}, ""},
		"verify-full":              {dbQueryOptions{addr: "db:5432", sslmode: "verify-full"}, ""},
		"require":                  {dbQueryOptions{addr: "db:5432", sslmode: "require"}, ""},
		"disable":                  {dbQueryOptions{addr: "db:5432", sslmode: "disable"}, ""},
		"no port":                  {dbQueryOptions{addr: "localhost"}, "host:port"},
		"no host":                  {dbQueryOptions{addr: ":5432"}, "host:port"},
		"port zero":                {dbQueryOptions{addr: "db:0"}, "host:port"},
		"port too big":             {dbQueryOptions{addr: "db:70000"}, "host:port"},
		"port not a number":        {dbQueryOptions{addr: "db:pg"}, "host:port"},
		"a URL":                    {dbQueryOptions{addr: "postgres://u:pw@db:5432/x"}, "host:port"},
		"unknown sslmode":          {dbQueryOptions{addr: "db:5432", sslmode: "prefer"}, "--sslmode"},
		"sslmode without addr":     {dbQueryOptions{sslmode: "require"}, "--addr only"},
		"password in addr refused": {dbQueryOptions{addr: "u:pw@db:5432"}, "host:port"},
	} {
		err := validateDBQueryAddr(tc.o)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr) || exitCodeForError(err) != exitWrong):
			t.Errorf("%s: err = %v, want exit 1 containing %q", name, err, tc.wantErr)
		}
	}
	// The address's password must never be echoed.
	if err := validateDBQueryAddr(dbQueryOptions{addr: "u:hunter2@db:5432"}); err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("echoed a secret: %v", err)
	}
}

func TestDBQueryAddrFlagsAreParsed(t *testing.T) {
	cmd := newDBQueryCommand()
	if err := cmd.ParseFlags([]string{"--addr", "localhost:5432", "--sslmode", "require"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cmd.Flags().GetString("addr"); got != "localhost:5432" {
		t.Errorf("addr = %q", got)
	}
	if got, _ := cmd.Flags().GetString("sslmode"); got != "require" {
		t.Errorf("sslmode = %q", got)
	}
	for _, name := range []string{"password", "password-env", "user"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("--%s exists: a credential source already exists, and a password never goes on the command line", name)
		}
	}
}

func TestDBQueryAddrRefusesAHostedEnv(t *testing.T) {
	withKCLFixture(t, hostedDBQueryFixture("open"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fake := &fakeQueryDatabase{reply: queryDatabaseReply}
	withHostedDB(t, fake)

	out, err := runDBQueryCmd(t, "prod", "select 1", "--addr", "localhost:5432", "--json")
	if exitCodeForError(err) != exitWrong || err == nil || !strings.Contains(err.Error(), "control plane executes") {
		t.Fatalf("err = %v, want exit 1 explaining the control plane executes hosted queries", err)
	}
	if len(fake.bodies) != 0 {
		t.Error("a hosted query was sent although --addr was given")
	}
	var doc dbQueryDocument
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("not one JSON document: %v\n%s", jerr, out)
	}
	if doc.OK || doc.ExitCode != exitWrong || doc.Schema != dbQuerySchema {
		t.Errorf("envelope %+v", doc.jsonEnvelope)
	}
}

// A real env's cluster database is refused without --addr and reachable with it.
func TestDBQueryAddrOpensARealClusterEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	const fixture = `{"output":{"data_access":{"db_query":"open"},
		"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":"ledger","runtime":"cluster","cluster":"k3d-demo","namespace":"demo","spec":{"instances":1}}]}}`
	withKCLFixture(t, fixture)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := runDBQueryCmd(t, "prod", "select 1")
	if exitCodeForError(err) != exitUndetermined || err == nil || !strings.Contains(err.Error(), "--addr") {
		t.Errorf("without --addr: err = %v, want exit 2 pointing at --addr", err)
	}

	asked := false
	prev := newDBQueryClient
	newDBQueryClient = func(kubeContext string) (*kubedial.Client, error) {
		asked = true
		if kubeContext != "k3d-demo" {
			t.Errorf("kube context %q", kubeContext)
		}
		return kubedial.NewWithClientset(fake.NewClientset(), nil), nil
	}
	t.Cleanup(func() { newDBQueryClient = prev })
	// The real env is judged: no credentials Secret in the fake cluster, so
	// the failure is the credential lookup (exit 2), not the lifecycle gate.
	_, err = runDBQueryCmd(t, "prod", "select 1", "--addr", "localhost:5432")
	if !asked || exitCodeForError(err) != exitUndetermined || err == nil || !strings.Contains(err.Error(), "ledger-forge-ro") {
		t.Errorf("with --addr: asked=%v err = %v, want the credential lookup to run and name the missing Secret", asked, err)
	}
}

func TestDBQueryAddrHonoursThePolicyOnARealEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	const fixture = `{"output":{"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":"ledger","runtime":"cluster","cluster":"k3d-demo","namespace":"demo","spec":{"instances":1}}]}}`
	withKCLFixture(t, fixture)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cs := fake.NewClientset(addrSecrets("ledger", "forge_ro", "pw", testCAPEM(t))...)
	prev := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) { return kubedial.NewWithClientset(cs, nil), nil }
	t.Cleanup(func() { newDBQueryClient = prev })

	// An unset policy on a real env is audited: --addr does not bypass it.
	_, err := runDBQueryCmd(t, "prod", "select 1", "--addr", "localhost:1")
	if exitCodeForError(err) != exitWrong || err == nil || !strings.Contains(err.Error(), "--confirm-env") {
		t.Errorf("err = %v, want exit 1 asking for --confirm-env and --reason", err)
	}
	// Confirmed, it connects (and nothing listens on port 1).
	_, err = runDBQueryCmd(t, "prod", "select 1", "--addr", "127.0.0.1:1", "--confirm-env", "prod", "--reason", "ISS-1")
	if exitCodeForError(err) != exitUndetermined || err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("err = %v, want exit 2 naming the address", err)
	}
	if err != nil && strings.Contains(err.Error(), "pw") && strings.Contains(err.Error(), "forge_ro:pw") {
		t.Errorf("the connection error echoed the password: %v", err)
	}
}

func addrSecrets(db, user, password string, ca []byte) []runtime.Object {
	return []runtime.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: dbgrants.SecretName(db)},
			Data: map[string][]byte{"username": []byte(user), "password": []byte(password)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: kubedial.CASecretName(db)},
			Data: map[string][]byte{"ca.crt": ca}},
	}
}

func TestDBQueryAddrServerNameDefaultsToTheEnvServiceHost(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	const fixture = `{"output":{"lifecycle":"local","data_access":{"db_query":"open"},
		"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":"ledger","runtime":"cluster","cluster":"k3d-demo","namespace":"demo","spec":{"instances":3}}]}}`
	withKCLFixture(t, fixture)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cs := fake.NewClientset(addrSecrets("ledger", "forge_ro", "pw", testCAPEM(t))...)
	prev := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) { return kubedial.NewWithClientset(cs, nil), nil }
	t.Cleanup(func() { newDBQueryClient = prev })

	resolve := func(args ...string) *pgx.ConnConfig {
		t.Helper()
		o := dbQueryOptions{env: "dev", workload: "api", addr: "localhost:6543"}
		for i := 0; i+1 < len(args); i += 2 {
			if args[i] == "--sslmode" {
				o.sslmode = args[i+1]
			}
		}
		target, err := resolveDBQueryTarget(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if target.scope.Transport != "addr" {
			t.Errorf("transport = %q", target.scope.Transport)
		}
		return target.connConfig
	}

	cfg := resolve()
	// <db>-rw even though the cluster runs standbys (-ro): a replica
	// certificate carries both, and the rw name is the one that always exists.
	if cfg.Host != "localhost" || cfg.Port != 6543 || cfg.User != "forge_ro" || cfg.Database != "ledger" {
		t.Errorf("config host=%s port=%d user=%s db=%s", cfg.Host, cfg.Port, cfg.User, cfg.Database)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.ServerName != "ledger-rw.demo.svc" || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.RootCAs == nil {
		t.Errorf("TLS %+v, want verify-full against the cluster CA as ledger-rw.demo.svc", cfg.TLSConfig)
	}
	if cfg.DialFunc != nil && cfg.TLSConfig != nil {
		// pgx's default dialer: forge adds no transport of its own.
		t.Log("custom dial func set")
	}
	if cfg := resolve("--sslmode", "require"); cfg.TLSConfig == nil || !cfg.TLSConfig.InsecureSkipVerify {
		t.Errorf("require: TLS %+v", cfg.TLSConfig)
	}
	if cfg := resolve("--sslmode", "disable"); cfg.TLSConfig != nil {
		t.Errorf("disable: TLS %+v", cfg.TLSConfig)
	}
}

// --addr wins over every transport forge has: the compose/host DATABASE_URL
// keeps its credentials and database, and only the dial target moves.
func TestDBQueryAddrOverridesTheDirectTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	withKCLFixture(t, dbQueryFixture(RuntimeHost, "postgres://app:s3cret@db.internal:5432/shop?sslmode=verify-full"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	target, err := resolveDBQueryTarget(context.Background(), dbQueryOptions{env: "dev", workload: "api", addr: "127.0.0.1:7000"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := target.connConfig
	if cfg == nil || cfg.Host != "127.0.0.1" || cfg.Port != 7000 || cfg.User != "app" || cfg.Password != "s3cret" || cfg.Database != "shop" {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.ServerName != "db.internal" {
		t.Errorf("verify-full must keep verifying the URL's own host name through the route, TLS %+v", cfg.TLSConfig)
	}
	if target.scope.Transport != "addr" {
		t.Errorf("transport = %q", target.scope.Transport)
	}

	// Without --addr nothing changes.
	target, err = resolveDBQueryTarget(context.Background(), dbQueryOptions{env: "dev", workload: "api"})
	if err != nil || target.connConfig != nil || target.scope.Transport != "direct" {
		t.Errorf("without --addr: %v %+v", err, target)
	}

	// --sslmode is for cluster databases: a URL carries its own.
	_, err = resolveDBQueryTarget(context.Background(), dbQueryOptions{env: "dev", workload: "api", addr: "127.0.0.1:7000", sslmode: "require"})
	if exitCodeForError(err) != exitWrong {
		t.Errorf("--sslmode on a direct workload: %v", err)
	}
}

func TestDsnAtAddrDoesNotLeakThePassword(t *testing.T) {
	_, err := dsnAtAddr("postgres://u:hunter2@%zz/db", "localhost:5432")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
	cfg, err := dsnAtAddr("postgres://u:pw@a:1,b:2/db?sslmode=prefer", "localhost:5432")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cfg.Fallbacks {
		if f.Host != "localhost" || f.Port != 5432 {
			t.Errorf("fallback %s:%d still names the URL's host", f.Host, f.Port)
		}
	}
}

// End to end against a real Postgres reached by --addr: a cluster database's
// read-only Secret is read, the address is dialled directly (the kube client has
// no pod dialer, so any use of the tunnel fails), the same guard, policy and JSON
// document apply, and the grants are still the boundary.
func TestDBQueryAddrAgainstPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	admin, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	role := fmt.Sprintf("fba_ro_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE entries (id int PRIMARY KEY, amount int, secret text);
		INSERT INTO entries VALUES (1, 10, 's'), (2, 20, 't');
		CREATE ROLE %s LOGIN PASSWORD 'pw'; GRANT SELECT (id, amount) ON entries TO %s`, role, role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON entries FROM %s; DROP ROLE IF EXISTS %s`, role, role))
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		t.Fatalf("pgtest URL host %q: %v", u.Host, err)
	}

	cs := fake.NewClientset(addrSecrets(dbName, role, "pw", testCAPEM(t))...)
	prevClient, prevTweak := newDBQueryClient, dbClusterConnTweak
	newDBQueryClient = func(string) (*kubedial.Client, error) { return kubedial.NewWithClientset(cs, nil), nil }
	t.Cleanup(func() { newDBQueryClient, dbClusterConnTweak = prevClient, prevTweak })
	withKCLFixture(t, fmt.Sprintf(`{"output":{"data_access":{"db_query":"audited"},
		"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":%q,"runtime":"cluster","cluster":"prod-cluster","namespace":"demo","spec":{"instances":1}}]}}`, dbName))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	common := []string{"--addr", u.Host, "--sslmode", "disable", "--confirm-env", "prod", "--reason", "ISS-9"}
	out, err := runDBQueryCmd(t, append([]string{"prod", "select id, amount from entries order by id", "--json"}, common...)...)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, out)
	}
	var doc struct {
		OK       bool            `json:"ok"`
		Scope    dbQueryScope    `json:"scope"`
		Rows     [][]json.Number `json:"rows"`
		RowCount int             `json:"row_count"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := dbQueryScope{Env: "prod", Transport: "addr", Workload: "api", Database: dbName, Policy: "audited"}
	if !doc.OK || doc.RowCount != 2 || doc.Scope != want {
		t.Errorf("doc %+v, want scope %+v", doc, want)
	}

	if _, err := runDBQueryCmd(t, append([]string{"prod", "select secret from entries"}, common...)...); exitCodeForError(err) != exitWrong {
		t.Errorf("an ungranted column: err = %v, want exit 1", err)
	}
	if _, err := runDBQueryCmd(t, append([]string{"prod", "delete from entries"}, common...)...); exitCodeForError(err) != exitWrong {
		t.Errorf("a write: err = %v, want exit 1", err)
	}
	out, err = runDBQueryCmd(t, append([]string{"prod", "select id from entries order by id", "--max-rows", "1", "--json"}, common...)...)
	if err != nil || !strings.Contains(out, `"truncated": true`) {
		t.Errorf("row cap: %v\n%s", err, out)
	}
	// The audited policy still holds through --addr.
	if _, err := runDBQueryCmd(t, "prod", "select 1", "--addr", u.Host, "--sslmode", "disable"); exitCodeForError(err) != exitWrong {
		t.Errorf("an unconfirmed audited query: err = %v, want exit 1", err)
	}
}
