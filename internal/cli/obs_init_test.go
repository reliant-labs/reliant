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

	// forge.yaml now declares the env for every obs verb, comments kept.
	raw, _ := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if !strings.Contains(string(raw), "# our app") {
		t.Errorf("the author's comment was lost:\n%s", raw)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DeclaresEnv("prod") || !cfg.MonitoringOn() {
		t.Fatalf("forge.yaml after init:\n%s", raw)
	}
	d, err := declareEnv(dir, cfg, "prod")
	if err != nil || d.Source != envDeclYAML {
		t.Fatalf("the seam does not see the env init declared: %+v %v", d, err)
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
		if _, statErr := os.Stat(filepath.Join(dir, "forge.yaml")); statErr == nil {
			t.Fatal("wrote forge.yaml nobody agreed to")
		}
	})
	t.Run("--write-config writes a minimal one named after the directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "My Shop")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := runInit(t, dir, fakeInitControlPlane(), noTTY, nil, "prod", "--write-config")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Name != "my-shop" || !cfg.DeclaresEnv("prod") || !cfg.MonitoringOn() {
			t.Fatalf("forge.yaml = %+v", cfg)
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
		if !strings.Contains(asked, "forge.yaml") {
			t.Fatalf("asked %q", asked)
		}
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err != nil {
			t.Fatal("did not write forge.yaml after a yes")
		}
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
	if cfg.ModulePath != "github.com/acme/shop" || !cfg.DeclaresEnv("prod") {
		t.Fatalf("forge.yaml = %+v", cfg)
	}
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
