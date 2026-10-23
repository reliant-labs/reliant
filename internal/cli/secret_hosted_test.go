package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// fakeControlPlane is an httptest server speaking Connect's JSON binding for
// the two services the hosted secret commands call. It records every call so
// tests assert the WIRE: procedure path, bearer token, and body fields.
type fakeControlPlane struct {
	mu    sync.Mutex
	calls []recordedCall
	envs  []cloudEnvironment
	// stored secret names -> version
	stored map[string]uint32
}

type recordedCall struct {
	Path  string
	Auth  string
	Body  map[string]any
	Bytes string
}

func (f *fakeControlPlane) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.calls = append(f.calls, recordedCall{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body, Bytes: string(raw)})
		f.mu.Unlock()
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "connect json binding requires POST + application/json", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		// LITERAL paths, not the production constants: a test that shared
		// them would move with a typo in the constant and pass.
		case "/controlplane.v1.DeployService/ListEnvironments":
			f.mu.Lock()
			envs := append([]cloudEnvironment(nil), f.envs...)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(cloudEnvironmentsResponse{Environments: envs})
		case "/controlplane.v1.DeployService/EnsureEnvironment":
			spec, _ := body["spec"].(map[string]any)
			name, _ := spec["name"].(string)
			f.mu.Lock()
			var id string
			for _, e := range f.envs {
				if e.Name == name {
					id = e.ID
				}
			}
			created := id == ""
			if created {
				id = "env-" + name + "-created"
				f.envs = append(f.envs, cloudEnvironment{ID: id, Name: name})
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]string{"id": id, "name": name}, "created": created})
		case "/controlplane.v1.SecretStoreService/SetSecret":
			name, _ := body["name"].(string)
			f.mu.Lock()
			f.stored[name]++
			v := f.stored[name]
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"version": v})
		case "/controlplane.v1.SecretStoreService/ListSecrets":
			var out []map[string]any
			f.mu.Lock()
			for n, v := range f.stored {
				out = append(out, map[string]any{"name": n, "currentVersion": v})
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": out})
		case "/controlplane.v1.SecretStoreService/DeleteSecret":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"unimplemented","message":"no such procedure"}`))
		}
	})
}

func (f *fakeControlPlane) callsTo(proc string) []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedCall
	for _, c := range f.calls {
		if c.Path == "/"+proc {
			out = append(out, c)
		}
	}
	return out
}

// hostedFixture wires the CLI to a fake control plane: KCL render returns an
// env declaring HostedSecrets + ControlPlane{endpoint: server}, and the writer
// is built through the REAL cloud.Client + REAL cloudEnvResolver.
func hostedFixture(t *testing.T, envs []cloudEnvironment) (*fakeControlPlane, string) {
	t.Helper()
	fake := &fakeControlPlane{envs: envs, stored: map[string]uint32{}}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	const token = "rlat_test_token_value"

	entities := &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "hosted"},
		ControlPlane:   &ControlPlaneEntity{Type: "control_plane", Endpoint: srv.URL},
	}
	prevRender, prevWriter := renderEntitiesForSecrets, newHostedSecretWriter
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return entities, nil }
	newHostedSecretWriter = func(ctx context.Context, envName string, e *KCLEntities, ensure bool) (hostedSecretWriter, cloud.Endpoint, error) {
		ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(e))
		if err != nil {
			return nil, cloud.Endpoint{}, err
		}
		client := cloud.NewClient(ep, cloud.Credential{Token: token})
		ref := hostedEnvRefFor(envName, e)
		var id string
		if ensure {
			id, err = ensureHostedEnv(ctx, client, ref)
		} else {
			id, err = cloudEnvResolver{client: client, project: ref.Project}.ResolveEnvironmentID(ctx, envName)
		}
		if err != nil {
			return nil, ep, err
		}
		return cloudSecretWriter{client: client, environmentID: id}, ep, nil
	}
	t.Cleanup(func() { renderEntitiesForSecrets, newHostedSecretWriter = prevRender, prevWriter })
	return fake, token
}

// TestHostedSecretSetSpeaksSecretStoreService: `forge secret set` on a hosted
// env resolves the env NAME to its id via DeployService/ListEnvironments and
// writes via SecretStoreService/SetSecret — right procedures, bearer token,
// proto3-JSON field names — and never echoes the value.
func TestHostedSecretSetSpeaksSecretStoreService(t *testing.T) {
	fake, token := hostedFixture(t, []cloudEnvironment{
		{ID: "env-prod-eu", Name: "prod-eu"}, // a search near-miss: must not be chosen
		{ID: "env-prod-uuid", Name: "prod"},
	})
	var out bytes.Buffer
	const value = "sk_live_canary_value"
	if err := runSecretSet(context.Background(), "prod", "STRIPE_KEY", "", strings.NewReader(value+"\n"), &out); err != nil {
		t.Fatalf("set: %v", err)
	}
	if strings.Contains(out.String(), value) {
		t.Fatal("forge secret set echoed the value")
	}

	// set is a WRITE: it ensures the env by name (exact — never the
	// prod-eu near-miss), and never needs a search.
	ensures := fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")
	if len(ensures) != 1 || ensures[0].Body["spec"].(map[string]any)["name"] != "prod" {
		t.Fatalf("EnsureEnvironment calls = %+v, want one for prod", ensures)
	}
	sets := fake.callsTo("controlplane.v1.SecretStoreService/SetSecret")
	if len(sets) != 1 {
		t.Fatalf("SetSecret calls = %d, want 1 (paths seen: %v)", len(sets), fake.calls)
	}
	c := sets[0]
	if c.Auth != "Bearer "+token {
		t.Errorf("Authorization = %q, want the bearer credential", c.Auth)
	}
	if c.Body["environmentId"] != "env-prod-uuid" {
		t.Errorf("environmentId = %v, want the EXACT-name match env-prod-uuid", c.Body["environmentId"])
	}
	if c.Body["name"] != "STRIPE_KEY" {
		t.Errorf("name = %v", c.Body["name"])
	}
	if c.Body["secretValue"] != value {
		t.Error("secretValue on the wire is not the value piped in (trailing newline must be trimmed)")
	}
	for _, forbidden := range []string{"projectId", "project_id", "env"} {
		if _, ok := c.Body[forbidden]; ok {
			t.Errorf("request carries retired field %q", forbidden)
		}
	}
}

// TestHostedSecretListIsNamesOnly: list goes through ListSecrets and the
// report — the same secretListReport the file provider emits — carries names
// and presence only.
func TestHostedSecretListIsNamesOnly(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	fake.stored["API_KEY"] = 2

	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := fake.callsTo("controlplane.v1.SecretStoreService/ListSecrets"); len(got) != 1 || got[0].Body["environmentId"] != "env-1" {
		t.Fatalf("ListSecrets calls = %+v", got)
	}
	if report.Provider != "hosted" {
		t.Errorf("provider = %q, want hosted", report.Provider)
	}
	if !containsString(report.Inert, "API_KEY") {
		t.Errorf("stored-but-undeclared API_KEY not reported: %+v", report)
	}
	var buf bytes.Buffer
	if err := runSecretListJSON(context.Background(), "prod", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"provider": "hosted"`) {
		t.Errorf("json report: %s", buf.String())
	}
}

// TestHostedSecretListCountsManagedSecrets: a SimpleBackend's managedSecret
// env vars ARE the hosted env's secret declarations — the control plane
// materializes exactly those names into forge-managed-secrets, all or nothing,
// so one without a value stalls the backend's reconcile. `forge secret list`
// must report each as declared (set or missing) and must not call a stored
// one inert. Before this, list read only secret_ref channels, so a hosted env
// printed "no secrets declared" while its api could not start.
func TestHostedSecretListCountsManagedSecrets(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	fake.stored["STRIPE_SECRET_KEY"] = 1

	prevRender := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(ctx context.Context, env string) (*KCLEntities, error) {
		e, err := prevRender(ctx, env)
		if err != nil {
			return nil, err
		}
		e.Workloads = []WorkloadEntity{hostedWL("api", func(w *WorkloadEntity) {
			w.Spec.Env = []deployv1alpha1.EnvVar{
				{Name: "LOG_FORMAT", Value: "json"},
				{Name: "STRIPE_SECRET_KEY", ManagedSecret: &deployv1alpha1.ManagedSecretRef{Name: "STRIPE_SECRET_KEY"}},
				// The env var and the store name differ: the STORE name
				// is what gets declared, because that is what `forge
				// secret set` writes and the control plane resolves.
				{Name: "WEBHOOK_SECRET", ManagedSecret: &deployv1alpha1.ManagedSecretRef{Name: "STRIPE_WEBHOOK_SECRET"}},
				{Name: "DATABASE_URL", DatabaseRef: &deployv1alpha1.DatabaseRef{Name: "db"}},
			}
		})}
		return e, nil
	}
	t.Cleanup(func() { renderEntitiesForSecrets = prevRender })

	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]string{}
	for _, s := range report.Secrets {
		got[s.Name] = s.Presence
		if len(s.DeclaredBy) != 1 || s.DeclaredBy[0].Workload != "api" || s.DeclaredBy[0].Kind != "managed-secret" {
			t.Errorf("%s declared_by = %+v, want one managed-secret declaration by api", s.Name, s.DeclaredBy)
		}
	}
	want := map[string]string{"STRIPE_SECRET_KEY": secretPresenceSet, "STRIPE_WEBHOOK_SECRET": secretPresenceMissing}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secrets = %v, want %v (DATABASE_URL is a databaseRef, never a store key)", got, want)
	}
	if len(report.Inert) != 0 {
		t.Errorf("inert = %v: a stored managedSecret is declared, not inert", report.Inert)
	}
	if report.OK || report.MissingCount != 1 {
		t.Errorf("ok=%v missing=%d, want ok=false missing=1", report.OK, report.MissingCount)
	}
}

// TestHostedSecretCLIEndToEnd drives the REAL cobra commands and the REAL
// production writer constructor (newHostedSecretWriter is NOT substituted —
// only the KCL render is), so the credential comes from
// FORGE_CONTROL_PLANE_TOKEN exactly as it would in CI:
//
//	printf v | forge secret set prod API_KEY
//	forge secret list prod --json
//
// It asserts the UX contract end to end: list shows the name with provider
// "hosted", the value never appears on stdout/stderr of either command, and
// nothing is written to local disk (no secrets/<env>.yaml, no forge home).
func TestHostedSecretCLIEndToEnd(t *testing.T) {
	fake := &fakeControlPlane{envs: []cloudEnvironment{{ID: "env-prod-uuid", Name: "prod"}}, stored: map[string]uint32{}}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	entities := &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "hosted"},
		ControlPlane:   &ControlPlaneEntity{Type: "control_plane", Endpoint: srv.URL},
	}
	prevRender := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return entities, nil }
	t.Cleanup(func() { renderEntitiesForSecrets = prevRender })

	projectDir, forgeHome := t.TempDir(), t.TempDir()
	t.Setenv("FORGE_HOME", forgeHome)
	t.Setenv(cloud.DefaultTokenEnv, "rlat_e2e_token")
	t.Chdir(projectDir)

	const value = "sk_live_e2e_canary_value"
	run := func(stdin string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := newSecretCmd()
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("forge secret %v: %v\n%s", args, err, out.String())
		}
		if strings.Contains(out.String(), value) {
			t.Fatalf("forge secret %v printed the value", args)
		}
		return out.String()
	}

	run(value+"\n", "set", "--env", "prod", "API_KEY")
	sets := fake.callsTo("controlplane.v1.SecretStoreService/SetSecret")
	if len(sets) != 1 || sets[0].Auth != "Bearer rlat_e2e_token" || sets[0].Body["environmentId"] != "env-prod-uuid" {
		t.Fatalf("SetSecret calls = %+v", sets)
	}

	listJSON := run("", "list", "--env", "prod", "--json")
	var report secretListReport
	if err := json.Unmarshal([]byte(listJSON), &report); err != nil {
		t.Fatalf("list --json is not JSON: %v\n%s", err, listJSON)
	}
	if report.Provider != "hosted" || report.StorePath != srv.URL {
		t.Fatalf("provider/store = %q/%q, want hosted/%s", report.Provider, report.StorePath, srv.URL)
	}
	if !containsString(report.Inert, "API_KEY") {
		t.Fatalf("API_KEY not listed after set: %+v", report)
	}

	// Nothing cached locally: the hosted path must not touch the file
	// provider's store nor persist anything under the forge home.
	for _, dir := range []string{projectDir, forgeHome} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("hosted secret commands wrote to %s: %v", dir, entries)
		}
	}
}

// TestHostedSecretUnsetCallsDeleteSecret.
func TestHostedSecretUnsetCallsDeleteSecret(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	if err := runSecretUnset(context.Background(), "prod", "API_KEY", io.Discard); err != nil {
		t.Fatalf("unset: %v", err)
	}
	dels := fake.callsTo("controlplane.v1.SecretStoreService/DeleteSecret")
	if len(dels) != 1 || dels[0].Body["environmentId"] != "env-1" || dels[0].Body["name"] != "API_KEY" {
		t.Fatalf("DeleteSecret calls = %+v", dels)
	}
}

// TestHostedSecretSetCreatesAFreshEnv: set on an env the control plane has
// never seen CREATES it by name (the env is declared in KCL), so a secret can
// be stored before the first deploy. The near-miss "prod-eu" is never
// written. Mutation: resolving instead of ensuring fails this.
func TestHostedSecretSetCreatesAFreshEnv(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-x", Name: "prod-eu"}})
	if err := runSecretSet(context.Background(), "prod", "K", "", strings.NewReader("v"), io.Discard); err != nil {
		t.Fatalf("set on a fresh env: %v", err)
	}
	sets := fake.callsTo("controlplane.v1.SecretStoreService/SetSecret")
	if len(sets) != 1 || sets[0].Body["environmentId"] != "env-prod-created" {
		t.Fatalf("SetSecret calls = %+v, want one into the created env", sets)
	}
}

// TestHostedSecretListNeverCreates: list is a READ. An env the control plane
// has not seen holds no secrets — every declared one is reported missing —
// and nothing is created.
func TestHostedSecretListNeverCreates(t *testing.T) {
	fake, _ := hostedFixture(t, nil)
	e, _ := renderEntitiesForSecrets(context.Background(), "prod")
	e.Workloads = []WorkloadEntity{hostWL("api", withSecretRef("K", "s", ""))}
	r, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatalf("list of an env the control plane has not seen: %v", err)
	}
	if r.OK || len(r.Missing) != 1 || r.Missing[0] != "K" || r.StoreExists {
		t.Fatalf("report = %+v, want K missing and no store", r)
	}
	if n := len(fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")); n != 0 {
		t.Fatalf("list called EnsureEnvironment %d time(s)", n)
	}
}
