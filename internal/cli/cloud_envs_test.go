package cli

// Tests for the cloud-environments primitives: required --env on the secret
// commands, `secret list` answering for every provider, env-free login that
// logs into every DISTINCT declared control plane, project + derived kind on
// EnsureEnvironment, the LOCAL-env deploy refusal, and `forge env up` pulling
// a LOCAL env's secrets into memory.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// ─── fixtures ────────────────────────────────────────────────────────────────

func withDeclaredControlPlanes(t *testing.T, decls []envDeclaration) {
	t.Helper()
	prev := declaredControlPlanes
	declaredControlPlanes = func(context.Context) ([]declaredControlPlane, []string, error) {
		return distinctControlPlanes(decls), nil, nil
	}
	t.Cleanup(func() { declaredControlPlanes = prev })
}

func withProjectName(t *testing.T, name string) {
	t.Helper()
	prev := hostedProjectName
	hostedProjectName = func() string { return name }
	t.Cleanup(func() { hostedProjectName = prev })
}

func withSecretEntities(t *testing.T, e *KCLEntities) {
	t.Helper()
	prev := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return e, nil }
	t.Cleanup(func() { renderEntitiesForSecrets = prev })
}

// localEnvEntities is a LOCAL env: control plane + HostedSecrets, a host
// service declaring one secret, and a frontend declaring another.
func localEnvEntities(endpoint string) *KCLEntities {
	return &KCLEntities{
		ControlPlane:   &ControlPlaneEntity{Type: "control_plane", Endpoint: endpoint},
		SecretProvider: &SecretProviderEntity{Type: "hosted"},
		Workloads:      []WorkloadEntity{hostWL("api", withSecretRef("STRIPE_SECRET_KEY", "app-secrets", "STRIPE_SECRET_KEY"))},
	}
}

// fakeCPCaller is an in-process control plane speaking the proto3-JSON subset.
type fakeCPCaller struct {
	mu      sync.Mutex
	calls   []fakeCPCall
	envs    []map[string]any
	secrets []map[string]any
	pullErr error
	// ensureErr is what EnsureEnvironment returns instead of succeeding.
	// It exists for F-15: a server that READ the shape and refused it must
	// fail the command loudly rather than be treated like an old server
	// that ignored the field.
	ensureErr error
	ensureID  string
	// clusters is what ListClusters answers: the org's connected clusters,
	// which a declaration resolves its bindings' names against.
	clusters []map[string]any
}

type fakeCPCall struct {
	Proc string
	Body map[string]any
}

func (f *fakeCPCaller) Call(_ context.Context, proc string, req, out any) error {
	raw, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCPCall{Proc: proc, Body: body})
	f.mu.Unlock()
	var reply any
	switch proc {
	case "controlplane.v1.DeployService/ListEnvironments":
		reply = map[string]any{"environments": f.envs}
	case "controlplane.v1.DeployService/EnsureEnvironment":
		if f.ensureErr != nil {
			return f.ensureErr
		}
		reply = map[string]any{"environment": map[string]any{"id": f.ensureID, "name": "x"}, "created": true}
	case "controlplane.v1.DeployService/CutRelease":
		reply = map[string]any{"created": true}
	case "controlplane.v1.DeployService/GetRelease":
		// A release that names NO artifact is refused by releaseFromWire —
		// correctly, since it could not be promoted — so the fake answers
		// with a minimal valid one.
		reply = map[string]any{"release": map[string]any{
			"id": "rel-1", "version": "v1.0.0", "createdAt": "2026-01-01T00:00:00Z",
			"artifacts": []any{map[string]any{
				"name": "api", "kind": "oci", "mode": "shared",
				"variant": "*", "digest": "sha256:" + strings.Repeat("c", 64),
			}},
		}}
	case "controlplane.v1.DeployService/ListReleases":
		reply = map[string]any{"releases": []any{}}
	case procListClusters:
		reply = map[string]any{"clusters": f.clusters}
	case "controlplane.v1.LocalSecretService/PullSecrets":
		if f.pullErr != nil {
			return f.pullErr
		}
		reply = map[string]any{"secrets": f.secrets}
	default:
		return errors.New("unexpected procedure " + proc)
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(reply)
	return json.Unmarshal(b, out)
}

func (f *fakeCPCaller) callsTo(proc string) []fakeCPCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCPCall
	for _, c := range f.calls {
		if c.Proc == proc {
			out = append(out, c)
		}
	}
	return out
}

// ─── 1. --env is required ────────────────────────────────────────────────────

func TestSecretCommands_RequireEnvFlag(t *testing.T) {
	cases := [][]string{
		{"set", "STRIPE_KEY"},
		{"unset", "STRIPE_KEY"},
		{"list"},
		{"ensure"},
		{"migrate"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			cmd := newSecretCmd()
			cmd.SetArgs(args)
			cmd.SetIn(strings.NewReader("v"))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), "--env is required") || !strings.Contains(err.Error(), "fix: forge secret "+args[0]+" --env") {
				t.Fatalf("forge secret %v without --env: want the --env fix, got %v", args, err)
			}
		})
	}
	// The retired positional spelling is refused rather than read as a key.
	for _, args := range [][]string{{"set", "dev", "STRIPE_KEY"}, {"list", "dev"}, {"unset", "dev", "K"}} {
		cmd := newSecretCmd()
		cmd.SetArgs(append(args, "--env", "dev"))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.ExecuteContext(context.Background()); err == nil {
			t.Fatalf("forge secret %v: a positional env must be refused", args)
		}
	}
}

// ─── 2. list answers for every provider ──────────────────────────────────────

func TestSecretList_ExternalProviderReportsUnknown(t *testing.T) {
	withSecretEntities(t, &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "external"},
		Workloads:      []WorkloadEntity{hostWL("api", withSecretRef("STRIPE_SECRET_KEY", "app-secrets", "STRIPE_SECRET_KEY"))},
	})
	var buf bytes.Buffer
	if err := runSecretListJSON(context.Background(), "prod", &buf); err != nil {
		t.Fatalf("list of an external env must not refuse: %v", err)
	}
	var r secretListReport
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Provider != "external" || r.Verifiable || r.OK || len(r.Missing) != 0 {
		t.Fatalf("report = %+v, want external/unverifiable/not-ok/no missing", r)
	}
	if len(r.Secrets) != 1 || r.Secrets[0].Presence != "unknown" || r.Secrets[0].Present {
		t.Fatalf("secrets = %+v, want one with presence unknown", r.Secrets)
	}
	var text bytes.Buffer
	if err := runSecretList(context.Background(), "prod", &text); err != nil || !strings.Contains(text.String(), "UNKNOWN") {
		t.Fatalf("text list: %v\n%s", err, text.String())
	}
}

func TestSecretList_NoProviderReportsDeclarations(t *testing.T) {
	withSecretEntities(t, &KCLEntities{
		Workloads: []WorkloadEntity{hostWL("api", withSecretRef("K", "s", "K"))},
	})
	r, err := collectSecretListFacts(context.Background(), "dev")
	if err != nil {
		t.Fatalf("list with no provider must answer: %v", err)
	}
	if r.Provider != "none" || len(r.Secrets) != 1 || r.Secrets[0].Presence != "unknown" {
		t.Fatalf("report = %+v", r)
	}
}

func TestSecretList_RenderedProviderResolvesSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: acme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteSecretFile(filepath.Join(dir, "secrets", "e2e.yaml"), map[string]string{"stripe": "sk_x"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	withSecretEntities(t, &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "rendered", Secrets: []RenderedSecretEntity{{
		Name: "app",
		Keys: map[string]RenderedSecretKeyEntity{
			"lit":    {From: "literal", Value: "canary-literal-value"},
			"stripe": {From: "file"},
			"absent": {From: "file", Key: "NOPE"},
		},
	}}}})
	var buf bytes.Buffer
	if err := runSecretListJSON(context.Background(), "e2e", &buf); err != nil {
		t.Fatalf("list of a rendered env must answer: %v", err)
	}
	if strings.Contains(buf.String(), "canary-literal-value") || strings.Contains(buf.String(), "sk_x") {
		t.Fatalf("a value leaked: %s", buf.String())
	}
	var r secretListReport
	_ = json.Unmarshal(buf.Bytes(), &r)
	got := map[string]string{}
	for _, s := range r.Secrets {
		got[s.Name] = s.Presence
	}
	want := map[string]string{"app/lit": "set", "app/stripe": "set", "app/absent": "missing"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s presence = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSecretList_HostedReportsVersions(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	fake.stored["STRIPE_SECRET_KEY"] = 3
	withProjectName(t, "acme")
	e, _ := renderEntitiesForSecrets(context.Background(), "prod")
	e.Workloads = []WorkloadEntity{hostWL("api", withSecretRef("STRIPE_SECRET_KEY", "s", "STRIPE_SECRET_KEY"))}
	r, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Secrets) != 1 || r.Secrets[0].Presence != "set" || r.Secrets[0].Version != 3 || !r.OK {
		t.Fatalf("report = %+v", r)
	}
	// List is a read addressed by (project, name).
	lists := fake.callsTo("controlplane.v1.DeployService/ListEnvironments")
	if len(lists) != 1 || lists[0].Body["project"] != "acme" || lists[0].Body["search"] != "prod" {
		t.Fatalf("ListEnvironments = %+v, want project=acme search=prod", lists)
	}
}

func TestHostedSecretSet_SaysCreatedThenRotated(t *testing.T) {
	hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	var first, second bytes.Buffer
	if err := runSecretSet(context.Background(), "prod", "K", "", strings.NewReader("v1"), &first); err != nil {
		t.Fatal(err)
	}
	if err := runSecretSet(context.Background(), "prod", "K", "", strings.NewReader("v2"), &second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), "K: created v1") {
		t.Errorf("first set: %q", first.String())
	}
	if !strings.Contains(second.String(), "K: rotated to v2") {
		t.Errorf("second set: %q", second.String())
	}
}

// ─── 3. login is not env-scoped ──────────────────────────────────────────────

func TestDistinctControlPlanes_DedupesByEndpoint(t *testing.T) {
	got := distinctControlPlanes([]envDeclaration{
		{Env: "prod", Decl: &cloud.Declaration{Endpoint: "https://cp.example.com/"}},
		{Env: "dev", Decl: &cloud.Declaration{Endpoint: "https://cp.example.com"}},
		{Env: "staging", Decl: &cloud.Declaration{Endpoint: "https://other.example.com"}},
		{Env: "local", Decl: nil},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 distinct control planes, got %+v", got)
	}
	if got[0].Endpoint.URL != "https://cp.example.com" || strings.Join(got[0].Envs, ",") != "dev,prod" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Endpoint.URL != "https://other.example.com" || strings.Join(got[1].Envs, ",") != "staging" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestForgeLogin_LogsIntoEveryDeclaredControlPlane(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	withFakeBrowser(t)
	cpA := loginTestCP(t, "rlat_AAAAAAAAAAAAAAAA")
	cpB := loginTestCP(t, "rlat_BBBBBBBBBBBBBBBB")
	withDeclaredControlPlanes(t, []envDeclaration{
		{Env: "dev", Decl: &cloud.Declaration{Endpoint: cpA.URL}},
		{Env: "prod", Decl: &cloud.Declaration{Endpoint: cpA.URL + "/"}},
		{Env: "eu", Decl: &cloud.Declaration{Endpoint: cpB.URL}},
	})
	out, err := runLoginCmd(t, "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if n := strings.Count(out, "Logged in to "); n != 2 {
		t.Fatalf("want one login per DISTINCT control plane (2), got %d:\n%s", n, out)
	}
	if a, err := lookupStored(t, cpA.URL); err != nil || a.Token != "rlat_AAAAAAAAAAAAAAAA" {
		t.Fatalf("entry A: %+v %v", a, err)
	}
	if b, err := lookupStored(t, cpB.URL); err != nil || b.Token != "rlat_BBBBBBBBBBBBBBBB" {
		t.Fatalf("entry B: %+v %v", b, err)
	}
	if !strings.Contains(out, "Declared by: dev, prod") {
		t.Errorf("summary should name the envs sharing an endpoint:\n%s", out)
	}

	// --token with several control planes is ambiguous and refused.
	if _, err := runLoginCmd(t, "login", "--token", "rlat_X", "--no-verify"); err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Fatalf("--token across 2 control planes must be refused with the --endpoint fix, got %v", err)
	}

	// logout with no flag forgets every declared control plane.
	if out, err := runLoginCmd(t, "logout"); err != nil || strings.Count(out, "Logged out of") != 2 {
		t.Fatalf("logout: %v\n%s", err, out)
	}
}

func TestResolveCredential_HintIsBareLogin(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "")
	_, err := cloud.ResolveCredential("", cloud.Endpoint{Env: "prod", URL: "https://cp.example.com", TokenEnv: "FORGE_CONTROL_PLANE_TOKEN"})
	if err == nil || strings.Contains(err.Error(), "forge login prod") || !strings.Contains(err.Error(), "forge login ") {
		t.Fatalf("the missing-credential hint must be an env-free `forge login`, got %v", err)
	}
}

// ─── 4. project + kind ───────────────────────────────────────────────────────

func TestHostedEnvKindOf(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp"}
	backend := hostedWL("api")
	host := hostWL("api")
	onCluster := clusterWL("search", "k3d-x", "ns")
	cases := []struct {
		name string
		e    *KCLEntities
		want deploytarget.HostedEnvKind
		dest string
	}{
		{"no control plane", &KCLEntities{Workloads: []WorkloadEntity{host}}, "", destinationHost},
		{"hosted workload", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{backend}}, deploytarget.HostedEnvPersistent, destinationHosted},
		{"hosted frontend", &KCLEntities{ControlPlane: cp, Frontends: []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}}}, deploytarget.HostedEnvPersistent, destinationHosted},
		{"hosted database", &KCLEntities{ControlPlane: cp, Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted}}}, deploytarget.HostedEnvPersistent, destinationHosted},
		{"host only", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}}, deploytarget.HostedEnvLocal, destinationHost},
		{"host + compose", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host, composeWL("pg", "docker-compose.yml")}}, deploytarget.HostedEnvLocal, destinationMixed},
		// Hosting is per workload: one hosted and one cluster workload in
		// one env is a PERSISTENT env whose destination is mixed.
		{"hosted + cluster", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{backend, onCluster}}, deploytarget.HostedEnvPersistent, destinationMixed},
		// The SELF_MANAGED split: a control plane, nothing hosted, something
		// on a cluster the author operates. Classifying these LOCAL made a
		// cluster env's secrets pullable.
		{"cluster only", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{onCluster}}, deploytarget.HostedEnvSelfManaged, destinationCluster},
		{"host + cluster", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host, onCluster}}, deploytarget.HostedEnvSelfManaged, destinationMixed},
		{"cluster database only", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}, Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeCluster}}}, deploytarget.HostedEnvSelfManaged, destinationMixed},
		// A support cluster_target alone carries a Namespace and gateways
		// for workloads that still run on this machine: still LOCAL.
		{"host + support cluster_target", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}, ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-x"}}, deploytarget.HostedEnvLocal, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hostedEnvKindOf(c.e); got != c.want {
				t.Errorf("kind = %q, want %q", got, c.want)
			}
			if c.dest == "" {
				return
			}
			if got := destinationOf(c.e); got != c.dest {
				t.Errorf("destination = %q, want %q", got, c.dest)
			}
		})
	}
}

// F-12: a control-plane env whose workloads run on a cluster the author
// operates is SELF_MANAGED, and its secrets are write-only. `forge env up`
// must never pull its values, and armed values must never be served to it —
// before the kind split it was classified LOCAL and both happened.
func TestSelfManagedEnv_NeverPullsSecrets(t *testing.T) {
	e := localEnvEntities("https://cp.example.com")
	e.Workloads = append(e.Workloads, clusterWL("search", "k3d-x", "ns"))
	if got := hostedEnvKindOf(e); got != deploytarget.HostedEnvSelfManaged {
		t.Fatalf("kind = %q, want self-managed", got)
	}
	prev := pullLocalSecretsFor
	t.Cleanup(func() { pullLocalSecretsFor = prev; disarmPulledSecrets() })
	pullLocalSecretsFor = func(context.Context, string, *KCLEntities) (map[string]string, error) {
		t.Fatal("a self-managed env's secret values were pulled")
		return nil, nil
	}
	if err := armLocalSecretsForUp(context.Background(), "prod-sim", e, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	armPulledSecrets("prod-sim", map[string]string{"STRIPE_SECRET_KEY": "sk_canary"})
	if _, ok := pulledSecretsFor(e); ok {
		t.Fatal("armed values were served to a self-managed env")
	}
	d := resolveEnvDestination(context.Background(), "prod-sim", e, nil)
	if d.ControlPlaneKind != "self_managed" {
		t.Fatalf("reported kind = %q, want self_managed", d.ControlPlaneKind)
	}
	if !envAppliesManifestsToCluster(e) {
		t.Fatal("a self-managed env's manifests are applied from this machine; its mounts are demand")
	}
}

func TestResolveEnvDestination_LocalCarriesKindAndEndpoint(t *testing.T) {
	e := localEnvEntities("https://cp.example.com/")
	called := false
	d := resolveEnvDestination(context.Background(), "dev", e, func(context.Context, string, *KCLEntities) (deploytarget.HostedEnvStatus, error) {
		called = true
		return deploytarget.HostedEnvStatus{}, nil
	})
	if d.Destination != destinationHost || d.ControlPlaneKind != "local" || d.Endpoint != "https://cp.example.com" {
		t.Fatalf("destination = %+v", d)
	}
	if called {
		t.Error("a LOCAL env has no hosted status to read")
	}
}

func TestEnsureHostedEnv_SendsProjectAndKind(t *testing.T) {
	fake := &fakeCPCaller{ensureID: "env-9"}
	id, err := ensureHostedEnv(context.Background(), fake, deploytarget.HostedEnvRef{Project: "hounders", Name: "dev", Kind: deploytarget.HostedEnvLocal})
	if err != nil || id != "env-9" {
		t.Fatalf("ensure = %q, %v", id, err)
	}
	c := fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")
	spec, _ := c[0].Body["spec"].(map[string]any)
	if spec["project"] != "hounders" || spec["name"] != "dev" || spec["kind"] != "DEPLOY_ENVIRONMENT_KIND_LOCAL" {
		t.Fatalf("spec = %v", spec)
	}
	if _, err := ensureHostedEnv(context.Background(), fake, deploytarget.HostedEnvRef{Name: "dev"}); err == nil {
		t.Fatal("an ensure with no derived kind must be refused, never defaulted (kind is immutable server-side)")
	}
}

func TestHostedSecretSet_EnsuresWithProjectAndDerivedKind(t *testing.T) {
	fake, _ := hostedFixture(t, nil)
	withProjectName(t, "hounders")
	if err := runSecretSet(context.Background(), "dev", "K", "", strings.NewReader("v"), io.Discard); err != nil {
		t.Fatal(err)
	}
	ensures := fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")
	if len(ensures) != 1 {
		t.Fatalf("ensures = %+v", ensures)
	}
	spec := ensures[0].Body["spec"].(map[string]any)
	// hostedFixture's env declares HostedSecrets and no tier → LOCAL.
	if spec["project"] != "hounders" || spec["kind"] != "DEPLOY_ENVIRONMENT_KIND_LOCAL" {
		t.Fatalf("EnsureEnvironment spec = %v, want project=hounders kind=LOCAL", spec)
	}
}

func TestLookupHostedEnvironment_IgnoresOtherProjects(t *testing.T) {
	fake := &fakeCPCaller{envs: []map[string]any{
		{"id": "env-other", "name": "prod", "project": "other"},
		{"id": "env-mine", "name": "prod", "project": "acme"},
	}}
	id, err := deploytarget.LookupHostedEnvironment(context.Background(), fake, "acme", "prod")
	if err != nil || id != "env-mine" {
		t.Fatalf("lookup = %q, %v; want env-mine", id, err)
	}
}

// ─── 4b. LOCAL env deploy refusal ────────────────────────────────────────────

func TestDispatchHostedDeploy_RefusesLocalEnvBeforeAnyRPC(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "render.json")
	raw := contractJSON(t, localEnvEntities("https://cp.invalid"))
	if err := os.WriteFile(fixture, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)
	prev := hostedDeployClient
	hostedDeployClient = func(cloud.Endpoint, cloud.Credential) deploytarget.HostedCaller {
		t.Fatal("a LOCAL env deploy must be refused before any control-plane client is built")
		return nil
	}
	t.Cleanup(func() { hostedDeployClient = prev })

	hosted, err := dispatchHostedDeploy(context.Background(), dir, "dev", deployOptions{})
	if !hosted || err == nil || !strings.Contains(err.Error(), "is LOCAL") || !strings.Contains(err.Error(), "forge env up dev") {
		t.Fatalf("dispatch = (%v, %v), want a LOCAL refusal naming `forge env up dev`", hosted, err)
	}
	// `forge env up`'s deploy phase (renderToLaunch) continues down the
	// ordinary path: its compose/host workloads are deployed locally.
	hosted, err = dispatchHostedDeploy(context.Background(), dir, "dev", deployOptions{purpose: renderToLaunch})
	if hosted || err != nil {
		t.Fatalf("env up deploy phase = (%v, %v), want the ordinary path", hosted, err)
	}
}

func TestBuildDeployGroups_LocalEnvIsNotHosted(t *testing.T) {
	e := localEnvEntities("https://cp")
	e.Workloads = append(e.Workloads, composeWL("pg", "docker-compose.yml"))
	groups, err := buildDeployGroups("dev", e, "")
	if err != nil {
		t.Fatalf("a LOCAL env's host/compose workloads must group normally: %v", err)
	}
	for _, g := range groups {
		if g.ProviderID == deploytarget.HostedProviderID {
			t.Fatalf("LOCAL env grouped as hosted: %+v", groups)
		}
	}
}

// ─── 5. env up pulls a LOCAL env's secrets ───────────────────────────────────

func TestPullLocalSecrets_ResolvesByProjectThenPulls(t *testing.T) {
	fake := &fakeCPCaller{
		envs:    []map[string]any{{"id": "env-dev", "name": "dev", "project": "hounders"}},
		secrets: []map[string]any{{"name": "STRIPE_SECRET_KEY", "value": "sk_pulled", "version": 2}},
	}
	got, err := pullLocalSecretsWith(context.Background(), fake, "dev", "hounders", "https://cp")
	if err != nil {
		t.Fatal(err)
	}
	if got["STRIPE_SECRET_KEY"] != "sk_pulled" {
		t.Fatalf("pulled = %v", got)
	}
	pulls := fake.callsTo("controlplane.v1.LocalSecretService/PullSecrets")
	if len(pulls) != 1 || pulls[0].Body["environmentId"] != "env-dev" {
		t.Fatalf("PullSecrets calls = %+v", pulls)
	}
	if n := len(fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")); n != 0 {
		t.Fatalf("env up's pull is a READ; it called EnsureEnvironment %d time(s)", n)
	}
}

func TestPullLocalSecrets_UnknownEnvIsEmptyNotError(t *testing.T) {
	fake := &fakeCPCaller{}
	got, err := pullLocalSecretsWith(context.Background(), fake, "dev", "hounders", "https://cp")
	if err != nil || len(got) != 0 {
		t.Fatalf("an env the control plane has not seen holds no values: got %v, %v", got, err)
	}
}

func TestPullLocalSecrets_FailureNamesLogin(t *testing.T) {
	fake := &fakeCPCaller{
		envs:    []map[string]any{{"id": "env-dev", "name": "dev"}},
		pullErr: errors.New("rejected the credential (HTTP 401)"),
	}
	_, err := pullLocalSecretsWith(context.Background(), fake, "dev", "", "https://cp")
	if err == nil || !strings.Contains(err.Error(), "forge login") {
		t.Fatalf("a failed pull must fail loudly with the login fix, got %v", err)
	}
}

// TestUpLocalEnv_PulledSecretsInjectedAndValidated is the env-up seam end to
// end, minus process launch: arm → provider → pre-flight → per-service env.
func TestUpLocalEnv_PulledSecretsInjectedAndValidated(t *testing.T) {
	e := localEnvEntities("https://cp.example.com")
	prev := pullLocalSecretsFor
	t.Cleanup(func() { pullLocalSecretsFor = prev; disarmPulledSecrets() })

	// Failure path: the pull fails → env up fails before any provider is built.
	pullLocalSecretsFor = func(context.Context, string, *KCLEntities) (map[string]string, error) {
		return nil, errors.New("pull secrets for LOCAL env \"dev\": boom\nfix: `forge login`")
	}
	if err := armLocalSecretsForUp(context.Background(), "dev", e, func(string, ...any) {}); err == nil {
		t.Fatal("a failed pull must fail env up")
	}

	// Success path.
	pullLocalSecretsFor = func(context.Context, string, *KCLEntities) (map[string]string, error) {
		return map[string]string{"STRIPE_SECRET_KEY": "sk_pulled_canary"}, nil
	}
	var logged bytes.Buffer
	if err := armLocalSecretsForUp(context.Background(), "dev", e, func(f string, a ...any) {
		logged.WriteString(strings.TrimSpace(f))
		for _, x := range a {
			if s, ok := x.(string); ok {
				logged.WriteString(" " + s)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logged.String(), "sk_pulled_canary") {
		t.Fatal("the pull log line printed a value")
	}
	prov, err := secretProviderFromEntities(e, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.ResolvesValues(prov) || prov.Kind() != "hosted" {
		t.Fatalf("provider = %T kind %q, want a value-resolving hosted provider", prov, prov.Kind())
	}
	if err := secrets.ValidateDeclaredRefs(prov, secretRefsForLaunch(e), secretStoreLabel(e)); err != nil {
		t.Fatalf("pre-flight with the pulled value present: %v", err)
	}
	cmd, _, err := buildHostServiceCmd(context.Background(), nil, e.Workloads[0], prov.All(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(cmd.Env, "STRIPE_SECRET_KEY=sk_pulled_canary") {
		t.Fatal("the pulled value did not reach the host service's environment")
	}

	// A declared secret the store lacks fails the pre-flight with the
	// env-named fix.
	withSecretRef("JWT_SECRET", "s", "JWT_SECRET")(&e.Workloads[0])
	err = withEnvInSecretFix(secrets.ValidateDeclaredRefs(prov, secretRefsForLaunch(e), secretStoreLabel(e)), "dev")
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") || !strings.Contains(err.Error(), "forge secret set --env dev") {
		t.Fatalf("missing pulled secret: %v", err)
	}

	// A persistent hosted env never resolves armed values.
	persistent := &KCLEntities{ControlPlane: e.ControlPlane, SecretProvider: e.SecretProvider,
		Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted, Spec: v1alpha1.ManagedDatabaseSpec{}}}}
	p2, _ := secretProviderFromEntities(persistent, t.TempDir())
	if secrets.ResolvesValues(p2) {
		t.Fatal("a PERSISTENT env's provider must stay value-free")
	}
}

func TestUpFrontend_ReceivesDeclaredSecretsOnly(t *testing.T) {
	fe := FrontendEntity{Name: "web", EnvVars: []KCLEnvVar{{Name: "NEXT_PUBLIC_STRIPE_KEY", SecretRef: "web"}}}
	scoped := scopeSecretsToEnvVars(map[string]string{"NEXT_PUBLIC_STRIPE_KEY": "pk_x", "STRIPE_SECRET_KEY": "sk_x"}, fe.EffectiveEnvVars())
	if scoped["NEXT_PUBLIC_STRIPE_KEY"] != "pk_x" || len(scoped) != 1 {
		t.Fatalf("frontend scope = %v", scoped)
	}
	refs := secretRefsForLaunch(&KCLEntities{Frontends: []FrontendEntity{fe}})
	if len(refs) != 1 || refs[0].EnvName != "NEXT_PUBLIC_STRIPE_KEY" {
		t.Fatalf("launch refs = %+v", refs)
	}
}
