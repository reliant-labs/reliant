package templates

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// TestExternalSecretLoginMatchesSecretaccess pins the two halves of the store
// login together. kcl/lib/external_secret.k renders the SecretStore a
// namespace logs in with (JWT role, mount, subject, KV path); pkg/secretaccess
// derives the role, mount and policy the store holds for it. They are written
// in two languages by two programs, and a role the store does not have is a
// namespace that silently syncs nothing, so the KCL derivation is rendered here
// and compared with the Go one for a spread of identifiers (UUIDs, short ids,
// long ids that hash).
func TestExternalSecretLoginMatchesSecretaccess(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)

	type tuple struct{ org, env, cluster, ns string }
	cases := []tuple{
		{"55949942-3ee9-4cfc-bb7c-817bdba75186", "13b10699-40f1-4b19-b273-4b36a9977cc9", "prod-daemon", "env-13b10699"},
		{"55949942-3ee9-4cfc-bb7c-817bdba75186", "13b10699-40f1-4b19-b273-4b36a9977cc9", "hub", "control-plane-prod"},
		{"acme", "prod", "hub", "app"},
		{"acme", "staging-eu-west", "customer_cluster_1", "payments"},
		{"Acme.Corp", "e", "A1", "a"},
		{"o", "9f8e7d6c-1111-2222-3333-444455556666", "0123456789abcdef", "very-long-namespace-name-with-many-parts"},
	}

	var src strings.Builder
	src.WriteString("import forge\n\n_clusters = {\n")
	for i, c := range cases {
		fmt.Fprintf(&src, "    \"ctx-%d\" = %q\n", i, c.cluster)
	}
	src.WriteString("}\n\noutput = forge.render(forge.Bundle {\n    project = \"fixture\"\n    env = \"prod\"\n")
	src.WriteString("    cluster_target = forge.ClusterTarget {cluster = \"ctx-0\", namespace = \"x\", platform = \"amd64\"}\n")
	src.WriteString("    control_plane = forge.ControlPlane {}\n    connected_clusters = _clusters\n    required_secrets = [\n")
	for i, c := range cases {
		fmt.Fprintf(&src, "        forge.ExternalSecret {name = \"s%d\", namespace = %q, keys = [\"k\"], source = forge.StoreSource {\n", i, c.ns)
		fmt.Fprintf(&src, "            store = forge.HostedStore {org = %q, env_id = %q}\n            data = {\"k\" = \"NAME_%d\"}\n            cluster = \"ctx-%d\"\n        }}\n", c.org, c.env, i, i)
	}
	src.WriteString("    ]\n})\n")

	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"external_secret_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  src.String(),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runKCL(t, filepath.Join(dir, "main.k"))
	if err != nil {
		t.Fatalf("render: %s", kclDiagnostics(err))
	}

	var doc struct {
		Output struct {
			Manifests []struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Spec map[string]any `json:"spec"`
			} `json:"manifests"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	stores := map[string]map[string]any{}
	keys := map[string]string{}
	accounts := map[string]bool{}
	for _, m := range doc.Output.Manifests {
		switch m.Kind {
		case "SecretStore":
			stores[m.Metadata.Namespace] = m.Spec
		case "ExternalSecret":
			data := m.Spec["data"].([]any)[0].(map[string]any)
			keys[m.Metadata.Namespace] = data["remoteRef"].(map[string]any)["key"].(string)
		case "ServiceAccount":
			accounts[m.Metadata.Namespace+"/"+m.Metadata.Name] = true
		}
	}

	for i, c := range cases {
		scope := secretaccess.Scope{Org: c.org, Env: c.env}
		vault := stores[c.ns]["provider"].(map[string]any)["vault"].(map[string]any)
		jwt := vault["auth"].(map[string]any)["jwt"].(map[string]any)

		if got, want := jwt["role"], scope.PolicyName(c.cluster, c.ns); got != want {
			t.Errorf("case %d: the SecretStore logs in as role %q, but secretaccess derives %q for (%s, %s, %s, %s)", i, got, want, c.org, c.env, c.cluster, c.ns)
		}
		if got, want := jwt["path"], "jwt-"+c.cluster; got != want {
			t.Errorf("case %d: auth mount %q, want %q (secretaccess.MountName)", i, got, want)
		}
		if got, want := secretaccess.MountName(c.cluster), jwt["path"]; got != want {
			t.Errorf("case %d: secretaccess.MountName = %q, rendered %q", i, got, want)
		}
		sa := jwt["kubernetesServiceAccountToken"].(map[string]any)["serviceAccountRef"].(map[string]any)["name"]
		if sa != secretaccess.DefaultServiceAccount {
			t.Errorf("case %d: logs in as service account %q, but the role is bound to %q", i, sa, secretaccess.DefaultServiceAccount)
		}
		if !accounts[c.ns+"/"+secretaccess.DefaultServiceAccount] {
			t.Errorf("case %d: no %s ServiceAccount is rendered in %s", i, secretaccess.DefaultServiceAccount, c.ns)
		}
		// The KV v2 provider reads <path>/data/<key>; that must be the very
		// path the role's policy grants.
		read := fmt.Sprintf("%s/data/%s", vault["path"], keys[c.ns])
		if want := scope.SecretPath(fmt.Sprintf("NAME_%d", i)); read != want {
			t.Errorf("case %d: ESO reads %q, but the policy grants %q", i, read, want)
		}
	}
}
