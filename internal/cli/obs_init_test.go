package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/envdoc"
)

// fakeInitControlPlane is a signed-in control plane: EnsureEnvironment answers
// env-<name>, CreateIngestToken a token for it.
func fakeInitControlPlane() *fakeCloudCaller {
	return &fakeCloudCaller{replies: map[string]string{
		procListIngestTokens:                              `{"tokens":[]}`,
		"controlplane.v1.DeployService/EnsureEnvironment": `{"environment":{"id":"env-prod","name":"prod","project":"shop","kind":"DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"},"created":true}`,
		procCreateIngestToken: `{"token":{"id":"jti-1","orgId":"org-1","environmentId":"env-prod","name":"forge-obs-init","createdBy":"u1",` +
			`"issuedAt":"2026-11-11T00:00:00Z","expiresAt":"2027-02-09T00:00:00Z"},"jwt":"eyJ.a.b","otlpHeaders":"authorization=Bearer eyJ.a.b",` +
			`"otlpEndpoint":"https://otlp.example.test"}`,
	}}
}

// runInit runs `forge obs init args…` in dir against cp. connectErr, when
// set, is the sign-in failure.
func runInit(t *testing.T, dir string, cp *fakeCloudCaller, confirm func(string) (bool, bool), connectErr error, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	t.Setenv(cloud.ConsoleURLEnv, "")
	var out bytes.Buffer
	var connected []string
	prev := obsInitDepsOverride
	obsInitDepsOverride = &obsInitDeps{
		Connect: func(_ context.Context, controlPlane, project, org string, bind bool) (obsSession, error) {
			connected = append(connected, controlPlane+"|"+project)
			if connectErr != nil {
				return obsSession{}, connectErr
			}
			if !bind {
				t.Error("obs init must create the project on a person's first use (bind)")
			}
			return obsSession{Caller: cp, OrgID: "org-1", ControlPlane: controlPlane}, nil
		},
		Getwd:   func() (string, error) { return dir, nil },
		Confirm: confirm,
		Stdout:  &out,
		Stderr:  &out,
	}
	t.Cleanup(func() { obsInitDepsOverride = prev })
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"obs", "init"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func noTTY(string) (bool, bool) { return false, false }

func TestObsInit_InARepoWithANameOnlyForgeYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("# our app\nname: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	out, err := runInit(t, dir, cp, noTTY, nil, "prod", "--json", "--service", "checkout-api")
	if err != nil {
		t.Fatalf("obs init: %v\n%s", err, out)
	}
	var doc obsInitDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a document: %v\n%s", err, out)
	}
	if doc.Schema != obsInitSchema || doc.Project != "shop" || doc.OrgID != "org-1" || !doc.WroteConfig || len(doc.Envs) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	e := doc.Envs[0]
	want := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.example.test",
		"OTEL_EXPORTER_OTLP_HEADERS":  "authorization=Bearer eyJ.a.b",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":           "checkout-api",
	}
	for k, v := range want {
		if e.OTelEnv[k] != v {
			t.Errorf("%s = %q, want %q", k, e.OTelEnv[k], v)
		}
	}
	if ra := e.OTelEnv["OTEL_RESOURCE_ATTRIBUTES"]; !strings.Contains(ra, "deployment.environment.name=prod") || !strings.Contains(ra, "service.version=") {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES = %q", ra)
	}
	if e.DashboardURL != cloud.DefaultConsoleURL+"/forge/p/shop/observability/dashboards?env=prod" {
		t.Errorf("dashboard = %q", e.DashboardURL)
	}
	if e.EnvironmentID != "env-prod" || e.Token == nil || e.Token.ID != "jti-1" {
		t.Errorf("env = %+v", e)
	}

	// The env is registered as SELF_MANAGED, with nothing but its project and
	// name, and the token is minted for the id the control plane answered.
	var ensured, minted string
	for _, c := range cp.calls {
		switch c.procedure {
		case "controlplane.v1.DeployService/EnsureEnvironment":
			ensured = c.body
		case procCreateIngestToken:
			minted = c.body
		}
	}
	if ensured != `{"spec":{"kind":"DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED","name":"prod","project":"shop"}}` {
		t.Errorf("EnsureEnvironment body = %s", ensured)
	}
	if minted != `{"environmentId":"env-prod","name":"forge-obs-init","orgId":"org-1"}` {
		t.Errorf("CreateIngestToken body = %s", minted)
	}

	// The env is declared by its env document, and forge.yaml is untouched:
	// it is project and tooling config, not a list of envs.
	if raw := mustRead(t, filepath.Join(dir, "forge.yaml")); raw != "# our app\nname: shop\n" {
		t.Errorf("forge.yaml was rewritten:\n%s", raw)
	}
	docPath := filepath.Join(dir, "deploy", "envs", "prod.yaml")
	if doc.ConfigPath != docPath || len(doc.WroteFiles) != 1 || e.Declaration != "deploy/envs/prod.yaml" {
		t.Errorf("config_path = %q, wrote_files = %v, declaration = %q; want the env document", doc.ConfigPath, doc.WroteFiles, e.Declaration)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertMonitoringDocument(t, dir, cfg, "shop", "prod")
}

// assertMonitoringDocument checks that env's document init wrote is what the
// seam reads: the document source, loaded through the render seam into an
// obs target on Reliant's control plane, naming project.
func assertMonitoringDocument(t *testing.T, dir string, cfg *config.ProjectConfig, project, env string) {
	t.Helper()
	d, err := declareEnv(dir, cfg, env)
	if err != nil || d.Source != envDeclDocument {
		t.Fatalf("the seam does not see the env document init wrote: %+v %v", d, err)
	}
	if cfg.DeclaresEnv(env) {
		t.Errorf("forge.yaml declares %s too", env)
	}
	e, err := RenderKCL(context.Background(), dir, env)
	if err != nil {
		t.Fatalf("the document does not load: %v\n%s", err, mustRead(t, filepath.Join(dir, "deploy", "envs", env+".yaml")))
	}
	f := obsTargetFromEntities(env, e).Facts
	if e.Project != project || e.Env != env || !f.Declared || !f.Enabled || f.Destination == nil || !f.Destination.TokenFromControlPlane {
		t.Fatalf("entities %+v, facts %+v", e, f)
	}
}

func TestObsInit_NoForgeYAML(t *testing.T) {
	t.Run("nobody to ask and no --project", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "shop")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod")
		if err == nil || !strings.Contains(err.Error(), "--write-config") {
			t.Fatalf("err = %v, want a refusal naming --write-config", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("wrote what nobody agreed to: %v", entries)
		}
	})
	t.Run("--write-config writes the env document, named after the directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "My Shop")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--write-config")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			t.Fatal("wrote a forge.yaml: the env document stands alone")
		}
		assertMonitoringDocument(t, dir, nil, "my-shop", "prod")
		if got := envDocumentsProject(dir); got != "my-shop" {
			t.Errorf("the project the verbs read = %q, want my-shop", got)
		}
		if !strings.Contains(out, "wrote "+filepath.Join(dir, "deploy", "envs", "prod.yaml")) {
			t.Errorf("text output does not say what it wrote:\n%s", out)
		}
		if !strings.Contains(out, "export OTEL_EXPORTER_OTLP_HEADERS='authorization=Bearer eyJ.a.b'") {
			t.Errorf("text output lacks the header export:\n%s", out)
		}
	})
	t.Run("--project --no-write leaves the directory untouched", func(t *testing.T) {
		dir := t.TempDir()
		out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--project", "shop", "--no-write", "--json")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("wrote into the directory: %v", entries)
		}
	})
	t.Run("the user says yes", func(t *testing.T) {
		dir := t.TempDir()
		var asked string
		yes := func(q string) (bool, bool) { asked = q; return true, true }
		if _, err := runInit(t, dir, fakeInitControlPlane(), yes, nil, "prod", "--project", "shop"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(asked, filepath.Join("deploy", "envs", "prod.yaml")) {
			t.Fatalf("asked %q", asked)
		}
		assertMonitoringDocument(t, dir, nil, "shop", "prod")
	})
}

func TestObsInit_SignInIsRequired(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	_, err := runInit(t, dir, cp, noTTY, errors.New("forge obs needs you signed in"), "prod")
	if err == nil || !strings.Contains(err.Error(), "signed in") {
		t.Fatalf("err = %v", err)
	}
	if len(cp.calls) != 0 {
		t.Fatalf("reached the control plane without a credential: %+v", cp.calls)
	}
}

// A control plane that predates TelemetryTokenService is refused before any
// env is registered on it: init leaves it exactly as it found it.
func TestObsInit_OldControlPlaneRegistersNothing(t *testing.T) {
	for name, cerr := range map[string]*cloud.Error{
		"unimplemented": {HTTPStatus: 501, Code: cloud.CodeUnimplemented, Message: "not served"},
		"no route":      {HTTPStatus: 404},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cp := fakeInitControlPlane()
			cp.errs = map[string]error{procListIngestTokens: cerr}
			_, err := runInit(t, dir, cp, noTTY, nil, "prod", "--write-config")
			if err == nil || !strings.Contains(err.Error(), "newer control plane") {
				t.Fatalf("err = %v", err)
			}
			for _, c := range cp.calls {
				if c.procedure != procListIngestTokens {
					t.Errorf("called %s on a control plane without ingest tokens", c.procedure)
				}
			}
		})
	}
}

func TestObsInit_RefusesAKCLEnv(t *testing.T) {
	dir, _ := monitoringProject(t, "name: shop\n", "prod")
	cp := fakeInitControlPlane()
	_, err := runInit(t, dir, cp, noTTY, nil, "prod")
	if err == nil || !strings.Contains(err.Error(), "deploy/kcl/prod") {
		t.Fatalf("err = %v, want a refusal naming the KCL env", err)
	}
	if len(cp.calls) != 0 {
		t.Fatal("registered a KCL env as self-managed")
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "envs")); err == nil {
		t.Fatal("wrote an env document for a KCL env")
	}
}

func TestObsInit_SelfHostedEndpointMintsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: shop\nenvs: [prod]\nmonitoring:\n  endpoint: https://otel.example.com:4318\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	connected := false
	out, err := runInit(t, dir, cp, noTTY, nil, "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_ = connected
	var doc obsInitDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Envs) != 1 || doc.Envs[0].OTelEnv["OTEL_EXPORTER_OTLP_ENDPOINT"] != "https://otel.example.com:4318" || doc.Envs[0].Token != nil {
		t.Fatalf("doc = %+v", doc)
	}
	if _, ok := doc.Envs[0].OTelEnv["OTEL_EXPORTER_OTLP_HEADERS"]; ok {
		t.Fatal("printed an auth header for a self-hosted backend")
	}
	if len(cp.calls) != 0 {
		t.Fatal("reached Reliant's control plane for a self-hosted backend")
	}
	if doc.WroteConfig || doc.Envs[0].Declaration != "forge.yaml" {
		t.Errorf("wrote_config = %v, declaration = %q; a legacy env stays in forge.yaml", doc.WroteConfig, doc.Envs[0].Declaration)
	}
}

func TestUpsertMonitoringConfig_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(path, []byte("name: shop\nenvs: [prod]\nmonitoring: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := upsertMonitoringConfig(path, "shop", []string{"prod"}, "")
	if err != nil || changed {
		t.Fatalf("changed = %v, %v; want an unchanged file", changed, err)
	}
	if changed, err = upsertMonitoringConfig(path, "shop", []string{"staging"}, ""); err != nil || !changed {
		t.Fatalf("adding staging = %v, %v", changed, err)
	}
	cfg, err := config.LoadProject([]byte(mustRead(t, path)), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Envs) != 2 || cfg.Envs[0] != "prod" || cfg.Envs[1] != "staging" {
		t.Fatalf("envs = %v", cfg.Envs)
	}
}

func TestProjectNameFromDir(t *testing.T) {
	for in, want := range map[string]string{"My Shop": "my-shop", "shop_api": "shop-api", "123": "", "--": "", "Acme.Web": "acme-web"} {
		if got := projectNameFromDir(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// A Go repo's hand-written forge.yaml of `name:` alone does not load (a Go
// project names its module); init writes the module path go.mod declares.
func TestObsInit_GoRepoWithANameOnlyForgeYAML(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"forge.yaml": "name: shop\n",
		"go.mod":     "module github.com/acme/shop\n\ngo 1.24\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModulePath != "github.com/acme/shop" {
		t.Fatalf("forge.yaml = %+v", cfg)
	}
	assertMonitoringDocument(t, dir, cfg, "shop", "prod")
}

// The control plane caps an ingest token at 90 days and nothing renews it, so
// the human view says when it expires and the command that rotates it.
func TestObsInit_HumanViewSaysHowToRotateBeforeExpiry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--write-config")
	if err != nil {
		t.Fatalf("obs init: %v\n%s", err, out)
	}
	for _, want := range []string{"expires 2027-02-09", "forge obs tokens rotate jti-1 --grace 24h", "90 days at most"} {
		if !strings.Contains(out, want) {
			t.Errorf("human view lacks %q:\n%s", want, out)
		}
	}
}

// TestObsInit_SeveralEnvsInOneCall pins the form the adopt and
// observability/monitoring-only skills tell agents to copy:
// `forge obs init prod staging` registers each env and mints one token per env.
func TestObsInit_SeveralEnvsInOneCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	out, err := runInit(t, dir, cp, noTTY, nil, "prod", "staging", "--json")
	if err != nil {
		t.Fatalf("obs init prod staging: %v\n%s", err, out)
	}
	var doc obsInitDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a document: %v\n%s", err, out)
	}
	if len(doc.Envs) != 2 {
		t.Fatalf("envs in the document = %d, want 2 (prod, staging)\n%s", len(doc.Envs), out)
	}
	var ensured, minted []string
	for _, c := range cp.calls {
		switch c.procedure {
		case "controlplane.v1.DeployService/EnsureEnvironment":
			ensured = append(ensured, c.body)
		case procCreateIngestToken:
			minted = append(minted, c.body)
		}
	}
	if len(ensured) != 2 || !strings.Contains(ensured[0], `"name":"prod"`) || !strings.Contains(ensured[1], `"name":"staging"`) {
		t.Errorf("EnsureEnvironment calls = %v, want prod then staging", ensured)
	}
	if len(minted) != 2 {
		t.Errorf("CreateIngestToken calls = %d, want one per env", len(minted))
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertMonitoringDocument(t, dir, cfg, "shop", "prod")
	assertMonitoringDocument(t, dir, cfg, "shop", "staging")
}

// An env forge.yaml `envs:` already declares (the legacy shorthand) keeps
// working and is not moved: a re-run leaves forge.yaml byte-identical and
// writes no env document.
func TestObsInit_LegacyForgeYAMLEnvIsLeftWhereItIs(t *testing.T) {
	dir := t.TempDir()
	const legacy = "# ours\nname: shop\nenvs: [prod]\nmonitoring: true\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	out, err := runInit(t, dir, cp, noTTY, nil, "prod", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var doc obsInitDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.WroteConfig || len(doc.WroteFiles) != 0 || len(doc.Envs) != 1 || doc.Envs[0].Declaration != "forge.yaml" || doc.Envs[0].Token == nil {
		t.Fatalf("doc = %+v", doc)
	}
	if got := mustRead(t, filepath.Join(dir, "forge.yaml")); got != legacy {
		t.Errorf("forge.yaml was rewritten:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "envs")); err == nil {
		t.Error("wrote an env document for an env forge.yaml declares")
	}
}

// A new env beside a legacy one gets its document, and the document says
// where forge.yaml's `monitoring:` sends telemetry, so the project's envs
// agree; the legacy env stays in forge.yaml.
func TestObsInit_NewEnvBesideALegacyOneTakesItsMonitoring(t *testing.T) {
	dir := t.TempDir()
	const legacy = "name: shop\nenvs: [prod]\nmonitoring:\n  control_plane: https://cp.example.test\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "staging", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var doc obsInitDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ControlPlane != "https://cp.example.test" || len(doc.Envs) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	if got := mustRead(t, filepath.Join(dir, "forge.yaml")); got != legacy {
		t.Errorf("forge.yaml was rewritten:\n%s", got)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := declareEnv(dir, cfg, "staging")
	if err != nil || d.Source != envDeclDocument {
		t.Fatalf("staging = %+v, %v", d, err)
	}
	staging, ok, err := readEnvDocumentDeclaration(dir, "staging")
	if err != nil || !ok || staging.Monitoring == nil || staging.Monitoring.ControlPlane != "https://cp.example.test" {
		t.Fatalf("staging's document = %+v, %v, %v; want forge.yaml's control plane", staging, ok, err)
	}
}

// A self-hosted endpoint in forge.yaml's `monitoring:` is what a new env's
// document declares as its destination, and nothing is minted.
func TestObsInit_NewDocumentKeepsASelfHostedEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: shop\nmonitoring:\n  endpoint: https://otel.example.com:4318\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := fakeInitControlPlane()
	out, err := runInit(t, dir, cp, noTTY, nil, "prod", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(cp.calls) != 0 {
		t.Fatal("reached Reliant's control plane for a self-hosted backend")
	}
	d, ok, err := readEnvDocumentDeclaration(dir, "prod")
	if err != nil || !ok || d.Monitoring == nil || d.Monitoring.Endpoint != "https://otel.example.com:4318" {
		t.Fatalf("prod's document = %+v, %v, %v", d, ok, err)
	}
}

// An env document init wrote is read, never rewritten: a re-run registers the
// env and mints a token again, and writes nothing.
func TestObsInit_DocumentEnvRerunIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--project", "shop", "--write-config"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "deploy", "envs", "prod.yaml")
	first := mustRead(t, path)
	for _, args := range [][]string{{"prod", "--json"}, {"--json"}} {
		cp := fakeInitControlPlane()
		out, err := runInit(t, dir, cp, noTTY, nil, args...)
		if err != nil {
			t.Fatalf("re-run %v: %v\n%s", args, err, out)
		}
		var doc obsInitDoc
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.WroteConfig || doc.Project != "shop" || len(doc.Envs) != 1 || doc.Envs[0].Env != "prod" || doc.Envs[0].Token == nil {
			t.Fatalf("re-run %v: doc = %+v", args, doc)
		}
		if got := mustRead(t, path); got != first {
			t.Fatalf("re-run %v rewrote the document:\n%s", args, got)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the directory holds more than deploy/: %v", entries)
	}
}

// One init signs in to one control plane: envs whose declarations send
// telemetry to different places are refused before anything is registered.
func TestObsInit_EnvsOnDifferentControlPlanesAreRefused(t *testing.T) {
	dir := t.TempDir()
	writeEnvDocument(t, dir, "prod.yaml", minimalMonitoringDocument)
	writeEnvDocument(t, dir, "staging.yaml", strings.ReplaceAll(
		strings.Replace(minimalMonitoringDocument, "control_plane: {}", "control_plane: {endpoint: https://cp.example.test}", 1),
		"env: prod", "env: staging"))
	cp := fakeInitControlPlane()
	_, err := runInit(t, dir, cp, noTTY, nil, "prod", "staging")
	if err == nil || !strings.Contains(err.Error(), "init them separately") {
		t.Fatalf("err = %v", err)
	}
	if len(cp.calls) != 0 {
		t.Fatalf("registered envs before refusing: %+v", cp.calls)
	}
}

// A project or env whose name a plain YAML scalar would read as a boolean or a
// number (`on`, `true`, `2026`) is written quoted, so the env document init
// writes loads back with the names it was given (review B1 on #2217).
func TestWriteMonitoringDocumentQuotesYAMLAmbiguousNames(t *testing.T) {
	for _, tc := range []struct{ project, env string }{{"on", "2026"}, {"yes", "true"}, {"null", "off"}, {"shop", "1e3"}} {
		dir := t.TempDir()
		path := filepath.Join(dir, envdoc.Dir, tc.env+".yaml")
		for _, m := range []config.MonitoringConfig{{Enabled: true}, {Enabled: true, Endpoint: "https://otel.example"}} {
			if err := writeMonitoringDocument(path, tc.project, tc.env, m); err != nil {
				t.Fatal(err)
			}
			d, ok, err := readEnvDocumentDeclaration(dir, tc.env)
			if err != nil || !ok {
				t.Fatalf("%s/%s: the written document does not load: ok=%v err=%v", tc.project, tc.env, ok, err)
			}
			if d.Project != tc.project {
				t.Errorf("project read back as %q, want %q", d.Project, tc.project)
			}
		}
	}
}
