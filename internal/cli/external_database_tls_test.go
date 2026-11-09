package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestExternalDatabaseTLSCLIProjection(t *testing.T) {
	ca := testCAPEM(t)
	cs := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "reader"}, Data: map[string][]byte{"pw": []byte("private-password")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "trust"}, Data: map[string]string{"bundle.pem": string(ca)}},
	)
	prev := newDBQueryClient
	newDBQueryClient = func(string) (*kubedial.Client, error) { return kubedial.NewWithClientset(cs, nil), nil }
	t.Cleanup(func() { newDBQueryClient = prev })
	e, err := parseKCLEntities([]byte(`{"output":{"external_databases":[{"name":"ledger","cluster":"k3d-demo","namespace":"prod","service":"pooler","database":"ledger","username":"reader","password_secret":"reader","password_key":"pw","sslmode":"verify-full","ca_configmap":"trust","ca_key":"bundle.pem","tls_server_name":"db.example.test"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	x := e.ExternalDatabases[0]
	target, err := resolveDBQueryExternal(context.Background(), dbQueryOptions{env: "prod", confirmEnv: "prod", reason: "TLS test"}, e, x, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := target.connConfig
	if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "db.example.test" || cfg.Password != "private-password" {
		t.Fatal("CLI lost TLS or credential selector")
	}
	route := shapeExternalDatabasesOf(e)[0]
	if route.CAConfigMap != "trust" || route.CAKey != "bundle.pem" || route.TLSServerName != "db.example.test" {
		t.Fatal("recorded shape lost trust selectors")
	}
}

func renderExternalDatabaseTLSMain(t *testing.T, main string) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	for f, c := range map[string]string{"kcl.mod": "[package]\nname = \"extdb\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n", "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	return kclrender.Run(dir, dir, nil)
}

func TestExternalDatabaseTLSKCLValidation(t *testing.T) {
	testkit.Slow(t, "renders KCL schema through native runtime")
	for _, tc := range []struct {
		name, replace string
		valid         bool
	}{
		{"valid ConfigMap", "", true},
		{"valid Secret derived name", "", true},
		{"no namespace", "", false},
		{"no key", "            ca_key = \"ca.crt\"\n", false},
		{"both sources", "            ca_configmap = \"pooler-ca\"\n", false},
		{"bad DNS", "            tls_server_name = \"pgbouncer.ledger.svc\"\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main := externalDatabasesMain
			switch tc.name {
			case "valid Secret derived name":
				main = strings.Replace(main, `ca_configmap = "pooler-ca"`, `ca_secret = "pooler-ca"`, 1)
				main = strings.Replace(main, "            tls_server_name = \"pgbouncer.ledger.svc\"\n", "", 1)
			case "no namespace":
				main = strings.Replace(main, `namespace = "demo-prod"`, `namespace = ""`, 1)
				main = strings.Replace(main, "            namespace = \"ledger\"\n", "", 1)
			case "no key":
				main = strings.Replace(main, tc.replace, "", 1)
			case "both sources":
				main = strings.Replace(main, tc.replace, tc.replace+"            ca_secret = \"other-ca\"\n", 1)
			case "bad DNS":
				main = strings.Replace(main, tc.replace, "            tls_server_name = \"*.ledger.svc\"\n", 1)
			}
			out, err := renderExternalDatabaseTLSMain(t, main)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			e, err := parseKCLEntities(out)
			if err != nil {
				t.Fatal(err)
			}
			routes := shapeExternalDatabasesOf(e)
			raw, err := json.Marshal(release.Shape{Kind: release.EnvSelfManaged, ExternalDatabases: routes}.Canonical())
			if err != nil {
				t.Fatal(err)
			}
			source := `"ca_configmap":"pooler-ca"`
			if tc.name == "valid Secret derived name" {
				source = `"ca_secret":"pooler-ca"`
			}
			for _, field := range []string{source, `"ca_key":"ca.crt"`, `"tls_server_name":"pgbouncer.ledger.svc"`} {
				if !strings.Contains(string(raw), field) {
					t.Fatalf("recorded route missing %s", field)
				}
			}
		})
	}
}
