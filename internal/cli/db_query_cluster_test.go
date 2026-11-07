package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestSelectQueryDatabase(t *testing.T) {
	e := &KCLEntities{Databases: []DatabaseEntity{
		{Name: "a", Runtime: RuntimeCluster}, {Name: "b", Runtime: RuntimeCluster}, {Name: "h", Runtime: RuntimeHosted},
	}}
	if _, err := selectQueryDatabase(e, ""); exitCodeForError(err) != exitWrong || !strings.Contains(err.Error(), "--database") {
		t.Errorf("two databases, none named: %v", err)
	}
	if d, err := selectQueryDatabase(e, "b"); err != nil || d.Name != "b" {
		t.Errorf("named: %v %+v", err, d)
	}
	if _, err := selectQueryDatabase(e, "h"); exitCodeForError(err) != exitWrong {
		t.Errorf("a hosted database is not a cluster database: %v", err)
	}
	if _, err := selectQueryDatabase(&KCLEntities{}, ""); exitCodeForError(err) != exitUndetermined {
		t.Errorf("none: %v", err)
	}
}

// fakeDBPods stands in for the cluster's port-forward: it "forwards" every
// stream to a real Postgres and records which pod was asked for.
type fakeDBPods struct {
	mu    sync.Mutex
	calls []string
	addr  string
}

func (f *fakeDBPods) DialPod(_ context.Context, namespace, pod string, port int) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s/%s:%d", namespace, pod, port))
	f.mu.Unlock()
	return net.Dial("tcp", f.addr)
}

// End to end through the port-forward transport against a real Postgres: the
// read-only Secret and CA are read through the kube client, the replica
// service's ready pod is dialled, the query runs as that role in the
// ManagedDatabase's database, and the tunnel is closed.
func TestDBQueryClusterThroughTheTunnel(t *testing.T) {
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

	// A unique role per run: the server may be shared.
	role := fmt.Sprintf("fbl_ro_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS entries (id int PRIMARY KEY, amount int, secret text);
		INSERT INTO entries VALUES (1, 10, 's') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'; GRANT SELECT (id, amount) ON entries TO %s`, role, role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON entries FROM %s; DROP ROLE IF EXISTS %s`, role, role))
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	// The ManagedDatabase is named after the pgtest database, so the database
	// the transport derives from its name (deploy.DatabaseIdentifiers) is that
	// one: a pgtest name has no hyphen for it to fold.
	dbName := strings.TrimPrefix(u.Path, "/")

	ready := true
	cs := fake.NewClientset([]runtime.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: dbgrants.SecretName(dbName)},
			Data: map[string][]byte{"username": []byte(role), "password": []byte("pw")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: kubedial.CASecretName(dbName)},
			Data: map[string][]byte{"ca.crt": testCAPEM(t)}},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: dbName + "-ro-x", Labels: map[string]string{discoveryv1.LabelServiceName: dbName + "-ro"}},
			Ports:      []discoveryv1.EndpointPort{{Port: new(int32(5432))}},
			Endpoints: []discoveryv1.Endpoint{{
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: dbName + "-2"},
			}},
		},
	}...)
	pods := &fakeDBPods{addr: u.Host}
	prevClient, prevTweak := newDBQueryClient, dbClusterConnTweak
	newDBQueryClient = func(kubeContext string) (*kubedial.Client, error) {
		if kubeContext != "k3d-demo" {
			t.Errorf("kube context %q, want the database's cluster", kubeContext)
		}
		return kubedial.NewWithClientset(cs, pods), nil
	}
	// The test Postgres has no TLS; the verification itself is covered in
	// pkg/dbquery/kubedial.
	dbClusterConnTweak = func(c *pgx.ConnConfig) { c.TLSConfig = nil }
	t.Cleanup(func() { newDBQueryClient, dbClusterConnTweak = prevClient, prevTweak })

	withKCLFixture(t, fmt.Sprintf(`{"output":{"lifecycle":"local","data_access":{"db_query":"open"},
		"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":%q,"runtime":"cluster","cluster":"k3d-demo","namespace":"demo","spec":{"instances":2}}]}}`, dbName))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := runDBQueryCmd(t, "dev", "select id, amount from entries order by id", "--json")
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
	if !doc.OK || len(doc.Rows) != 1 || doc.Scope.Transport != "port-forward" || doc.Scope.Database != dbName {
		t.Errorf("doc %+v", doc)
	}
	pods.mu.Lock()
	calls := append([]string(nil), pods.calls...)
	pods.mu.Unlock()
	if len(calls) == 0 || calls[0] != "demo/"+dbName+"-2:5432" {
		t.Errorf("forwarded to %v, want the ready pod behind the replica service", calls)
	}

	// The role's grants are the boundary: a column it was not granted is
	// refused by Postgres, exit 1.
	if _, err := runDBQueryCmd(t, "dev", "select secret from entries"); exitCodeForError(err) != exitWrong {
		t.Errorf("an ungranted column: err = %v, want exit 1", err)
	}
}

// A database whose read-only Secret is missing is not queried: the command
// says why and how to fix it, exit 2.
func TestDBQueryClusterWithoutTheReadOnlyRole(t *testing.T) {
	prevClient := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) {
		return kubedial.NewWithClientset(fake.NewClientset(), &fakeDBPods{}), nil
	}
	t.Cleanup(func() { newDBQueryClient = prevClient })
	withKCLFixture(t, `{"output":{"lifecycle":"local","data_access":{"db_query":"open"},
		"workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}],
		"databases":[{"name":"ledger","runtime":"cluster","cluster":"k3d-demo","namespace":"demo","spec":{"instances":1}}]}}`)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, err := runDBQueryCmd(t, "dev", "select 1")
	if exitCodeForError(err) != exitUndetermined || err == nil || !strings.Contains(err.Error(), "ledger-forge-ro") || !strings.Contains(err.Error(), "redeploy") {
		t.Errorf("err = %v, want exit 2 naming the missing secret and the fix", err)
	}
}

// testCAPEM is a throwaway CA certificate: the transport only parses it, as
// the test Postgres speaks no TLS.
func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
