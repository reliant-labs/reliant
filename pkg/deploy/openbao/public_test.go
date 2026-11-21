package openbao

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// updateGolden rewrites testdata/*.golden.yaml. A flag, not an env var:
// forge/pkg must not read the ambient environment (internal/pkgguard).
var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden.yaml from the current render")

func publicSpec() Spec {
	s := raftSpec()
	s.Public = &Public{
		Host: "secrets.example.com", Gateway: "public", GatewayNamespace: "demo-prod", Listener: "openbao-public",
		RequestsPerSecond: 500, PerSourceRequestsPerSecond: 50, MaxConnections: 2000, RequestTimeout: "30s",
		Port: 8210, ProxyNamespace: "envoy-gateway-system",
		TrustedProxyCIDRs: []string{"10.0.0.0/8", "fc00::/7"},
	}
	return s
}

// publicKinds are the kinds the endpoint adds beside the store's own.
var publicKinds = map[string]bool{
	"ReferenceGrant": true, "BackendTLSPolicy": true, "HTTPRoute": true,
	"BackendTrafficPolicy": true, "ClientTrafficPolicy": true,
}

// The whole public object set, pinned. A change to a rendered field is a
// reviewed diff in testdata/public.golden.yaml. The set is also the part of the
// render a Gateway controller reads, so it is the one that must stay valid
// against Envoy Gateway v1.9.2 (see TestPublicSmoke_EnvoyAndOpenBao).
//
//	go test ./pkg/deploy/openbao -run TestPublic_Golden -update
func TestPublic_Golden(t *testing.T) {
	objs, err := Render(publicSpec())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, o := range objs {
		if !publicKinds[o.GetKind()] && !(o.GetKind() == "NetworkPolicy" || o.GetKind() == "Service" && o.GetName() == "openbao") {
			continue
		}
		b, err := yaml.Marshal(o.Object)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString("---\n")
		buf.Write(b)
	}
	path := filepath.Join("testdata", "public.golden.yaml")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with -update)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("the public render drifted from %s (regenerate with -update and review the diff)\n%s", path, firstDiffLine(string(want), buf.String()))
	}
}

func firstDiffLine(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "line " + itoa(i+1) + ":\n  golden: " + wl + "\n  render: " + gl
		}
	}
	return "(no line differs)"
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// No public => none of it. The default render is byte-for-byte what SF6 shipped.
func TestPublic_UnsetRendersNothing(t *testing.T) {
	objs, err := Render(raftSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if publicKinds[o.GetKind()] {
			t.Errorf("%s/%s rendered without Public", o.GetKind(), o.GetName())
		}
	}
	cfg := configOf(t, objs)
	if strings.Contains(cfg, "x_forwarded_for") || strings.Count(cfg, `listener "tcp"`) != 1 {
		t.Errorf("an unpublished store trusts X-Forwarded-For or has a second listener:\n%s", cfg)
	}
	np := findObj(t, objs, "NetworkPolicy", "openbao")
	raw, _ := json.Marshal(np.Object)
	if strings.Contains(string(raw), "envoy") {
		t.Errorf("the NetworkPolicy admits a proxy without Public: %s", raw)
	}
}

// ---- the allowlist ---------------------------------------------------------

type route struct{ method, path string }

// matchAllowlist is how Envoy applies the route's matches: any match whose
// method AND path both hold. Regular expressions are RE2 and anchored by the
// pattern itself (Envoy requires a full match), exactly as Go's regexp here.
func matchAllowlist(t *testing.T, s Spec, r route) bool {
	t.Helper()
	for _, m := range s.PublicAllowlist() {
		if m.Method != r.method {
			continue
		}
		switch m.Type {
		case "Exact":
			if m.Value == r.path {
				return true
			}
		case "RegularExpression":
			if regexp.MustCompile(m.Value).MatchString(r.path) {
				return true
			}
		default:
			t.Fatalf("allowlist uses match type %q: only Exact and RegularExpression are allowed (a PathPrefix would route a whole subtree)", m.Type)
		}
	}
	return false
}

// Only the paths the design lists are routed. Everything else, any method, gets
// a 404 from Envoy because no route matches.
func TestPublicAllowlist_RoutesOnlyWhatTheDesignLists(t *testing.T) {
	s := publicSpec()
	allowed := []route{
		{"PUT", "/v1/auth/jwt-hub/login"}, // ESO's Vault client writes with PUT
		{"POST", "/v1/auth/jwt-hub/login"},
		{"PUT", "/v1/auth/jwt-gke_edge-1/login"},
		{"GET", "/v1/auth/token/lookup-self"}, // ESO validates a store with this
		{"GET", "/v1/secret/data/tenants/acme/prod/STRIPE_SECRET_KEY"},
		{"GET", "/v1/secret/data/tenants/13b10699-0c5d-4b6c-9f43-2c8f0f2d7b3a/13b10699/DB_URL"}, // a UUID org
		{"GET", "/v1/secret/data/tenants/acme/prod/_PRIVATE"},
	}
	for _, r := range allowed {
		if !matchAllowlist(t, s, r) {
			t.Errorf("%s %s is not routed; ESO needs it", r.method, r.path)
		}
	}
	refused := []route{
		// the system and the rest of the API
		{"GET", "/v1/sys/health"}, {"GET", "/v1/sys/seal-status"}, {"PUT", "/v1/sys/init"}, {"PUT", "/v1/sys/unseal"},
		{"GET", "/v1/sys/metrics"}, {"GET", "/v1/sys/leader"}, {"GET", "/v1/sys/internal/ui/mounts"},
		{"PUT", "/v1/sys/policies/acl/root"}, {"PUT", "/v1/sys/auth/jwt-x"}, {"GET", "/v1/sys/storage/raft/snapshot"},
		{"LIST", "/v1/secret/metadata/tenants/acme/prod"}, {"GET", "/v1/secret/metadata/tenants/acme/prod/DB_URL"},
		{"GET", "/v1/secret/tenants/acme/prod/DB_URL"},
		{"GET", "/"}, {"GET", "/ui/"}, {"GET", "/v1/"}, {"GET", "/v1/auth/token/lookup"}, {"POST", "/v1/auth/token/create"},
		{"POST", "/v1/auth/token/renew-self"}, {"POST", "/v1/auth/token/revoke-self"}, {"GET", "/v1/auth/token/lookup-self/x"},
		// methods the allowlist does not name, on a path it does
		{"GET", "/v1/auth/jwt-hub/login"}, {"DELETE", "/v1/auth/jwt-hub/login"}, {"PUT", "/v1/auth/token/lookup-self"},
		{"POST", "/v1/secret/data/tenants/acme/prod/DB_URL"}, {"PUT", "/v1/secret/data/tenants/acme/prod/DB_URL"},
		{"DELETE", "/v1/secret/data/tenants/acme/prod/DB_URL"}, {"PATCH", "/v1/secret/data/tenants/acme/prod/DB_URL"},
		// shapes of the read path that must not widen it
		{"GET", "/v1/secret/data/tenants/acme/prod/"},       // the directory
		{"GET", "/v1/secret/data/tenants/acme/prod"},        // a name that is not there
		{"GET", "/v1/secret/data/tenants/acme"},             // the org
		{"GET", "/v1/secret/data/tenants/acme/prod/a/b"},    // too deep
		{"GET", "/v1/secret/data/tenants//prod/DB_URL"},     // an empty segment
		{"GET", "/v1/secret/data/tenants/acme/prod/DB-URL"}, // not an env-var name
		{"GET", "/v1/secret/data/tenants/acme/prod/1DB"},    // starts with a digit
		{"GET", "/v1/secret/data/tenants/../prod/DB_URL"},
		{"GET", "/v1/secret/data/tenants/acme/../DB_URL"},
		{"GET", "/v1/secret/data/tenants/acme/prod/DB_URL/"},
		{"GET", "/v1/secret/data/tenants/acme/prod/DB_URL/extra"},
		{"GET", "/v1/secret/data/tenants/acme/prod/DB_URL%2F..%2Fother"},
		{"GET", "/v1/secret/data/local/proj/dev/DB_URL"}, // the local root is never served
		{"GET", "/v1/secret/data/tenants/*/*/DB_URL"},
		{"GET", "/v1/other/data/tenants/acme/prod/DB_URL"}, // a different mount
		{"GET", "/v1/secret/data/tenants/acme/prod/DB_URL\n"},
		{"GET", "/v1//secret/data/tenants/acme/prod/DB_URL"},
		{"GET", "/v1/auth/jwt-/login"}, {"GET", "/v1/auth/jwt-hub/role/x"}, {"PUT", "/v1/auth/jwt-hub/role/x"},
		{"PUT", "/v1/auth/jwt-hub/config"}, {"PUT", "/v1/auth/jwt-hub/login/extra"}, {"PUT", "/v1/auth/jwt-hub/../sys/mounts/x/login"},
		{"PUT", "/v1/auth/oidc-hub/login"}, {"PUT", "/v1/auth/userpass/login/admin"}, {"PUT", "/v1/auth/approle/login"},
		{"PUT", "/v1/auth/jwt-hub/x/login"}, {"PUT", "/v1/auth/jwt-hu/b/login"},
	}
	for _, r := range refused {
		if matchAllowlist(t, s, r) {
			t.Errorf("%s %q is routed; the public host must answer it with 404", r.method, r.path)
		}
	}
}

// The edge routes only paths a policy could grant. The patterns here and the
// rules pkg/secretaccess enforces on write are written separately; this closes
// the loop, so a segment the store accepts is never one the edge refuses, and
// the reverse.
func TestPublicAllowlist_AgreesWithSecretaccess(t *testing.T) {
	s := publicSpec()
	for _, c := range []struct {
		org, env, name string
		ok             bool
	}{
		{"acme", "prod", "STRIPE_SECRET_KEY", true},
		{"13b10699-0c5d-4b6c-9f43-2c8f0f2d7b3a", "13b10699-0c5d-4b6c-9f43-2c8f0f2d7b3a", "OTEL_AGENT_TOKEN_DAEMON", true},
		{"a.b", "p-1", "_X", true},
		{"acme", "prod", "bad-name", false},
		{"acme", "prod", "9lives", false},
		{"-acme", "prod", "X", false},
		{"a/b", "prod", "X", false},
		{"acme", "*", "X", false},
		{"acme", "..", "X", false},
	} {
		scope := secretaccess.Scope{Org: c.org, Env: c.env}
		storeOK := scope.Validate() == nil && secretaccess.ValidName(c.name)
		if storeOK != c.ok {
			t.Fatalf("test table is wrong about secretaccess for %+v: store says %v", c, storeOK)
		}
		path := "/v1/" + scope.SecretPath(c.name)
		if got := matchAllowlist(t, s, route{"GET", path}); got != c.ok {
			t.Errorf("GET %s: the edge routes it = %v, secretaccess accepts it = %v", path, got, c.ok)
		}
	}

	// Mount names: the login path is exactly a cluster-id mount.
	for _, id := range []string{"hub", "gke_us-central1-a_prod", "13b1", "A1"} {
		if !matchAllowlist(t, s, route{"PUT", "/v1/auth/" + secretaccess.MountName(id) + "/login"}) {
			t.Errorf("login on mount %s is not routed", secretaccess.MountName(id))
		}
	}
}

// The KV mount and the first path segment come from the spec, so a store with
// its own mount name is served on it and no other.
func TestPublicAllowlist_FollowsTheKVMountAndRoot(t *testing.T) {
	s := publicSpec()
	s.KV.Mount = "kv"
	if !matchAllowlist(t, s, route{"GET", "/v1/kv/data/tenants/acme/prod/X"}) {
		t.Error("the configured KV mount is not served")
	}
	if matchAllowlist(t, s, route{"GET", "/v1/secret/data/tenants/acme/prod/X"}) {
		t.Error("the default mount is served on a store that renamed it")
	}
	s.Access.Root = "local"
	if !matchAllowlist(t, s, route{"GET", "/v1/kv/data/local/acme/prod/X"}) || matchAllowlist(t, s, route{"GET", "/v1/kv/data/tenants/acme/prod/X"}) {
		t.Error("the allowlist does not follow Access.Root")
	}
}

// The allowlist is the route's matches, one for one, all in one rule: a second
// rule or a PathPrefix would be a second way in.
func TestPublicRoute_IsExactlyTheAllowlist(t *testing.T) {
	s := publicSpec()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	route := findObj(t, objs, "HTTPRoute", "openbao-public")
	spec := route.Object["spec"].(map[string]any)
	rules := spec["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("%d rules, want 1", len(rules))
	}
	matches := rules[0].(map[string]any)["matches"].([]any)
	want := s.PublicAllowlist()
	if len(matches) != len(want) {
		t.Fatalf("%d matches, want %d", len(matches), len(want))
	}
	for i, m := range matches {
		mm := m.(map[string]any)
		p := mm["path"].(map[string]any)
		if mm["method"] != want[i].Method || p["type"] != want[i].Type || p["value"] != want[i].Value {
			t.Errorf("match %d = %v, want %+v", i, mm, want[i])
		}
		if p["type"] == "PathPrefix" {
			t.Errorf("match %d is a PathPrefix", i)
		}
	}
	if spec["hostnames"].([]any)[0] != "secrets.example.com" {
		t.Errorf("hostnames = %v", spec["hostnames"])
	}
	// The one backend is OpenBao's Service on the Gateway's own port.
	ref := rules[0].(map[string]any)["backendRefs"].([]any)
	if len(ref) != 1 || ref[0].(map[string]any)["name"] != "openbao" || ref[0].(map[string]any)["port"] != int64(8210) {
		t.Errorf("backendRefs = %v", ref)
	}
}

// ---- the rest of the object set -------------------------------------------

func TestPublic_BackendTLSPolicyVerifiesOpenBaosCertificate(t *testing.T) {
	objs, _ := Render(publicSpec())
	btp := findObj(t, objs, "BackendTLSPolicy", "openbao-public")
	if btp.GetNamespace() != "openbao" {
		t.Errorf("the policy must live with the Service it targets: %s", btp.GetNamespace())
	}
	raw, _ := json.Marshal(btp.Object["spec"])
	for _, want := range []string{
		`"hostname":"openbao.openbao.svc"`,                                          // a name the server certificate always carries
		`"caCertificateRefs":[{"group":"","kind":"ConfigMap","name":"openbao-ca"}]`, // the CA SF6 publishes
		`"sectionName":"public"`,                                                    // the Gateway's port only
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("BackendTLSPolicy lacks %s:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "insecure") || strings.Contains(string(raw), "wellKnownCACertificates") {
		t.Errorf("the policy must pin OpenBao's CA, not trust the system roots or skip verification: %s", raw)
	}
	// The name verified must be in the certificate the CA signed.
	s := publicSpec()
	have := false
	for _, n := range s.dnsNames() {
		if n == s.Host() {
			have = true
		}
	}
	if !have {
		t.Errorf("the verified name %q is not among the certificate's names %v", s.Host(), s.dnsNames())
	}
	// The public host never has to be in the internal certificate.
	for _, n := range s.dnsNames() {
		if n == "secrets.example.com" {
			t.Error("the public host is in the internal certificate: it only needs the Service name")
		}
	}
}

func TestPublic_ReferenceGrantOnlyAcrossNamespaces(t *testing.T) {
	s := publicSpec()
	objs, _ := Render(s)
	g := findObj(t, objs, "ReferenceGrant", "openbao-public-from-demo-prod")
	raw, _ := json.Marshal(g.Object["spec"])
	if !strings.Contains(string(raw), `"from":[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute","namespace":"demo-prod"}]`) ||
		!strings.Contains(string(raw), `"to":[{"group":"","kind":"Service","name":"openbao"}]`) {
		t.Errorf("grant = %s", raw)
	}
	s.Public.GatewayNamespace = "openbao"
	objs, _ = Render(s)
	for _, o := range objs {
		if o.GetKind() == "ReferenceGrant" {
			t.Error("a same-namespace route needs no ReferenceGrant")
		}
	}
}

func TestPublic_PoliciesTargetTheRouteAndListener(t *testing.T) {
	objs, _ := Render(publicSpec())
	btp := findObj(t, objs, "BackendTrafficPolicy", "openbao-public")
	ctp := findObj(t, objs, "ClientTrafficPolicy", "openbao-public")
	for _, o := range []*unstructured.Unstructured{btp, ctp} {
		if o.GetNamespace() != "demo-prod" {
			t.Errorf("%s/%s is in %s: a policy lives with its target", o.GetKind(), o.GetName(), o.GetNamespace())
		}
	}
	braw, _ := json.Marshal(btp.Object["spec"])
	for _, want := range []string{
		`"targetRefs":[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute","name":"openbao-public"}]`,
		`"requestBuffer":{"limit":"16Ki"}`,
		`{"clientSelectors":[{"path":{"type":"PathPrefix","value":"/"}}],"limit":{"requests":500,"unit":"Second"}}`,
		`{"clientSelectors":[{"sourceCIDR":{"type":"Distinct","value":"0.0.0.0/0"}}],"limit":{"requests":50,"unit":"Second"}}`,
		`{"clientSelectors":[{"sourceCIDR":{"type":"Distinct","value":"::/0"}}],"limit":{"requests":50,"unit":"Second"}}`,
	} {
		if !strings.Contains(string(braw), want) {
			t.Errorf("BackendTrafficPolicy lacks %s:\n%s", want, braw)
		}
	}
	craw, _ := json.Marshal(ctp.Object["spec"])
	for _, want := range []string{
		`"targetRefs":[{"group":"gateway.networking.k8s.io","kind":"Gateway","name":"public","sectionName":"openbao-public"}]`,
		`"connection":{"connectionLimit":{"value":2000}}`,
		`"requestReceivedTimeout":"30s"`,
		`"escapedSlashesAction":"RejectRequest"`,
		`"earlyRequestHeaders":{"remove":["x-forwarded-for"]}`,
	} {
		if !strings.Contains(string(craw), want) {
			t.Errorf("ClientTrafficPolicy lacks %s:\n%s", want, craw)
		}
	}
}

// ---- XFF and the network ---------------------------------------------------

func TestPublic_OnlyTheGatewayPortTrustsForwardedFor(t *testing.T) {
	objs, _ := Render(publicSpec())
	cfg := configOf(t, objs)
	if n := strings.Count(cfg, `listener "tcp"`); n != 2 {
		t.Fatalf("%d listeners, want the API listener and the Gateway's:\n%s", n, cfg)
	}
	api, pub, _ := strings.Cut(cfg, `listener "tcp" {`+"\n  address = \"0.0.0.0:8210\"")
	if strings.Contains(api, "x_forwarded_for") {
		t.Errorf("the in-cluster listener trusts X-Forwarded-For; ESO, the converge Job and the kubelet reach it directly:\n%s", api)
	}
	for _, want := range []string{
		`tls_cert_file = "/openbao/tls/tls.crt"`,
		`x_forwarded_for_authorized_addrs = ["10.0.0.0/8", "fc00::/7"]`,
		"disallow_metrics = true",
	} {
		if !strings.Contains(pub, want) {
			t.Errorf("the Gateway listener lacks %s:\n%s", want, pub)
		}
	}
	// An unauthorised peer and a missing header are both refused, explicitly:
	// OpenBao's documented defaults are not what v2.6.2 does (see publicListener).
	for _, strict := range []string{"x_forwarded_for_reject_not_authorized = true", "x_forwarded_for_reject_not_present = true"} {
		if !strings.Contains(pub, strict) {
			t.Errorf("the Gateway listener lacks %s:\n%s", strict, pub)
		}
	}
	for _, loose := range []string{"x_forwarded_for_reject_not_authorized = false", "x_forwarded_for_reject_not_present = false", "tls_disable"} {
		if strings.Contains(pub, loose) {
			t.Errorf("the Gateway listener relaxes %s", loose)
		}
	}
	if strings.Contains(pub, "unauthenticated_metrics_access") {
		t.Error("metrics are reachable on the Gateway's port")
	}
}

func TestPublic_ServiceAndPodExposeThePort(t *testing.T) {
	objs, _ := Render(publicSpec())
	svc, _ := json.Marshal(findObj(t, objs, "Service", "openbao").Object["spec"])
	if !strings.Contains(string(svc), `"name":"public","port":8210`) {
		t.Errorf("the Service lacks the Gateway's port: %s", svc)
	}
	ss, _ := json.Marshal(findObj(t, objs, "StatefulSet", "openbao").Object["spec"])
	if !strings.Contains(string(ss), `{"containerPort":8210,"name":"public"}`) {
		t.Errorf("the pod lacks the Gateway's port: %s", ss)
	}
}

// The Gateway's port is open to the proxy pods of ONE Gateway and to nobody
// else, and the proxy pods can reach nothing else of OpenBao.
func TestPublic_NetworkPolicyAdmitsOnlyThatGatewaysProxy(t *testing.T) {
	objs, _ := Render(publicSpec())
	np := findObj(t, objs, "NetworkPolicy", "openbao")
	ingress := np.Object["spec"].(map[string]any)["ingress"].([]any)
	if len(ingress) != 3 {
		t.Fatalf("%d ingress rules, want 3 (peers, raft, gateway)", len(ingress))
	}
	pub, _ := json.Marshal(ingress[2])
	for _, want := range []string{
		`"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"envoy-gateway-system"}}`,
		`"gateway.envoyproxy.io/owning-gateway-name":"public"`,
		`"gateway.envoyproxy.io/owning-gateway-namespace":"demo-prod"`,
		`"app.kubernetes.io/managed-by":"envoy-gateway"`,
		`"ports":[{"port":8210,"protocol":"TCP"}]`,
	} {
		if !strings.Contains(string(pub), want) {
			t.Errorf("gateway rule lacks %s:\n%s", want, pub)
		}
	}
	// The other rules do not name the proxy and do not open the Gateway's port.
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(ingress[i])
		if strings.Contains(string(raw), "envoy") || strings.Contains(string(raw), "8210") {
			t.Errorf("rule %d reaches the proxy or the Gateway's port: %s", i, raw)
		}
	}
}

// ---- refusals --------------------------------------------------------------

func TestPublic_ValidateRefusesAndNamesTheFix(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*Spec)
		want []string
	}{
		"host":             {func(s *Spec) { s.Public.Host = "Secrets" }, []string{"not a DNS name", "public.host"}},
		"no gateway":       {func(s *Spec) { s.Public.Gateway = "" }, []string{"gateway is required"}},
		"zero limit":       {func(s *Spec) { s.Public.MaxConnections = 0 }, []string{"at least 1", "Unpublish"}},
		"per source > all": {func(s *Spec) { s.Public.PerSourceRequestsPerSecond = 900 }, []string{"exceeds requestsPerSecond", "Lower"}},
		"duration":         {func(s *Spec) { s.Public.RequestTimeout = "1.5s" }, []string{"not a Gateway API duration", "h, m, s or ms"}},
		"no cidrs":         {func(s *Spec) { s.Public.TrustedProxyCIDRs = nil }, []string{"trustedProxyCIDRs is empty", "pod CIDR"}},
		"bad cidr":         {func(s *Spec) { s.Public.TrustedProxyCIDRs = []string{"10.0.0.0"} }, []string{"not a CIDR"}},
		"cluster port":     {func(s *Spec) { s.Public.Port = ClusterPort }, []string{"cluster port"}},
		"listener port":    {func(s *Spec) { s.Public.Port = 8200 }, []string{"already listener 8200", "only it is trusted"}},
		"port range":       {func(s *Spec) { s.Public.Port = 70000 }, []string{"1..65535"}},
		"namespace":        {func(s *Spec) { s.Public.ProxyNamespace = "Envoy_GW" }, []string{"RFC-1123"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := publicSpec()
			tc.mut(&s)
			_, err := Render(s)
			if err == nil {
				t.Fatal("rendered")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
		})
	}
}

func TestPublic_RenderIsDeterministic(t *testing.T) {
	a, _ := Render(publicSpec())
	b, _ := Render(publicSpec())
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Error("two renders of one spec differ")
	}
}

// A request on the Gateway's port that did not come through Envoy is refused by
// OpenBao itself, not by trust in the network alone.
func TestPublic_ConfigHasNoPublicSideEffectsOnTheAPIListener(t *testing.T) {
	with, _ := Render(publicSpec())
	without, _ := Render(raftSpec())
	w, wo := configOf(t, with), configOf(t, without)
	api, _, _ := strings.Cut(w, "listener \"tcp\" {\n  address = \"0.0.0.0:8210\"")
	if !strings.HasPrefix(api, wo[:strings.Index(wo, "seal \"static\"")]) {
		t.Errorf("publishing changed the API listener's config:\n--- with\n%s\n--- without\n%s", api, wo)
	}
	_ = http.StatusOK
}
