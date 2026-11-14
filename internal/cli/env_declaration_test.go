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
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// monitoringProject writes forge.yaml (and, per kclEnvs, a deploy/kcl/<env>/main.k)
// into a temp dir and returns the dir and the loaded config.
func monitoringProject(t *testing.T, forgeYAML string, kclEnvs ...string) (string, *config.ProjectConfig) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range kclEnvs {
		p := filepath.Join(dir, "deploy", "kcl", env)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "main.k"), []byte("import forge\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

func TestDeclareEnv_ForgeYAMLLowersToTheControlPlaneBackend(t *testing.T) {
	dir, cfg := monitoringProject(t, "name: shop\nenvs: [prod]\nmonitoring: true\n")
	d, err := declareEnv(dir, cfg, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != envDeclYAML || d.Project != "shop" {
		t.Fatalf("declaration = %+v", d)
	}
	f := yamlObsTarget(d).Facts
	if !f.Declared || !f.Enabled || f.Destination == nil || !f.Destination.TokenFromControlPlane || f.ControlPlane != cloud.DefaultEndpoint {
		t.Fatalf("facts = %+v", f)
	}
	// The same table KCL facts go through: a monitoring-only env reads from
	// the control plane.
	dec, err := obsbackend.Resolve(f, "")
	if err != nil || dec.Backend != obsbackend.BackendCloud {
		t.Fatalf("resolve = %+v, %v; want the cloud backend", dec, err)
	}
}

func TestDeclareEnv_SelfHostedEndpointIsNotQueryable(t *testing.T) {
	dir, cfg := monitoringProject(t, "name: shop\nenvs: [prod]\nmonitoring:\n  endpoint: https://otel.example.com\n")
	d, err := declareEnv(dir, cfg, "prod")
	if err != nil {
		t.Fatal(err)
	}
	f := yamlObsTarget(d).Facts
	if f.Destination == nil || f.Destination.Endpoint != "https://otel.example.com" || f.ControlPlane != "" {
		t.Fatalf("facts = %+v", f)
	}
	if _, err := obsbackend.Resolve(f, ""); err == nil {
		t.Fatal("a self-hosted destination resolved to a backend forge can query")
	}
}

func TestDeclareEnv_KCLWinsAndBothIsALoadError(t *testing.T) {
	// The env is KCL's: forge.yaml lists it but declares no monitoring.
	dir, cfg := monitoringProject(t, "name: shop\nenvs: [prod]\n", "prod")
	if d, err := declareEnv(dir, cfg, "prod"); err != nil || d.Source != envDeclKCL {
		t.Fatalf("declaration = %+v, %v; want KCL's", d, err)
	}
	// Both declare its monitoring: refused, naming both places.
	dir, cfg = monitoringProject(t, "name: shop\nenvs: [prod, staging]\nmonitoring: true\n", "prod")
	if _, err := declareEnv(dir, cfg, "prod"); !errors.Is(err, errEnvDeclaredTwice) {
		t.Fatalf("both = %v, want errEnvDeclaredTwice", err)
	}
	// Another env of the same project is unaffected.
	if d, err := declareEnv(dir, cfg, "staging"); err != nil || d.Source != envDeclYAML {
		t.Fatalf("staging = %+v, %v", d, err)
	}
	// An env neither declares is nobody's.
	if d, err := declareEnv(dir, cfg, "dev"); err != nil || d.Source != envDeclNone {
		t.Fatalf("dev = %+v, %v", d, err)
	}
}

func TestDeclareEnv_FlagsWithNoForgeFiles(t *testing.T) {
	prevP, prevC := obsProjectFlag, obsControlPlaneFlag
	t.Cleanup(func() { obsProjectFlag, obsControlPlaneFlag = prevP, prevC })
	obsProjectFlag, obsControlPlaneFlag = "shop", "https://cp.example.test/"

	d, err := declareEnv("", nil, "prod")
	if err != nil {
		t.Fatal(err)
	}
	f := yamlObsTarget(d).Facts
	if d.Source != envDeclFlags || d.Project != "shop" || f.ControlPlane != "https://cp.example.test" || !f.Enabled {
		t.Fatalf("declaration = %+v facts = %+v", d, f)
	}

	_, cfg := monitoringProject(t, "name: other\n")
	if _, err := declareEnv("", cfg, "prod"); err == nil {
		t.Fatal("--project disagreeing with forge.yaml's name was accepted")
	}
}

// Every obs verb resolves through resolveObsTarget: a forge.yaml env is
// answered without a KCL render (there is no deploy/kcl to render).
func TestResolveObsTarget_ForgeYAMLEnvNeedsNoKCL(t *testing.T) {
	dir, _ := monitoringProject(t, "name: shop\nenvs: [prod]\nmonitoring:\n  control_plane: https://cp.example.test\n")
	t.Chdir(dir)
	tgt, err := resolveObsTarget(t.Context(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Facts.ControlPlane != "https://cp.example.test" || tgt.Declaration() == nil || tgt.Declaration().Endpoint != "https://cp.example.test" {
		t.Fatalf("target = %+v", tgt)
	}
}

// emptyCloud answers every procedure with an empty message and records it.
type emptyCloud struct{ calls []fakeCloudCall }

func (e *emptyCloud) Call(_ context.Context, procedure string, req, out any) error {
	b, _ := json.Marshal(req)
	e.calls = append(e.calls, fakeCloudCall{procedure, string(b)})
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{}`), out)
}

// `forge obs changes <env>` on an env forge.yaml declares (no deploy/kcl):
// the seam resolves it to the control plane, and the ledger is read for the
// project forge.yaml names.
func TestObsChanges_ForgeYAMLEnvReadsTheControlPlane(t *testing.T) {
	dir, _ := monitoringProject(t, "name: shop\nenvs: [prod]\nmonitoring: true\n")
	t.Chdir(dir)
	ec := &emptyCloud{}
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: ec}, nil
		},
		Now: func() time.Time { return obsNow },
	}
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"obs", "changes", "prod", "--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out.String(), errb.String())
	}
	if scope, _ := decode(t, out.String())["scope"].(map[string]any); scope["backend"] != "cloud" {
		t.Fatalf("scope = %v", scope)
	}
	sawProject := false
	for _, c := range ec.calls {
		if strings.Contains(c.body, `"shop"`) {
			sawProject = true
		}
	}
	if !sawProject {
		t.Fatalf("no call named project shop: %+v", ec.calls)
	}
}
