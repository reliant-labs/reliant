package openbao

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

const (
	testOrg = "demo"
	testEnv = "prod"
)

func raftSpec() Spec {
	return Spec{
		Name: "openbao", Namespace: "openbao", Image: "quay.io/openbao/openbao:2.6.2",
		Replicas: 3, Owner: "demo.prod",
		Storage:        Storage{Type: "raft", Raft: &Raft{SizeGiB: 10}},
		Listeners:      []Listener{{Port: 8200, TLS: true}},
		TLS:            TLS{CAConfigMap: "openbao-ca", Duration: "8760h"},
		Seal:           Seal{KeyID: "prod-1"},
		RecoverySecret: "openbao-recovery",
		KV:             KV{Mount: "secret", MaxVersions: 10},
		HA:             HA{PDB: true, ZoneSpread: true},
		Audit:          Audit{Stdout: true, File: true, FilePath: "/openbao/audit/audit.log"},
		Writer:         Writer{Type: "service_account", ServiceAccount: "forge-secret-writer"},
		Access: Access{
			Org: testOrg, Env: testEnv, SelfCluster: "local",
			Clusters: []AccessCluster{{ID: "local", Audience: "https://openbao.openbao.svc:8200"}},
		},
	}
}

func postgresSpec() Spec {
	s := raftSpec()
	s.Replicas = 2
	s.Storage = Storage{Type: "postgres", Postgres: &Postgres{
		CloudSQLInstance: "acme:us-central1:pg", GCPServiceAccount: "openbao@acme.iam.gserviceaccount.com",
		Port: 5432, Database: "openbao", Role: "openbao", MaxParallel: 4, PrivateIP: true,
	}}
	s.CloudSQLProxyImage = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
	return s
}

func TestValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*Spec)
		want string
	}{
		"even raft":      {func(s *Spec) { s.Replicas = 2 }, "odd number of voters"},
		"plaintext only": {func(s *Spec) { s.Listeners = []Listener{{Port: 8200}} }, "states no transitionalReason"},
		"no tls at all": {func(s *Spec) {
			s.Listeners = []Listener{{Port: 8200, TransitionalReason: "x"}}
		}, "no TLS listener"},
		"first listener plaintext": {func(s *Spec) {
			s.Listeners = []Listener{{Port: 8100, TransitionalReason: "x"}, {Port: 8200, TLS: true}}
		}, "must be the TLS one"},
		"byo tls without ca": {func(s *Spec) { s.TLS = TLS{Secret: "mine"} }, "needs tls.caConfigMap"},
		"no audit":           {func(s *Spec) { s.Audit = Audit{} }, "no audit trail"},
		"cluster port taken": {func(s *Spec) { s.Listeners = []Listener{{Port: 8201, TLS: true}} }, "cluster port"},
		"wildcard read": {func(s *Spec) {
			s.Consumers = []Consumer{{Name: "deployer", Workload: "deployer", Grants: []Grant{{Path: "secret/data/envs/*", Capabilities: []string{"read"}}}}}
		}, "wildcardReadReason"},
		"sudo grant": {func(s *Spec) {
			s.Consumers = []Consumer{{Name: "x", Workload: "x", Grants: []Grant{{Path: "sys/x", Capabilities: []string{"sudo"}}}}}
		}, "not grantable"},
		"idle consumer": {func(s *Spec) { s.Consumers = []Consumer{{Name: "x", Workload: "x"}} }, "no grants and is not delegated"},
		"cloudsql no wi": {func(s *Spec) {
			*s = postgresSpec()
			s.Storage.Postgres.GCPServiceAccount = ""
		}, "gcpServiceAccount"},
	} {
		t.Run(name, func(t *testing.T) {
			s := raftSpec()
			tc.mut(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
	if err := raftSpec().Validate(); err != nil {
		t.Fatalf("the default spec must validate: %v", err)
	}
	if err := postgresSpec().Validate(); err != nil {
		t.Fatalf("the postgres spec must validate: %v", err)
	}
}

func TestWildcardReadWithReasonIsAllowed(t *testing.T) {
	s := raftSpec()
	s.Consumers = []Consumer{{Name: "deployer", Workload: "deployer", Grants: []Grant{{
		Path: "secret/data/tenants/*", Capabilities: []string{"read"}, WildcardReadReason: "until per-env grants land",
	}}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	doc, err := BuildAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	p := findPolicy(t, doc, "consumer-deployer")
	if p.Rules[0].Reason == "" {
		t.Fatal("the reason must travel into the policy")
	}
	if !strings.Contains(hcl(p), "# wildcard read: until per-env grants land") {
		t.Fatalf("policy HCL drops the reason:\n%s", hcl(p))
	}
}

func findPolicy(t *testing.T, d AccessDoc, name string) Policy {
	t.Helper()
	for _, p := range d.Policies {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no policy %q in %v", name, names(d))
	return Policy{}
}

func names(d AccessDoc) []string {
	var n []string
	for _, p := range d.Policies {
		n = append(n, p.Name)
	}
	return n
}

func hcl(p Policy) string {
	var rules []secretaccess.Rule
	for _, r := range p.Rules {
		rules = append(rules, secretaccess.Rule{Path: r.Path, Capabilities: r.Capabilities, WildcardReadReason: r.Reason})
	}
	return secretaccess.Policy{Name: p.Name, Rules: rules}.HCL()
}

func accessSpec() Spec {
	s := raftSpec()
	s.Access.Clusters = []AccessCluster{
		{ID: "local", Audience: "https://openbao.openbao.svc:8200"},
		{ID: "daemon", Audience: "https://secrets.example.com/c/daemon", OIDCDiscoveryURL: "https://container.googleapis.com/v1/projects/p/locations/l/clusters/c"},
	}
	s.Access.Bindings = []Binding{
		{Cluster: "local", Namespace: "app", Names: []string{"B_NAME", "A_NAME"}},
		{Cluster: "daemon", Namespace: "env-1", Names: []string{"TOKEN"}},
	}
	s.Consumers = []Consumer{
		{Name: "control-plane-service", Workload: "admin-server", Namespace: "cp", Grants: []Grant{{Path: "secret/data/*", Capabilities: []string{"create", "update"}}}},
		{Name: "access-admin", Workload: "workspace-controller", Namespace: "cp", Delegated: true},
	}
	return s
}

func TestBuildAccess_IsDerivedFromSecretaccess(t *testing.T) {
	s := accessSpec()
	doc, err := BuildAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	want, err := secretaccess.Derive(secretaccess.Request{
		Scope:    secretaccess.Scope{Org: testOrg, Env: testEnv},
		Clusters: []secretaccess.Cluster{{ID: "daemon", Audience: "https://secrets.example.com/c/daemon", Trust: secretaccess.Trust{OIDCDiscoveryURL: "https://container.googleapis.com/v1/projects/p/locations/l/clusters/c"}}, {ID: "local", Audience: "https://openbao.openbao.svc:8200"}},
		Bindings: []secretaccess.Binding{{Cluster: "local", Namespace: "app", Names: []string{"A_NAME", "B_NAME"}}, {Cluster: "daemon", Namespace: "env-1", Names: []string{"TOKEN"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, wp := range want.Policies {
		got := findPolicy(t, doc, wp.Name)
		if hcl(got) != wp.HCL() {
			t.Errorf("policy %s differs from pkg/secretaccess:\n got %s\nwant %s", wp.Name, hcl(got), wp.HCL())
		}
		if len(got.Rules) > 0 && strings.Contains(got.Rules[0].Path, "*") {
			t.Errorf("a derived policy grants a glob: %s", got.Rules[0].Path)
		}
	}
	for _, wr := range want.Roles {
		var found bool
		for _, r := range doc.Roles {
			if r.Mount == wr.Mount && r.Name == wr.Name {
				found = true
				a, _ := json.Marshal(r.Payload)
				b, _ := json.Marshal(wr.Payload())
				if string(a) != string(b) {
					t.Errorf("role %s payload differs:\n%s\n%s", wr.Name, a, b)
				}
			}
		}
		if !found {
			t.Errorf("role %s/%s missing", wr.Mount, wr.Name)
		}
	}
}

func TestBuildAccess_SelfMountTrustAndFixedPrincipals(t *testing.T) {
	doc, err := BuildAccess(accessSpec())
	if err != nil {
		t.Fatal(err)
	}
	trust := map[string]string{}
	for _, m := range doc.Mounts {
		trust[m.Name] = m.Trust
	}
	if trust["jwt-local"] != "self" || trust["jwt-daemon"] != "config" {
		t.Fatalf("trust = %v: the self cluster reads its own JWKS, a remote one its declared issuer", trust)
	}

	roles := map[string]Role{}
	for _, r := range doc.Roles {
		roles[r.Mount+"/"+r.Name] = r
	}
	conv := roles["jwt-local/"+RoleConverge]
	if conv.Payload["bound_subject"] != "system:serviceaccount:openbao:openbao-converge" {
		t.Errorf("converge role subject = %v", conv.Payload["bound_subject"])
	}
	if aud := conv.Payload["bound_audiences"].([]string); aud[0] != ConvergeAudience {
		t.Errorf("converge audience = %v", aud)
	}
	w := roles["jwt-local/"+RoleWriter]
	if w.Payload["token_type"] != "batch" || w.Payload["bound_subject"] != "system:serviceaccount:openbao:forge-secret-writer" {
		t.Errorf("writer role = %v", w.Payload)
	}

	admin := roles["jwt-local/consumer-access-admin"]
	if pol := admin.Payload["token_policies"].([]string); len(pol) != 1 || pol[0] != PolicyAccessAdmin {
		t.Errorf("a delegated consumer with no grants gets exactly access-admin, got %v", pol)
	}
	svc := roles["jwt-local/consumer-control-plane-service"]
	if pol := svc.Payload["token_policies"].([]string); len(pol) != 1 || pol[0] != "consumer-control-plane-service" {
		t.Errorf("consumer policies = %v", pol)
	}
	if svc.Payload["bound_subject"] != "system:serviceaccount:cp:admin-server" {
		t.Errorf("consumer subject = %v", svc.Payload["bound_subject"])
	}
}

func TestAccessAdminHasNoSecretCapability(t *testing.T) {
	doc, err := BuildAccess(accessSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{PolicyAccessAdmin, PolicyConverge, PolicyWriter} {
		for _, r := range findPolicy(t, doc, name).Rules {
			for _, c := range r.Capabilities {
				readsData := strings.HasPrefix(r.Path, "secret/data") && (c == "read")
				if readsData {
					t.Errorf("%s can read values: %s %v", name, r.Path, r.Capabilities)
				}
			}
		}
	}
	for _, name := range []string{PolicyAccessAdmin, PolicyConverge} {
		for _, r := range findPolicy(t, doc, name).Rules {
			if strings.HasPrefix(r.Path, "secret/data") || strings.HasPrefix(r.Path, "secret/metadata") {
				t.Errorf("%s has a capability under the KV data: %s", name, r.Path)
			}
		}
	}
}

func TestBuildAccess_Refusals(t *testing.T) {
	s := accessSpec()
	s.Access.Clusters[1].OIDCDiscoveryURL = ""
	if _, err := BuildAccess(s); err == nil || !strings.Contains(err.Error(), "forge cluster connect") {
		t.Fatalf("a remote cluster with no issuer must be refused naming the escape, got %v", err)
	}
	s = accessSpec()
	s.Access.SelfCluster = "nowhere"
	if _, err := BuildAccess(s); err == nil || !strings.Contains(err.Error(), "selfCluster") {
		t.Fatalf("got %v", err)
	}
	s = accessSpec()
	s.Consumers[0].Name = "a"
	s.Consumers[1].Name = "a"
	if _, err := BuildAccess(s); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("two consumers deriving one role must be refused, got %v", err)
	}
}

func TestBuildAccess_Deterministic(t *testing.T) {
	a, _ := BuildAccess(accessSpec())
	s := accessSpec()
	s.Access.Bindings[0].Names = []string{"A_NAME", "B_NAME"}
	s.Access.Bindings[0], s.Access.Bindings[1] = s.Access.Bindings[1], s.Access.Bindings[0]
	b, _ := BuildAccess(s)
	ja, _ := a.JSON()
	jb, _ := b.JSON()
	if string(ja) != string(jb) || a.Hash() != b.Hash() {
		t.Fatal("the same spec in another order must give the same document")
	}
}

func TestWidenThenNarrow(t *testing.T) {
	oldS := accessSpec()
	newS := accessSpec()
	newS.Access.Bindings = []Binding{
		{Cluster: "local", Namespace: "app", Names: []string{"B_NAME", "C_NAME"}},
	}
	// A binding that leaves the shape loses its role and policy, but its
	// cluster is still registered, so the mount stays.
	stay, _ := BuildAccess(newS)
	oldAll, _ := BuildAccess(oldS)
	if g := Removed(oldAll, stay); len(g.Mounts) != 0 || len(g.Roles) != 1 || len(g.Policies) != 1 {
		t.Fatalf("dropping a binding removes its role and policy only: mounts %d roles %d policies %d", len(g.Mounts), len(g.Roles), len(g.Policies))
	}
	// Dropping the cluster too removes the mount.
	newS.Access.Clusters = newS.Access.Clusters[:1]
	oldD, _ := BuildAccess(oldS)
	newD, _ := BuildAccess(newS)

	wide := Widen(oldD, newD)
	pol := findPolicy(t, wide, secretaccess.Scope{Org: testOrg, Env: testEnv}.PolicyName("local", "app"))
	var paths []string
	for _, r := range pol.Rules {
		paths = append(paths, r.Path[strings.LastIndex(r.Path, "/")+1:])
	}
	if strings.Join(paths, ",") != "A_NAME,B_NAME,C_NAME" {
		t.Fatalf("widened policy reads %v, want the union of old and new", paths)
	}
	var daemonRole bool
	for _, r := range wide.Roles {
		if r.Mount == "jwt-daemon" {
			daemonRole = true
		}
	}
	if !daemonRole {
		t.Fatal("widening must keep a namespace the new shape dropped: its workloads may still be rolling")
	}

	gone := Removed(oldD, newD)
	if len(gone.Mounts) != 1 || gone.Mounts[0].Name != "jwt-daemon" {
		t.Fatalf("removed mounts = %v", gone.Mounts)
	}
	if len(gone.Roles) != 1 || gone.Roles[0].Mount != "jwt-daemon" {
		t.Fatalf("removed roles = %v", gone.Roles)
	}
	if len(gone.Policies) != 1 {
		t.Fatalf("removed policies = %v", names(AccessDoc{Policies: gone.Policies}))
	}
}

func TestRemoved_NeverRemovesTheSelfMount(t *testing.T) {
	oldD, _ := BuildAccess(accessSpec())
	next := oldD
	next.Mounts = nil
	if g := Removed(oldD, next); len(g.Mounts) != 1 || g.Mounts[0].Name != "jwt-daemon" {
		for _, m := range g.Mounts {
			if m.Name == "jwt-local" {
				t.Fatal("the self mount holds the converge role and must never be removed")
			}
		}
	}
}

func TestRender_Raft(t *testing.T) {
	objs, err := Render(raftSpec())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, o := range objs {
		kinds[o.GetKind()]++
	}
	for kind, n := range map[string]int{
		"StatefulSet": 1, "Service": 2, "PodDisruptionBudget": 1, "NetworkPolicy": 2, "Job": 2,
		"Issuer": 2, "Certificate": 2, "ServiceAccount": 3,
	} {
		if kinds[kind] != n {
			t.Errorf("%s: got %d, want %d (all: %v)", kind, kinds[kind], n, kinds)
		}
	}
	if kinds["Deployment"] != 0 {
		t.Error("Raft is a StatefulSet")
	}
	cfg := configOf(t, objs)
	for i := 0; i < 3; i++ {
		want := "leader_api_addr = \"https://openbao-" + string(rune('0'+i)) + ".openbao-internal.openbao.svc:8200\""
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks retry_join to pod %d:\n%s", i, cfg)
		}
	}
	for _, want := range []string{`storage "raft"`, `tls_cert_file`, `seal "static"`, `current_key = "env://BAO_STATIC_KEY"`, `audit "file" "to-stdout"`, `audit "file" "to-disk"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q", want)
		}
	}
	if strings.Contains(cfg, "tls_disable") {
		t.Error("the default config must not disable TLS")
	}
}

func TestRender_Postgres(t *testing.T) {
	objs, err := Render(postgresSpec())
	if err != nil {
		t.Fatal(err)
	}
	var dep map[string]any
	for _, o := range objs {
		if o.GetKind() == "Deployment" {
			dep = o.Object
		}
		if o.GetKind() == "StatefulSet" {
			t.Fatal("Postgres storage is a Deployment")
		}
	}
	if dep == nil {
		t.Fatal("no Deployment")
	}
	raw, _ := json.Marshal(dep)
	for _, want := range []string{"cloud-sql-proxy", "--private-ip", "acme:us-central1:pg", "BAO_PG_CONNECTION_URL", `"restartPolicy":"Always"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("deployment lacks %s", want)
		}
	}
	sa := findObj(t, objs, "ServiceAccount", "openbao")
	if sa.GetAnnotations()["iam.gke.io/gcp-service-account"] != "openbao@acme.iam.gserviceaccount.com" {
		t.Errorf("the server's ServiceAccount must carry the Workload Identity annotation: %v", sa.GetAnnotations())
	}
	cfg := configOf(t, objs)
	if !strings.Contains(cfg, `storage "postgresql"`) || !strings.Contains(cfg, `max_parallel = "4"`) || strings.Contains(cfg, "password") {
		t.Errorf("postgres config:\n%s", cfg)
	}
}

func TestRender_TransitionalListenerIsPlaintextAndNamed(t *testing.T) {
	s := raftSpec()
	s.Listeners = append(s.Listeners, Listener{Port: 8300, TransitionalReason: "clients move to https by 2026-12-01"})
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configOf(t, objs)
	if strings.Count(cfg, "tls_disable = true") != 1 || strings.Count(cfg, "tls_cert_file") != 1 {
		t.Errorf("exactly one listener is plaintext:\n%s", cfg)
	}
	svc := findObj(t, objs, "Service", "openbao")
	raw, _ := json.Marshal(svc.Object)
	if !strings.Contains(string(raw), "8300") {
		t.Error("the Service must expose the transitional port")
	}
}

func spreadOf(t *testing.T, objs []*unstructuredT, key string) map[string]any {
	t.Helper()
	sts := findObj(t, objs, "StatefulSet", "openbao")
	cs, _, _ := unstructured.NestedSlice(sts.Object, "spec", "template", "spec", "topologySpreadConstraints")
	for _, c := range cs {
		m := c.(map[string]any)
		if m["topologyKey"] == key {
			return m
		}
	}
	return nil
}

// An HA store (replicas >= 2) must not co-locate its replicas: the SC7 prod
// adoption put both on one node under a preference. Node spread is a hard
// constraint by default; zone spread stays best effort.
//
// Mutation that fails it: drop the DoNotSchedule branch in render.go.
func TestRender_HAStoreRequiresNodeSpreadByDefault(t *testing.T) {
	for name, s := range map[string]Spec{"raft": raftSpec(), "postgres": postgresSpec()} {
		objs, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		kind := "StatefulSet"
		if name == "postgres" {
			kind = "Deployment"
		}
		obj := findObj(t, objs, kind, "openbao")
		cs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "topologySpreadConstraints")
		by := map[string]map[string]any{}
		for _, c := range cs {
			m := c.(map[string]any)
			by[m["topologyKey"].(string)] = m
		}
		node, zone := by["kubernetes.io/hostname"], by["topology.kubernetes.io/zone"]
		if node == nil || node["whenUnsatisfiable"] != "DoNotSchedule" {
			t.Errorf("%s: node spread must be DoNotSchedule by default, got %v", name, node)
		}
		if node != nil && node["maxSkew"] != int64(1) {
			t.Errorf("%s: node spread maxSkew = %v, want 1", name, node["maxSkew"])
		}
		if zone == nil || zone["whenUnsatisfiable"] != "ScheduleAnyway" {
			t.Errorf("%s: zone spread must stay best effort, got %v", name, zone)
		}
	}
}

func TestRender_PreferredNodeSpreadIsAnExplicitOverrideWithAReason(t *testing.T) {
	s := raftSpec()
	s.HA.NodeSpread = NodeSpreadPreferred
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "nodeSpreadReason") {
		t.Fatalf("preferred without a reason must be refused, got %v", err)
	}
	s.HA.NodeSpreadReason = "two-node staging pool"
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := spreadOf(t, objs, "kubernetes.io/hostname"); got == nil || got["whenUnsatisfiable"] != "ScheduleAnyway" {
		t.Errorf("an overridden node spread is best effort, got %v", got)
	}

	r := raftSpec()
	r.HA.NodeSpreadReason = "no override to excuse"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "excuses a preferred spread") {
		t.Errorf("a reason on a required spread must be refused, got %v", err)
	}
	r = raftSpec()
	r.HA.NodeSpread = "sometimes"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "must be required or preferred") {
		t.Errorf("an unknown nodeSpread must be refused, got %v", err)
	}
}

func TestRender_BYOTLSRendersNoCertManagerObjects(t *testing.T) {
	s := raftSpec()
	s.TLS = TLS{Secret: "my-tls", CAConfigMap: "my-ca"}
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() == "Issuer" || o.GetKind() == "Certificate" {
			t.Errorf("BYO TLS rendered %s/%s", o.GetKind(), o.GetName())
		}
	}
	raw, _ := json.Marshal(findObj(t, objs, "StatefulSet", "openbao").Object)
	if !strings.Contains(string(raw), `"name":"my-tls"`) || !strings.Contains(string(raw), `"name":"my-ca"`) {
		t.Error("the pods must mount the BYO certificate and its CA ConfigMap")
	}
}

func TestRender_ReplicaOneHasNoPDBOrSpread(t *testing.T) {
	s := raftSpec()
	s.Replicas = 1
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() == "PodDisruptionBudget" {
			t.Error("a single replica must not carry a PDB: it would make its node undrainable")
		}
	}
	raw, _ := json.Marshal(findObj(t, objs, "StatefulSet", "openbao").Object)
	if strings.Contains(string(raw), "topologySpreadConstraints") {
		t.Error("a single replica needs no spread")
	}
}

func TestRender_EveryObjectIsOwnedAndManaged(t *testing.T) {
	objs, _ := Render(raftSpec())
	for _, o := range objs {
		l := o.GetLabels()
		if l[LabelOwner] != "demo.prod" || l[LabelManagedBy] != "forge" || l[LabelComponent] != "openbao" {
			t.Errorf("%s/%s labels = %v", o.GetKind(), o.GetName(), l)
		}
		if o.GetNamespace() != "openbao" {
			t.Errorf("%s/%s namespace = %q", o.GetKind(), o.GetName(), o.GetNamespace())
		}
	}
}

func TestRender_NetworkPolicyAdmitsOnlyDeclaredPeers(t *testing.T) {
	s := accessSpec()
	s.IngressNamespaces = []string{"app", "external-secrets", "app"}
	s.IngressFrom = []Peer{{NamespaceLabels: map[string]string{"gateway": "public"}}}
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	np := findObj(t, objs, "NetworkPolicy", "openbao")
	raw, _ := json.Marshal(np.Object)
	text := string(raw)
	for _, want := range []string{"admin-server", `"kubernetes.io/metadata.name":"app"`, `"kubernetes.io/metadata.name":"external-secrets"`, `"gateway":"public"`, "openbao-converge", "8201"} {
		if !strings.Contains(text, want) {
			t.Errorf("network policy lacks %s:\n%s", want, text)
		}
	}
	if strings.Count(text, `"kubernetes.io/metadata.name":"app"`) != 1 {
		t.Error("a namespace is admitted once")
	}
}

func TestConvergeJobs_ContentAddressedAndPhased(t *testing.T) {
	objs, err := Render(accessSpec())
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]string{}
	for _, o := range objs {
		if o.GetKind() != "Job" {
			continue
		}
		jobs[o.GetLabels()[LabelJobName]] = o.GetName() + "|" + o.GetAnnotations()[AnnotationDeployPhase]
		if _, has := o.Object["spec"].(map[string]any)["ttlSecondsAfterFinished"]; has {
			t.Errorf("%s sets a TTL: a reconciler would re-run it forever", o.GetName())
		}
		if !strings.Contains(o.GetName(), "-"+o.GetLabels()[LabelSpecHash]) {
			t.Errorf("%s is not named by its hash", o.GetName())
		}
	}
	if !strings.HasSuffix(jobs["openbao-widen"], "|pre-rollout") || !strings.HasSuffix(jobs["openbao-converge"], "|post-rollout") {
		t.Fatalf("phases = %v", jobs)
	}

	// Change the script's input: both Jobs get a new name, so a completed Job
	// never silently skips the change.
	s := accessSpec()
	s.Access.Bindings[0].Names = append(s.Access.Bindings[0].Names, "C_NAME")
	objs2, _ := Render(s)
	for _, o := range objs2 {
		if o.GetKind() == "Job" && strings.Contains(jobs[o.GetLabels()[LabelJobName]], o.GetName()+"|") {
			t.Errorf("%s did not change when the access document did", o.GetName())
		}
	}
	// The same input renders the same bytes.
	objs3, _ := Render(accessSpec())
	a, _ := json.Marshal(objs)
	b, _ := json.Marshal(objs3)
	if string(a) != string(b) {
		t.Fatal("render is not deterministic")
	}
}

func TestConvergeJob_LeastPrivilege(t *testing.T) {
	objs, _ := Render(accessSpec())
	role := findObj(t, objs, "Role", "openbao-converge")
	raw, _ := json.Marshal(role.Object)
	text := string(raw)
	if strings.Contains(text, `"delete"`) || strings.Contains(text, `"list"`) || strings.Contains(text, `"*"`) {
		t.Errorf("the converge Role may neither delete nor list, and names no wildcard: it could destroy the only recovery copy.\n%s", text)
	}
	if !strings.Contains(text, `"resourceNames":["openbao-recovery"]`) {
		t.Errorf("reading the recovery Secret must be pinned by resourceNames:\n%s", text)
	}
	job := findJob(t, objs, "openbao-converge")
	jr, _ := json.Marshal(job.Object)
	for _, want := range []string{`"audience":"openbao-converge"`, `"runAsNonRoot":true`, `"readOnlyRootFilesystem":true`, "podFailurePolicy", `"values":[64]`} {
		if !strings.Contains(string(jr), want) {
			t.Errorf("converge Job lacks %s", want)
		}
	}
	if strings.Contains(string(jr), "BAO_STATIC_KEY") || strings.Contains(string(jr), "static-key") {
		t.Error("the converge Job must never see the seal key")
	}
}

func TestConvergeJob_HookSeams(t *testing.T) {
	s := raftSpec()
	s.Hooks = Hooks{Escrow: "openbao-escrow-abc", Generate: "openbao-generate-abc"}
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(findJob(t, objs, "openbao-converge").Object)
	for _, want := range []string{"/escrow", "/generate", "openbao-escrow-abc", "openbao-generate-abc"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("hook seam %s is not mounted", want)
		}
	}
	plain, _ := Render(raftSpec())
	raw, _ = json.Marshal(findJob(t, plain, "openbao-converge").Object)
	if strings.Contains(string(raw), "/escrow") {
		t.Error("no hook is mounted unless named")
	}
}

func TestEmbeddedScriptCarriesItsSafetyProperties(t *testing.T) {
	for _, want := range []string{
		"set -euo pipefail", "uninitcode", "sys/init", "kubectl create -f -", `refuse "the store reports it is NOT initialised`,
		"X-Vault-Token", "revoke_if_root", "EXIT_REFUSED",
	} {
		if !strings.Contains(ConvergeScript, want) {
			t.Errorf("converge.sh lost %q", want)
		}
	}
	if strings.Contains(ConvergeScript, "BAO_TOKEN=") || strings.Contains(ConvergeScript, "-H \"X-Vault-Token: $") {
		t.Error("the token must travel in a header file, never on a command line")
	}
	initAt := strings.Index(ConvergeScript, "initialise\n")
	escrowAt := strings.Index(ConvergeScript, "\n  escrow\n")
	applyAt := strings.Index(ConvergeScript, "\n  apply_doc \"$NEW\"")
	if !(initAt > 0 && initAt < escrowAt && escrowAt < applyAt) {
		t.Errorf("order must be init (%d) < escrow (%d) < apply (%d)", initAt, escrowAt, applyAt)
	}
}

func TestRender_RefusesAnInvalidSpec(t *testing.T) {
	s := raftSpec()
	s.Listeners = []Listener{{Port: 8200}}
	if _, err := Render(s); err == nil || !strings.Contains(err.Error(), "transitionalReason") {
		t.Fatalf("got %v", err)
	}
}

func configOf(t *testing.T, objs []*unstructuredT) string {
	t.Helper()
	cm := findObj(t, objs, "ConfigMap", "openbao-config")
	data, _ := cm.Object["data"].(map[string]any)
	s, _ := data["config.hcl"].(string)
	if s == "" {
		t.Fatal("no config.hcl")
	}
	return s
}

func findObj(t *testing.T, objs []*unstructuredT, kind, name string) *unstructuredT {
	t.Helper()
	for _, o := range objs {
		if o.GetKind() == kind && o.GetName() == name {
			return o
		}
	}
	t.Fatalf("no %s/%s", kind, name)
	return nil
}

func findJob(t *testing.T, objs []*unstructuredT, handle string) *unstructuredT {
	t.Helper()
	for _, o := range objs {
		if o.GetKind() == "Job" && o.GetLabels()[LabelJobName] == handle {
			return o
		}
	}
	t.Fatalf("no Job %s", handle)
	return nil
}

type unstructuredT = unstructured.Unstructured

// P6 (cert-manager dependency): the server certificate must answer to every
// name a Raft peer dials, or retry_join fails TLS verification on a real
// cluster. The smoke test proves it against live nodes; this pins it without.
func TestCertificateCoversEveryRaftPeer(t *testing.T) {
	s := raftSpec()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	cert := findObj(t, objs, "Certificate", "openbao-tls")
	spec, _ := cert.Object["spec"].(map[string]any)
	have := map[string]bool{}
	for _, n := range spec["dnsNames"].([]any) {
		have[n.(string)] = true
	}
	for i := 0; i < s.Replicas; i++ {
		peer := fmt.Sprintf("openbao-%d.openbao-internal.openbao.svc", i)
		covered := have[peer]
		for n := range have {
			if strings.HasPrefix(n, "*.") && strings.HasSuffix(peer, n[1:]) && strings.Count(peer, ".") == strings.Count(n, ".") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("no certificate name covers raft peer %s (have %v)", peer, have)
		}
	}
	for _, n := range []string{s.Host(), "openbao"} {
		if !have[n] {
			t.Errorf("the certificate lacks the client-facing name %s", n)
		}
	}
	ref := spec["issuerRef"].(map[string]any)
	if ref["name"] != "openbao-ca" || ref["kind"] != "Issuer" || ref["group"] != "cert-manager.io" {
		t.Errorf("the server certificate must be signed by the namespaced CA Issuer: %v", ref)
	}
	if spec["secretName"] != s.TLSSecret() {
		t.Errorf("secretName = %v", spec["secretName"])
	}
	caCert := findObj(t, objs, "Certificate", "openbao-ca")
	cs, _ := caCert.Object["spec"].(map[string]any)
	if cs["isCA"] != true || cs["secretName"] != "openbao-ca" {
		t.Errorf("CA certificate = %v", cs)
	}
}
