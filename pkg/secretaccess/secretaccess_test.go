package secretaccess

import (
	"reflect"
	"strings"
	"testing"
)

const (
	testOrg = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	testEnv = "13b10699-40f1-4b19-b273-4b36a9977cc9"
)

func baseRequest() Request {
	return Request{
		Scope: Scope{Org: testOrg, Env: testEnv},
		Clusters: []Cluster{
			{ID: "hub", Audience: "https://secrets.example.com/c/hub"},
			{ID: "daemon", Audience: "https://secrets.example.com/c/daemon"},
		},
		Bindings: []Binding{
			{Cluster: "hub", Namespace: "control-plane-prod", Names: []string{"STRIPE_SECRET_KEY", "DB_URL", "DB_URL"}},
			{Cluster: "daemon", Namespace: "env-abc", Names: []string{"TOKEN"}},
		},
	}
}

func TestDerive_ExactPathsOnly(t *testing.T) {
	doc, err := Derive(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Policies) != 2 || len(doc.Roles) != 2 || len(doc.Mounts) != 2 {
		t.Fatalf("want 2 mounts, policies and roles; got %d, %d, %d", len(doc.Mounts), len(doc.Policies), len(doc.Roles))
	}
	prefix := "secret/data/tenants/" + testOrg + "/" + testEnv + "/"
	for _, p := range doc.Policies {
		for _, r := range p.Rules {
			if strings.Contains(r.Path, "*") {
				t.Errorf("policy %s grants a glob: %s", p.Name, r.Path)
			}
			if !strings.HasPrefix(r.Path, prefix) {
				t.Errorf("policy %s leaves the env directory: %s", p.Name, r.Path)
			}
			if !reflect.DeepEqual(r.Capabilities, []string{"read"}) {
				t.Errorf("policy %s: capabilities %v, want exactly read", p.Name, r.Capabilities)
			}
		}
		if strings.Contains(p.HCL(), "list") || strings.Contains(p.HCL(), "metadata") {
			t.Errorf("policy %s mentions list or metadata:\n%s", p.Name, p.HCL())
		}
	}
	var hub Policy
	for _, p := range doc.Policies {
		if len(p.Rules) == 2 {
			hub = p
		}
	}
	want := `path "` + prefix + `DB_URL" {
  capabilities = ["read"]
}

path "` + prefix + `STRIPE_SECRET_KEY" {
  capabilities = ["read"]
}
`
	if got := hub.HCL(); got != want {
		t.Errorf("hub policy HCL\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDerive_RoleShape(t *testing.T) {
	doc, err := Derive(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	var role Role
	for _, r := range doc.Roles {
		if r.Mount == "jwt-hub" {
			role = r
		}
	}
	if role.Name != "env-13b10699-hub-"+role.Name[len(role.Name)-8:] {
		t.Errorf("role name %q does not follow env-<env8>-<cluster>-<hash8>", role.Name)
	}
	if role.BoundSubject != "system:serviceaccount:control-plane-prod:reliant-secrets" {
		t.Errorf("subject = %q", role.BoundSubject)
	}
	if !reflect.DeepEqual(role.Policies, []string{role.Name}) {
		t.Errorf("policies = %v, want the role's own policy", role.Policies)
	}
	got := role.Payload()
	want := map[string]any{
		"role_type":       "jwt",
		"user_claim":      "sub",
		"bound_audiences": []string{"https://secrets.example.com/c/hub"},
		"bound_subject":   "system:serviceaccount:control-plane-prod:reliant-secrets",
		"token_policies":  []string{role.Name},
		"token_type":      "batch",
		"token_ttl":       600,
		"token_max_ttl":   600,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload\n got %v\nwant %v", got, want)
	}
	if role.Path() != "auth/jwt-hub/role/"+role.Name {
		t.Errorf("path = %s", role.Path())
	}
}

func TestDerive_Deterministic(t *testing.T) {
	a, err := Derive(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Same content, every list shuffled.
	r := baseRequest()
	r.Clusters[0], r.Clusters[1] = r.Clusters[1], r.Clusters[0]
	r.Bindings[0], r.Bindings[1] = r.Bindings[1], r.Bindings[0]
	r.Bindings[1].Names = []string{"DB_URL", "STRIPE_SECRET_KEY"}
	b, err := Derive(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("order of the request changed the document:\n%+v\n%+v", a, b)
	}
}

func TestPolicyName_DistinctPerTuple(t *testing.T) {
	s := Scope{Org: testOrg, Env: testEnv}
	seen := map[string]string{}
	for _, c := range []string{"hub", "daemon"} {
		for _, ns := range []string{"a", "b", "ab"} {
			n := s.PolicyName(c, ns)
			if prev, dup := seen[n]; dup {
				t.Errorf("%s and %s/%s share policy name %s", prev, c, ns, n)
			}
			seen[n] = c + "/" + ns
		}
	}
	other := Scope{Org: testOrg, Env: "99999999-3ee9-4cfc-bb7c-817bdba75186"}
	if s.PolicyName("hub", "a") == other.PolicyName("hub", "a") {
		t.Error("two envs share a policy name")
	}
	// Two envs that shorten to the same label still differ by hash.
	e1 := Scope{Org: "p", Env: "a-very-long-environment-name-one"}
	e2 := Scope{Org: "p", Env: "a-very-long-environment-name-two"}
	if e1.PolicyName("hub", "a") == e2.PolicyName("hub", "a") {
		t.Error("envs with long names collide")
	}
	if got := s.PolicyName("hub", "a"); !strings.HasPrefix(got, "env-13b10699-hub-") || len(got) != len("env-13b10699-hub-")+8 {
		t.Errorf("name = %q", got)
	}
}

func TestDerive_WildcardNeedsReason(t *testing.T) {
	r := baseRequest()
	r.Bindings[0].Wildcards = []WildcardRead{{Pattern: "STRIPE_*"}}
	if _, err := Derive(r); err == nil || !strings.Contains(err.Error(), "needs a reason") {
		t.Fatalf("want a missing-reason error, got %v", err)
	}

	r.Bindings[0].Wildcards = []WildcardRead{{Pattern: "STRIPE_*", Reason: "tenants add\nkeys at runtime"}}
	doc, err := Derive(r)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range doc.Policies {
		if strings.Contains(p.HCL(), `STRIPE_*" {`) {
			found = true
			if !strings.Contains(p.HCL(), "# wildcard read: tenants add keys at runtime\n") {
				t.Errorf("reason not recorded as a one-line comment:\n%s", p.HCL())
			}
		}
	}
	if !found {
		t.Error("wildcard rule missing from the policy")
	}
}

func TestDerive_Rejects(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Request)
		want string
	}{
		{"bad name", func(r *Request) { r.Bindings[0].Names = []string{"has/slash"} }, "not a valid secret name"},
		{"glob as name", func(r *Request) { r.Bindings[0].Names = []string{"*"} }, "not a valid secret name"},
		{"traversal in env", func(r *Request) { r.Scope.Env = ".." }, "not a plain path segment"},
		{"slash in org", func(r *Request) { r.Scope.Org = "a/b" }, "not a plain path segment"},
		{"glob in env", func(r *Request) { r.Scope.Env = "*" }, "not a plain path segment"},
		{"glob in pattern middle", func(r *Request) {
			r.Bindings[0].Wildcards = []WildcardRead{{Pattern: "A*B", Reason: "r"}}
		}, "ending in one"},
		{"slash in pattern", func(r *Request) {
			r.Bindings[0].Wildcards = []WildcardRead{{Pattern: "../*", Reason: "r"}}
		}, "ending in one"},
		{"unknown cluster", func(r *Request) { r.Bindings[0].Cluster = "nope" }, "cluster is not declared"},
		{"bad namespace", func(r *Request) { r.Bindings[0].Namespace = "Bad_NS" }, "not a DNS label"},
		{"duplicate binding", func(r *Request) { r.Bindings = append(r.Bindings, r.Bindings[0]) }, "declared twice"},
		{"empty binding", func(r *Request) { r.Bindings[0].Names = nil }, "reads no names"},
		{"no audience", func(r *Request) { r.Clusters[0].Audience = "" }, "needs an audience"},
		{"duplicate cluster", func(r *Request) { r.Clusters = append(r.Clusters, r.Clusters[0]) }, "declared twice"},
		{"bad cluster id", func(r *Request) { r.Clusters[0].ID = "a/b" }, "not a plain identifier"},
		{"both trust modes", func(r *Request) {
			r.Clusters[0].Trust = Trust{OIDCDiscoveryURL: "https://x", PublicKeys: []string{"k"}}
		}, "choose one"},
		{"bad service account", func(r *Request) { r.ServiceAccount = "A:b" }, "not a DNS label"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRequest()
			tc.edit(&r)
			_, err := Derive(r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDerive_ReportsEveryProblem(t *testing.T) {
	r := baseRequest()
	r.Bindings[0].Names = []string{"bad name", "also/bad"}
	_, err := Derive(r)
	if err == nil || !strings.Contains(err.Error(), "bad name") || !strings.Contains(err.Error(), "also/bad") {
		t.Fatalf("want both names reported, got %v", err)
	}
}

func TestWidenThenNarrow(t *testing.T) {
	old := baseRequest()
	next := baseRequest()
	next.Bindings = []Binding{
		{Cluster: "hub", Namespace: "control-plane-prod", Names: []string{"DB_URL", "NEW_KEY"}},
	}

	wide, err := Widen(old, next)
	if err != nil {
		t.Fatal(err)
	}
	wideDoc, err := Derive(wide)
	if err != nil {
		t.Fatal(err)
	}
	oldDoc, _ := Derive(old)
	nextDoc, _ := Derive(next)

	paths := func(d Document, ns string) map[string]bool {
		out := map[string]bool{}
		for _, r := range d.Roles {
			if strings.HasSuffix(strings.TrimSuffix(r.BoundSubject, ":reliant-secrets"), ":"+ns) {
				for _, p := range d.Policies {
					if p.Name == r.Name {
						for _, rule := range p.Rules {
							out[rule.Path[strings.LastIndex(rule.Path, "/")+1:]] = true
						}
					}
				}
			}
		}
		return out
	}
	got := paths(wideDoc, "control-plane-prod")
	for _, n := range []string{"STRIPE_SECRET_KEY", "DB_URL", "NEW_KEY"} {
		if !got[n] {
			t.Errorf("widened policy lacks %s: %v", n, got)
		}
	}
	if len(paths(wideDoc, "env-abc")) != 1 {
		t.Error("widening dropped a namespace that only the old shape had")
	}
	if n := paths(nextDoc, "control-plane-prod"); n["STRIPE_SECRET_KEY"] || !n["NEW_KEY"] {
		t.Errorf("narrowed policy = %v", n)
	}

	gone := Removed(oldDoc, nextDoc)
	if len(gone.Roles) != 1 || gone.Roles[0].Mount != "jwt-daemon" || len(gone.Policies) != 1 || len(gone.Mounts) != 0 {
		t.Errorf("Removed = %+v, want the daemon namespace's role and policy, and no mount (the cluster is still declared)", gone)
	}
	next.Clusters = next.Clusters[:1]
	dropped, err := Derive(next)
	if err != nil {
		t.Fatal(err)
	}
	if g := Removed(oldDoc, dropped); len(g.Mounts) != 1 || g.Mounts[0].Cluster != "daemon" {
		t.Errorf("Removed mounts = %+v, want the daemon mount", g.Mounts)
	}
	if again := Removed(nextDoc, nextDoc); len(again.Roles)+len(again.Policies)+len(again.Mounts) != 0 {
		t.Errorf("Removed of equal documents = %+v", again)
	}
}

func TestWiden_Rejects(t *testing.T) {
	a, b := baseRequest(), baseRequest()
	b.Scope.Env = "other"
	if _, err := Widen(a, b); err == nil {
		t.Error("Widen across envs must fail")
	}
	b = baseRequest()
	b.Clusters[0].Audience = "https://changed"
	if _, err := Widen(a, b); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Errorf("Widen with a changed cluster registration = %v", err)
	}
}

func TestMountPayloads(t *testing.T) {
	m := Mount{Cluster: "hub", Trust: Trust{OIDCDiscoveryURL: "https://issuer.example"}}
	if m.Name() != "jwt-hub" || m.EnablePath() != "sys/auth/jwt-hub" || m.ConfigPath() != "auth/jwt-hub/config" {
		t.Errorf("paths: %s %s %s", m.Name(), m.EnablePath(), m.ConfigPath())
	}
	if got := m.ConfigPayload(); !reflect.DeepEqual(got, map[string]any{"oidc_discovery_url": "https://issuer.example"}) {
		t.Errorf("oidc config = %v", got)
	}
	keys := Mount{Cluster: "x", Trust: Trust{PublicKeys: []string{"PEM"}, BoundIssuer: "iss"}}
	if got := keys.ConfigPayload(); !reflect.DeepEqual(got, map[string]any{"jwt_validation_pubkeys": []string{"PEM"}, "bound_issuer": "iss"}) {
		t.Errorf("key config = %v", got)
	}
	if (Mount{Cluster: "x"}).ConfigPayload() != nil {
		t.Error("a mount with no Trust must emit no config")
	}
}

func TestLocalRoot(t *testing.T) {
	s := Scope{Root: "local", Org: "p", Env: "dev"}
	if got := s.SecretPath("A"); got != "secret/data/local/p/dev/A" {
		t.Errorf("path = %s", got)
	}
}
