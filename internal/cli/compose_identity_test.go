package cli

import (
	"path/filepath"
	"testing"
)

func TestComposeIdentityIncludesExactCheckoutAndEnv(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "one", "project"), filepath.Join(root, "two", "project")
	cm := &ComposeRuntime{Service: "clickstack", Env: map[string]string{"CLICKSTACK_OTLP_GRPC_PORT": "14317"}}
	first := composeSpecFor(a, "dev", cm)
	for _, other := range []*struct{ dir, env string }{{b, "dev"}, {a, "other"}} {
		got := composeSpecFor(other.dir, other.env, cm)
		if first.Env["COMPOSE_PROJECT_NAME"] == got.Env["COMPOSE_PROJECT_NAME"] {
			t.Fatalf("compose selects same project for %s/%s and %s/dev", other.dir, other.env, a)
		}
	}
	if first.ProjectDirectory != a || first.Env["CLICKSTACK_OTLP_GRPC_PORT"] != "14317" {
		t.Fatalf("lost resolved compose input: %+v", first)
	}
	if _, ok := cm.Env["COMPOSE_PROJECT_NAME"]; ok {
		t.Fatal("mutated declaration")
	}
}

// `docker compose up otel-profiles` recreates clickstack (a depends_on), so
// it must interpolate clickstack's declared ports too, not the file's
// defaults another stack may hold.
func TestComposeSpecCarriesTheWholeFilesEnv(t *testing.T) {
	stack := &ComposeRuntime{Service: "clickstack", Env: map[string]string{"CLICKSTACK_OTLP_GRPC_PORT": "14317", "SHARED": "theirs"}}
	profiles := &ComposeRuntime{Service: "otel-profiles", Env: map[string]string{"CLICKSTACK_PROFILES_OTLP_PORT": "14328", "SHARED": "mine"}}
	elsewhere := &ComposeRuntime{Service: "x", File: "other.yml", Env: map[string]string{"OTHER_FILE": "1"}}
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: stack}},
		{Name: "otel-profiles", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: profiles}},
		{Name: "x", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: elsewhere}},
	}}
	spec := composeSpecFor(t.TempDir(), "dev", profiles)
	withComposeFileEnv(spec, profiles, e)
	if spec.Env["CLICKSTACK_OTLP_GRPC_PORT"] != "14317" {
		t.Fatalf("the depended-on service's port is missing: %v", spec.Env)
	}
	if spec.Env["SHARED"] != "mine" || spec.Env["CLICKSTACK_PROFILES_OTLP_PORT"] != "14328" {
		t.Fatalf("the workload's own env must win: %v", spec.Env)
	}
	if _, ok := spec.Env["OTHER_FILE"]; ok {
		t.Fatal("env leaked from another compose file")
	}
	if len(profiles.Env) != 2 {
		t.Fatal("mutated declaration")
	}
}
