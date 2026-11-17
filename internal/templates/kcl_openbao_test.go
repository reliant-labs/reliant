package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/openbao"
)

// TestOpenBaoGrantsWhatTheSecretStoreAsksFor closes the loop between the two
// halves of the platform-secrets render. kcl/lib/external_secret.k renders the
// SecretStore a namespace logs in with (mount, role, audience, server, CA,
// KV path); kcl/lib/openbao.k lowers the component whose converge Job creates
// what that login needs. They are written separately, so this renders ONE
// Bundle containing both, expands the OpenBao record through the real Go
// renderer, and requires the access document to contain exactly the role the
// SecretStore names: on the mount it names, bound to the audience it requests,
// with a policy granting the path its ExternalSecret reads. A mismatch is a
// namespace that silently syncs nothing.
func TestOpenBaoGrantsWhatTheSecretStoreAsksFor(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)

	const main = `import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
    required_secrets = [
        forge.ExternalSecret {
            name = "stripe"
            namespace = "payments"
            keys = ["key"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"key" = "STRIPE_KEY"}
            }
        }
        forge.ExternalSecret {
            name = "db"
            namespace = "orders"
            keys = ["url"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"url" = "DB_URL"}
            }
        }
    ]
})
`
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"openbao_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  main,
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
			Manifests []map[string]any `json:"manifests"`
			OpenBao   struct {
				ServerURL   string `json:"server_url"`
				CAConfigMap string `json:"ca_configmap"`
				AuthMount   string `json:"auth_mount"`
				Audience    string `json:"audience"`
			} `json:"openbao"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	var spec openbao.Spec
	stores := map[string]map[string]any{}
	keys := map[string]string{}
	for _, m := range doc.Output.Manifests {
		md, _ := m["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		switch m["kind"] {
		case openbao.Kind:
			raw, _ := json.Marshal(m["spec"])
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&spec); err != nil {
				t.Fatalf("the Go renderer cannot read the record KCL emits: %v", err)
			}
		case "SecretStore":
			stores[ns] = m["spec"].(map[string]any)
		case "ExternalSecret":
			data := m["spec"].(map[string]any)["data"].([]any)[0].(map[string]any)
			keys[ns] = data["remoteRef"].(map[string]any)["key"].(string)
		}
	}
	if spec.Name == "" {
		t.Fatal("no OpenBao record in the stream")
	}
	if len(stores) != 2 {
		t.Fatalf("want a SecretStore in each of payments and orders, got %v", stores)
	}

	access, err := openbao.BuildAccess(spec)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]openbao.Role{}
	for _, r := range access.Roles {
		roles[r.Mount+"/"+r.Name] = r
	}
	policies := map[string]openbao.Policy{}
	for _, p := range access.Policies {
		policies[p.Name] = p
	}

	for ns, st := range stores {
		vault := st["provider"].(map[string]any)["vault"].(map[string]any)
		jwt := vault["auth"].(map[string]any)["jwt"].(map[string]any)
		mount, role := jwt["path"].(string), jwt["role"].(string)

		if mount != doc.Output.OpenBao.AuthMount {
			t.Errorf("%s: the SecretStore logs in on %q, the component's resolved mount is %q", ns, mount, doc.Output.OpenBao.AuthMount)
		}
		r, ok := roles[mount+"/"+role]
		if !ok {
			t.Fatalf("%s: the SecretStore logs in as %s/%s, which the access document does not create (it has %v)", ns, mount, role, keysOf(roles))
		}
		aud := jwt["kubernetesServiceAccountToken"].(map[string]any)["audiences"].([]any)[0].(string)
		if got := r.Payload["bound_audiences"].([]string)[0]; got != aud {
			t.Errorf("%s: the SecretStore requests audience %q, the role requires %q", ns, aud, got)
		}
		if aud != doc.Output.OpenBao.Audience {
			t.Errorf("%s: audience %q differs from the resolved block's %q", ns, aud, doc.Output.OpenBao.Audience)
		}
		if want := "system:serviceaccount:" + ns + ":reliant-secrets"; r.Payload["bound_subject"] != want {
			t.Errorf("%s: role subject %v, want %s", ns, r.Payload["bound_subject"], want)
		}
		if vault["server"] != doc.Output.OpenBao.ServerURL {
			t.Errorf("%s: the SecretStore dials %v, the component serves %q", ns, vault["server"], doc.Output.OpenBao.ServerURL)
		}
		if ca := vault["caProvider"].(map[string]any); ca["name"] != doc.Output.OpenBao.CAConfigMap || ca["type"] != "ConfigMap" {
			t.Errorf("%s: the SecretStore trusts %v, the component publishes ConfigMap %q", ns, ca, doc.Output.OpenBao.CAConfigMap)
		}
		read := vault["path"].(string) + "/data/" + keys[ns]
		granted := false
		for _, rule := range policies[role].Rules {
			if rule.Path == read && len(rule.Capabilities) == 1 && rule.Capabilities[0] == "read" {
				granted = true
			}
		}
		if !granted {
			t.Errorf("%s: ESO reads %q, but policy %s grants %v", ns, read, role, policies[role].Rules)
		}
		if !slices.Contains(spec.CANamespaces, ns) || !slices.Contains(spec.IngressNamespaces, ns) {
			t.Errorf("%s: holds a SecretStore but is not given the CA / admitted by the NetworkPolicy (%v / %v)", ns, spec.CANamespaces, spec.IngressNamespaces)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestOpenBaoPublic_KCLRecordExpandsToWhatTheGatewayServes closes the loop of
// the public endpoint the way the test above closes the loop of the store. KCL
// declares the Gateway, adds the OpenBao listener to it and emits the record;
// Go expands the record into the route and policies. They are written
// separately, so this renders ONE Bundle containing both, decodes the record
// STRICTLY (as production does) and expands it, then requires that:
//
//   - the HTTPRoute attaches to a listener the rendered Gateway really has, on
//     the Gateway's name, and serves the host the SecretStore of a remote
//     cluster dials;
//   - the listener has a certificate that is issued (a Certificate fills the
//     Secret it names) and is its own;
//   - every audience the roles require is the public host's, so a token minted
//     for it is the one that logs in;
//   - the route's login match is the mount the access document creates, and the
//     read match covers the path the ExternalSecret reads, so ESO's two calls
//     both get through the allowlist;
//   - the policies, the route and the Gateway all land on the cluster that
//     runs the store.
func TestOpenBaoPublic_KCLRecordExpandsToWhatTheGatewayServes(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)

	const main = `import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}
_edge = forge.ClusterTarget {cluster = "gke_acme_edge", namespace = "edge", platform = "amd64", connected_cluster = "edge"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    gateways = [forge.Gateway {
        name = "public"
        tls = forge.GatewayTLS {cert_issuer = "letsencrypt-dns01", secret_name = "public-tls"}
        listeners = [forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}]
    }]
    openbao = forge.OpenBao {public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public"}}
    external_secrets_operators = [
        forge.ExternalSecretsOperator {cluster = _hub}
        forge.ExternalSecretsOperator {cluster = _edge}
    ]
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
    required_secrets = [
        forge.ExternalSecret {
            name = "stripe"
            namespace = "payments"
            keys = ["key"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"key" = "STRIPE_KEY"}
                cluster = "gke_acme_edge"
            }
        }
    ]
})
`
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"openbao_public_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  main,
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
			Manifests []map[string]any `json:"manifests"`
			OpenBao   struct {
				Audience string `json:"audience"`
				Cluster  string `json:"cluster"`
			} `json:"openbao"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	var spec openbao.Spec
	var gateway map[string]any
	certs := map[string]string{} // secret -> dnsName
	stores := map[string]map[string]any{}
	keys := map[string]string{}
	for _, m := range doc.Output.Manifests {
		md, _ := m["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		switch m["kind"] {
		case openbao.Kind:
			raw, _ := json.Marshal(m["spec"])
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&spec); err != nil {
				t.Fatalf("the Go renderer cannot read the record KCL emits: %v", err)
			}
		case "Gateway":
			gateway = m
		case "Certificate":
			s := m["spec"].(map[string]any)
			if names, _ := s["dnsNames"].([]any); len(names) == 1 {
				certs[s["secretName"].(string)] = names[0].(string)
			}
		case "SecretStore":
			stores[ns] = m["spec"].(map[string]any)
		case "ExternalSecret":
			keys[ns] = m["spec"].(map[string]any)["data"].([]any)[0].(map[string]any)["remoteRef"].(map[string]any)["key"].(string)
		}
	}
	if spec.Public == nil {
		t.Fatal("the record carries no public block")
	}

	objs, err := openbao.Render(spec)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	find := func(kind string) map[string]any {
		for _, o := range objs {
			if o.GetKind() == kind {
				return o.Object
			}
		}
		t.Fatalf("no %s rendered", kind)
		return nil
	}

	// The route attaches to a listener the Gateway has, for the host that listener serves.
	route := find("HTTPRoute")
	parent := route["spec"].(map[string]any)["parentRefs"].([]any)[0].(map[string]any)
	var listener map[string]any
	for _, l := range gateway["spec"].(map[string]any)["listeners"].([]any) {
		if l.(map[string]any)["name"] == parent["sectionName"] {
			listener = l.(map[string]any)
		}
	}
	if parent["name"] != "public" || listener == nil {
		t.Fatalf("the route attaches to %v, which the rendered Gateway does not have (listeners %v)", parent, gateway["spec"].(map[string]any)["listeners"])
	}
	host := route["spec"].(map[string]any)["hostnames"].([]any)[0]
	if listener["hostname"] != host || listener["protocol"] != "HTTPS" {
		t.Errorf("the route serves %v on listener %v", host, listener)
	}
	// The listener's certificate is issued, and is its own.
	secret := listener["tls"].(map[string]any)["certificateRefs"].([]any)[0].(map[string]any)["name"].(string)
	if certs[secret] != "secrets.example.com" {
		t.Errorf("no single-name Certificate fills listener Secret %q (have %v)", secret, certs)
	}
	if secret == "public-tls" {
		t.Error("the public host shares the Gateway's certificate: adding it would re-issue that one")
	}
	// The remote cluster's SecretStore dials the host and asks for its audience.
	edge := stores["payments"]["provider"].(map[string]any)["vault"].(map[string]any)
	if edge["server"] != "https://secrets.example.com" {
		t.Errorf("the edge SecretStore dials %v, not the public host", edge["server"])
	}
	jwt := edge["auth"].(map[string]any)["jwt"].(map[string]any)
	aud := jwt["kubernetesServiceAccountToken"].(map[string]any)["audiences"].([]any)[0].(string)
	if !strings.HasPrefix(aud, "https://secrets.example.com/c/") || aud != "https://secrets.example.com/c/edge" {
		t.Errorf("the edge SecretStore requests audience %q", aud)
	}

	// Both of ESO's calls pass the allowlist: the login on the mount the
	// access document creates, and the read of the path the ExternalSecret names.
	access, err := openbao.BuildAccess(spec)
	if err != nil {
		t.Fatal(err)
	}
	allowed := func(method, path string) bool {
		for _, m := range spec.PublicAllowlist() {
			if m.Method != method {
				continue
			}
			if m.Type == "Exact" && m.Value == path {
				return true
			}
			if m.Type == "RegularExpression" && regexp.MustCompile(m.Value).MatchString(path) {
				return true
			}
		}
		return false
	}
	// The remote cluster's mount (jwt-edge) is created when its issuer is
	// registered (forge cluster connect), not by this render, so the document
	// is checked on the store's own cluster; the edge login only has to pass
	// the allowlist.
	mount := jwt["path"].(string)
	selfMount := "jwt-" + access.Self
	hasSelf := false
	for _, m := range access.Mounts {
		hasSelf = hasSelf || m.Name == selfMount
	}
	if !hasSelf {
		t.Errorf("the access document has no mount %q for the store's own cluster", selfMount)
	}
	for _, m := range []string{mount, selfMount} {
		if !allowed("PUT", "/v1/auth/"+m+"/login") {
			t.Errorf("ESO's login on %q is not routed", m)
		}
	}
	if !allowed("GET", "/v1/auth/token/lookup-self") {
		t.Error("ESO's token check is not routed")
	}
	if read := "/v1/" + edge["path"].(string) + "/data/" + keys["payments"]; !allowed("GET", read) {
		t.Errorf("ESO reads %q, which the allowlist does not route", read)
	}
	if allowed("GET", "/v1/sys/health") || allowed("PUT", "/v1/"+edge["path"].(string)+"/data/"+keys["payments"]) {
		t.Error("the allowlist routes health or a write")
	}

	// Everything lands on the store's cluster.
	for _, kind := range []string{"HTTPRoute", "BackendTrafficPolicy", "ClientTrafficPolicy", "BackendTLSPolicy"} {
		if ns := find(kind)["metadata"].(map[string]any)["namespace"]; kind == "BackendTLSPolicy" && ns != "openbao" || kind != "BackendTLSPolicy" && ns != "acme-prod" {
			t.Errorf("%s is in namespace %v", kind, ns)
		}
	}
	if gateway["metadata"].(map[string]any)["labels"].(map[string]any)["forge.dev/cluster"] != doc.Output.OpenBao.Cluster {
		t.Errorf("the Gateway lands on %v, the store on %s", gateway["metadata"].(map[string]any)["labels"], doc.Output.OpenBao.Cluster)
	}
}
