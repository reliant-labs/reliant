package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// externalDatabasesMain is an env with one managed database and two external
// ones: ledger on the env's primary cluster in its own namespace, dialed
// directly at a pooler port, and billing bound to its own cluster with every
// default (a port-forward).
const externalDatabasesMain = `import forge

_billing = forge.ClusterTarget {cluster = "gke-billing", namespace = "billing-prod"}

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "gke-prod", namespace = "demo-prod"}
    databases = [forge.ManagedDatabase {name = "app"}]
    external_databases = [
        forge.ExternalDatabase {
            name = "ledger"
            service = "pgbouncer"
            database = "ledger_prod"
            username = "ledger_ro"
            password_secret = "ledger-ro"
            password_key = "pw"
            sslmode = "disable"
            namespace = "ledger"
            reach = "direct"
            port = 6432
        }
        forge.ExternalDatabase {
            name = "billing"
            runtime = forge.OnCluster {target = _billing}
            service = "billing-pooler"
            database = "billing"
            username = "billing_ro"
            password_secret = "billing-ro"
        }
    ]
})
`

func renderExternalDatabases(t *testing.T) *KCLEntities {
	t.Helper()
	testkit.Slow(t, "renders KCL")
	dir := t.TempDir()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"extdb\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  externalDatabasesMain,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// The password of an external database lives in its Secret; the stream below
// carries that Secret with a value, the way a project that renders its own
// Secret would.
const externalDatabasesSecretValue = "s3cr3t-ledger-password"

const externalDatabasesStream = `# cluster: gke-prod
apiVersion: v1
kind: Secret
metadata:
  name: ledger-ro
  namespace: ledger
stringData:
  pw: ` + externalDatabasesSecretValue + `
---
# cluster: gke-prod
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: demo-prod
spec:
  replicas: 1
`

// Two forge.ExternalDatabases reach the recorded shape as routes, each where
// the render resolved it, and the shape names the password Secret without its
// value.
func TestExternalDatabasesReachTheShape(t *testing.T) {
	e := renderExternalDatabases(t)
	got := shapeExternalDatabasesOf(e)
	want := []release.ShapeExternalDatabase{{
		Name: "ledger", Cluster: "gke-prod", Namespace: "ledger", Service: "pgbouncer", Database: "ledger_prod",
		Username: "ledger_ro", PasswordSecret: "ledger-ro", PasswordKey: "pw", SSLMode: release.ExternalDatabaseSSLDisable,
		Reach: release.ExternalDatabaseReachDirect, Port: 6432,
	}, {
		Name: "billing", Cluster: "gke-billing", Namespace: "billing-prod", Service: "billing-pooler", Database: "billing",
		Username: "billing_ro", PasswordSecret: "billing-ro", PasswordKey: "password", SSLMode: release.ExternalDatabaseSSLRequire,
		Reach: release.ExternalDatabaseReachPortForward,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shapeExternalDatabasesOf =\n%+v\nwant\n%+v", got, want)
	}

	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:              release.EnvSelfManaged,
		Workloads:         shapeWorkloadsOf(e),
		Clusters:          []string{"gke-prod"},
		Manifests:         externalDatabasesStream,
		ExternalDatabases: got,
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	if names := []string{shape.ExternalDatabases[0].Name, shape.ExternalDatabases[1].Name}; names[0] != "billing" || names[1] != "ledger" {
		t.Errorf("the shape's external databases are not sorted by name: %v", names)
	}
	if shape.DatabaseKindOf("ledger") != release.DatabaseExternal || shape.DatabaseKindOf("billing") != release.DatabaseExternal {
		t.Errorf("the shape does not read its external databases as external")
	}
	if !shape.HasOptionalParts() {
		t.Error("a shape with external databases claims to need no optional feature")
	}
	raw, err := shape.Encode()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	if strings.Contains(doc, externalDatabasesSecretValue) {
		t.Errorf("the shape carries the password Secret's value: %s", doc)
	}
	if x, _ := shape.ExternalDatabase("ledger"); x.DirectAddress() != "pgbouncer.ledger.svc:6432" {
		t.Errorf("ledger's direct address = %q", x.DirectAddress())
	}
	for _, name := range []string{`"external_databases"`, `"password_secret":"ledger-ro"`, `"password_secret":"billing-ro"`, `"reach":"direct","port":6432`, `"reach":"port_forward"`} {
		if !strings.Contains(doc, name) {
			t.Errorf("the shape has no %s: %s", name, doc)
		}
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["external_databases"]; !ok {
		t.Error("the encoded shape has no external_databases key")
	}
}

// An env that declares no external database records none: no key, and no
// feature asked of the control plane, so one that predates the field keeps
// accepting the project's bundles.
func TestNoExternalDatabasesRecordNothing(t *testing.T) {
	e := renderDashboards(t, "")
	if got := shapeExternalDatabasesOf(e); got != nil {
		t.Fatalf("an env with no external databases projects %+v", got)
	}
	if shapeExternalDatabasesOf(nil) != nil || shapeExternalDatabasesOf(&KCLEntities{}) != nil {
		t.Error("no entities must project nothing")
	}
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:              release.EnvSelfManaged,
		Workloads:         shapeWorkloadsOf(e),
		Manifests:         externalDatabasesStream,
		ExternalDatabases: shapeExternalDatabasesOf(e),
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	raw, err := shape.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "external_databases") || shape.HasOptionalParts() {
		t.Errorf("an env with no external databases records the field or needs a feature: %s", raw)
	}
}
