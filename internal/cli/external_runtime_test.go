package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclrender"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func TestDispatchRuntime_External(t *testing.T) {
	rt, err := dispatchRuntime("api", json.RawMessage(`{"type":"external","status":["./status"],"deploy":["helm","upgrade"],"plan":null,"env":{"A":"1"},"pass_env":true,"timeout":60,"platform":"arm64"}`))
	if err != nil {
		t.Fatal(err)
	}
	x := rt.External
	if rt.Type != RuntimeExternal || x == nil || x.Plan != nil || len(x.Deploy) != 2 || !x.PassEnv || x.Timeout != 60 || x.ImagePlatform() != "arm64" {
		t.Fatalf("decoded %+v / %+v", rt, x)
	}
	// The Go half of the schema's checks runs on decode.
	if _, err := dispatchRuntime("api", json.RawMessage(`{"type":"external","deploy":[]}`)); err == nil || !strings.Contains(err.Error(), "empty argv") {
		t.Errorf("an empty hook must be refused at decode, got %v", err)
	}
}

type mapResolver map[string]string

func (m mapResolver) Resolve(name string) (string, bool) { v, ok := m[name]; return v, ok }

func TestExternalSpecFor_PassEnv(t *testing.T) {
	w := WorkloadEntity{
		Name:    "api",
		Runtime: RuntimeEntity{Type: RuntimeExternal, External: &deploytarget.ExternalRuntime{PassEnv: true}},
		Spec: deployv1alpha1.WorkloadSpec{Image: "ghcr.io/acme/api@sha256:aa", Env: []deployv1alpha1.EnvVar{
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "DB_PASSWORD", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "db", Key: "password"}},
			{Name: "API_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "api", Key: "key"}},
		}},
	}
	var warn bytes.Buffer
	s := externalSpecFor("prod", w, &KCLEntities{Project: "acme"}, mapResolver{"DB_PASSWORD": "s3cret"}, &warn)
	if s.Project != "acme" || s.EnvName != "prod" || s.Image != "ghcr.io/acme/api@sha256:aa" {
		t.Errorf("spec = %+v", s)
	}
	if s.WorkloadEnv["LOG_LEVEL"] != "info" || s.WorkloadEnv["DB_PASSWORD"] != "s3cret" {
		t.Errorf("workload env = %v", s.WorkloadEnv)
	}
	if _, passed := s.WorkloadEnv["API_KEY"]; passed {
		t.Error("an unresolved secret must not be passed (empty would read as a real value)")
	}
	if !strings.Contains(warn.String(), "API_KEY") {
		t.Errorf("an unresolved secret must be named; warned %q", warn.String())
	}
	w.Runtime.External.PassEnv = false
	if s := externalSpecFor("prod", w, nil, mapResolver{}, nil); s.WorkloadEnv != nil {
		t.Errorf("pass_env off must pass nothing, got %v", s.WorkloadEnv)
	}
}

func TestBuildDeployGroups_ExternalGroup(t *testing.T) {
	rt := deploytarget.ExternalRuntime{Deploy: []string{"./deploy.sh"}}
	entities := &KCLEntities{Project: "acme", Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{Type: RuntimeExternal, External: &rt}, Spec: deployv1alpha1.WorkloadSpec{Image: "ghcr.io/acme/api@sha256:aa"}},
		{Name: "worker", Runtime: RuntimeEntity{Type: RuntimeExternal, External: &deploytarget.ExternalRuntime{}}, Spec: deployv1alpha1.WorkloadSpec{Image: "ghcr.io/acme/worker:v1"}},
	}}
	groups, err := buildDeployGroupsForEnv("prod", entities, "acme", "v1.2.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ProviderID != deploytarget.ExternalProviderID || len(groups[0].Services) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	stampExternalRelease(kclrender.WithRelease(context.Background(), "v1.2.3"), groups)
	api := groups[0].Services[0].External
	if api.Workload != "api" || api.EnvName != "prod" || api.Version != "v1.2.3" || api.Release != "v1.2.3" || api.Digest() != "sha256:aa" {
		t.Errorf("api spec = %+v", api)
	}
	if !groups[0].Services[1].External.Runtime.RecordOnly() {
		t.Error("worker binds no hooks: record-only")
	}
}
