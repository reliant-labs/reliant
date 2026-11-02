package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestSelectExternalDatabase(t *testing.T) {
	ledger := ExternalDatabaseEntity{Name: "ledger"}
	only := &KCLEntities{ExternalDatabases: []ExternalDatabaseEntity{ledger}}
	if x, ok, err := selectExternalDatabase(only, ""); err != nil || !ok || x.Name != "ledger" {
		t.Errorf("the env's only database: %v %v %+v", err, ok, x)
	}
	if _, ok, err := selectExternalDatabase(only, "other"); err != nil || ok {
		t.Errorf("--database naming no external database falls through: %v %v", err, ok)
	}
	both := &KCLEntities{
		Databases:         []DatabaseEntity{{Name: "app", Runtime: RuntimeCluster}},
		ExternalDatabases: []ExternalDatabaseEntity{ledger},
	}
	if _, _, err := selectExternalDatabase(both, ""); exitCodeForError(err) != exitWrong || !strings.Contains(err.Error(), "ledger (external)") {
		t.Errorf("two databases, none named: %v", err)
	}
	if x, ok, err := selectExternalDatabase(both, "ledger"); err != nil || !ok || x.Name != "ledger" {
		t.Errorf("named: %v %v %+v", err, ok, x)
	}
	if _, ok, err := selectExternalDatabase(both, "app"); err != nil || ok {
		t.Errorf("a managed database is not external: %v %v", err, ok)
	}
	if _, ok, err := selectExternalDatabase(&KCLEntities{}, ""); err != nil || ok {
		t.Errorf("none: %v %v", err, ok)
	}
}

// externalFixture is a REAL env (no lifecycle, so db_query is audited) whose
// only workload runs on a cluster and whose database, ledger, is external:
// reached through the pgbouncer Service, as ledger_ro.
const externalFixture = `{"output":{
	"workloads":[{"name":"ledger-api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
	"external_databases":[{"name":"ledger","cluster":"gke-prod","namespace":"prod","service":"pgbouncer",
		"database":%q,"username":%q,"password_secret":"ledger-ro","password_key":"password","sslmode":"require"}]}}`

// An audited env refuses a query that does not name the env and a reason, and
// refuses it before the password Secret is read.
func TestDBQueryExternalJudgesThePolicyBeforeReadingTheSecret(t *testing.T) {
	cs := fake.NewClientset()
	prevClient := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) { return kubedial.NewWithClientset(cs, &fakeDBPods{}), nil }
	t.Cleanup(func() { newDBQueryClient = prevClient })
	withKCLFixture(t, fmt.Sprintf(externalFixture, "ledger", "ledger_ro"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := runDBQueryCmd(t, "prod", "select 1", "--database", "ledger")
	if exitCodeForError(err) != exitWrong || err == nil || !strings.Contains(err.Error(), "--confirm-env prod") {
		t.Errorf("err = %v, want exit 1 asking for --confirm-env and --reason", err)
	}
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Errorf("read %s before the policy allowed the query", a.GetResource().Resource)
		}
	}
}

// A missing password Secret says which one, exit 2.
func TestDBQueryExternalWithoutItsSecret(t *testing.T) {
	prevClient := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) {
		return kubedial.NewWithClientset(fake.NewClientset(), &fakeDBPods{}), nil
	}
	t.Cleanup(func() { newDBQueryClient = prevClient })
	withKCLFixture(t, fmt.Sprintf(externalFixture, "ledger", "ledger_ro"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, err := runDBQueryCmd(t, "prod", "select 1", "--confirm-env", "prod", "--reason", "ISS-1")
	if exitCodeForError(err) != exitUndetermined || err == nil || !strings.Contains(err.Error(), "prod/ledger-ro") {
		t.Errorf("err = %v, want exit 2 naming the missing secret", err)
	}
}

// End to end against a real Postgres: on a real env, the env's only database
// is the external one, the password comes from its Secret through the kube
// client, the tunnel lands on the ready pod behind its Service at the
// Service's only port, and the statement runs as the declared role in a READ
// ONLY transaction, which refuses a write even from a role that may write.
func TestDBQueryExternalThroughTheTunnel(t *testing.T) {
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
	// A role that MAY write: the read-only transaction is what stops it.
	role := fmt.Sprintf("ext_rw_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS ledger_entries (id int PRIMARY KEY, amount int);
		INSERT INTO ledger_entries VALUES (1, 10) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'; GRANT SELECT, INSERT ON ledger_entries TO %s`, role, role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON ledger_entries FROM %s; DROP ROLE IF EXISTS %s`, role, role))
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")

	ready := true
	cs := fake.NewClientset([]runtime.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "ledger-ro"}, Data: map[string][]byte{"password": []byte("pw")}},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "pgbouncer-x", Labels: map[string]string{discoveryv1.LabelServiceName: "pgbouncer"}},
			Ports:      []discoveryv1.EndpointPort{{Port: new(int32(6432))}},
			Endpoints: []discoveryv1.Endpoint{{
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "pgbouncer-0"},
			}},
		},
	}...)
	pods := &fakeDBPods{addr: u.Host}
	prevClient, prevTweak := newDBQueryClient, dbClusterConnTweak
	newDBQueryClient = func(kubeContext string) (*kubedial.Client, error) {
		if kubeContext != "gke-prod" {
			t.Errorf("kube context %q, want the external database's cluster", kubeContext)
		}
		return kubedial.NewWithClientset(cs, pods), nil
	}
	dbClusterConnTweak = func(c *pgx.ConnConfig) { c.TLSConfig = nil } // the test Postgres speaks no TLS
	t.Cleanup(func() { newDBQueryClient, dbClusterConnTweak = prevClient, prevTweak })
	withKCLFixture(t, fmt.Sprintf(externalFixture, dbName, role))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := runDBQueryCmd(t, "prod", "select id, amount, current_user from ledger_entries order by id",
		"--json", "--confirm-env", "prod", "--reason", "ISS-1")
	if err != nil {
		t.Fatalf("query: %v\n%s", err, out)
	}
	var doc struct {
		OK    bool         `json:"ok"`
		Scope dbQueryScope `json:"scope"`
		Rows  [][]any      `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !doc.OK || len(doc.Rows) != 1 || doc.Scope.Transport != "port-forward" || doc.Scope.Database != "ledger" || doc.Scope.Policy != "audited" {
		t.Errorf("doc %+v", doc)
	}
	if len(doc.Rows) == 1 && fmt.Sprint(doc.Rows[0][2]) != role {
		t.Errorf("ran as %v, want the declared role %s", doc.Rows[0][2], role)
	}
	pods.mu.Lock()
	calls := append([]string(nil), pods.calls...)
	pods.mu.Unlock()
	if len(calls) == 0 || calls[0] != "prod/pgbouncer-0:6432" {
		t.Errorf("forwarded to %v, want the ready pod behind the service, at its port", calls)
	}

	// The role may insert; the READ ONLY transaction refuses it all the same.
	if _, err := runDBQueryCmd(t, "prod", "with x as (insert into ledger_entries values (2, 20) returning id) select id from x",
		"--confirm-env", "prod", "--reason", "ISS-1"); exitCodeForError(err) != exitWrong {
		t.Errorf("a write: err = %v, want exit 1", err)
	}
}
