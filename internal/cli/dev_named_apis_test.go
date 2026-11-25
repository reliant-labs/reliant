package cli

import (
	"reflect"
	"strings"
	"testing"
)

// resolveDevAPIUpstreams hands each dev server the upstreams of its named
// same-origin APIs AFTER host ports are settled: a host workload's port wins
// over what render resolved, any other target keeps render's URL, and a name
// with neither is left out (the dev server answers it 404).
func TestResolveDevAPIUpstreams(t *testing.T) {
	ports := func(p ...int) *[]int { return &p }
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "billing-api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: ports(43123)}}},
			{Name: "auth", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
		},
		Frontends: []FrontendEntity{
			{
				Name:    "web",
				APIs:    map[string]string{"billing": "billing-api", "auth": "auth", "search": "search"},
				APIURLs: map[string]string{"billing": "http://localhost:1", "search": "http://search.localhost:8081"},
			},
			{Name: "plain"},
		},
	}
	resolveDevAPIUpstreams(e)
	want := map[string]string{"billing": "http://localhost:43123", "search": "http://search.localhost:8081"}
	if got := e.Frontends[0].DevAPIUpstreams; !reflect.DeepEqual(got, want) {
		t.Errorf("DevAPIUpstreams = %v, want %v", got, want)
	}
	if e.Frontends[1].DevAPIUpstreams != nil {
		t.Errorf("a frontend without apis got upstreams: %v", e.Frontends[1].DevAPIUpstreams)
	}
	if got := devAPIsEnvValue(want); got != `{"billing":"http://localhost:43123","search":"http://search.localhost:8081"}` {
		t.Errorf("FORGE_DEV_APIS = %s", got)
	}
	if devAPIsEnvValue(nil) != "" {
		t.Error("no upstreams must set no FORGE_DEV_APIS")
	}
}

// The dev server is launched with FORGE_DEV_APIS in its lowest layer: the
// frontend's KCL env_vars outrank it, and the developer's shell does when it
// names it in FORGE_ENV_OVERRIDE.
func TestBuildFrontendCmd_SetsDevAPIs(t *testing.T) {
	fe := FrontendEntity{Name: "web", Type: "vite", Path: t.TempDir(), DevAPIUpstreams: map[string]string{"billing": "http://localhost:43123"}}
	cmd := buildFrontendCmd(t.Context(), fe, "dev", nil, nil, "")
	if !lastEnvIs(cmd.Env, `FORGE_DEV_APIS={"billing":"http://localhost:43123"}`) {
		t.Errorf("dev server env lacks FORGE_DEV_APIS: %v", cmd.Env)
	}
	cmd = buildFrontendCmd(t.Context(), fe, "dev", []string{`FORGE_DEV_APIS={"billing":"http://elsewhere"}`, "FORGE_ENV_OVERRIDE=FORGE_DEV_APIS"}, nil, "")
	if !lastEnvIs(cmd.Env, `FORGE_DEV_APIS={"billing":"http://elsewhere"}`) {
		t.Errorf("the shell's FORGE_DEV_APIS must win: %v", cmd.Env)
	}
}

func lastEnvIs(env []string, kv string) bool {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], key) {
			return env[i] == kv
		}
	}
	return false
}

// A frontend forge writes config.js for gets its named APIs as the
// document's `apis` object, each at its workload's own URL; a hosted one
// gets nothing (the control plane writes its document).
func TestFrontendRuntimeOverlays_NamedAPIs(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "site", Type: "vite", Runtime: FrontendRuntime{Type: FrontendRuntimeBucket}, APIURLs: map[string]string{"billing": "https://billing.acme.test"}},
		{Name: "hosted", Type: "vite", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}, APIs: map[string]string{"billing": "billing-api"}},
	}}
	got := frontendRuntimeOverlays(entities)
	if apis, _ := got["site"]["apis"].(map[string]string); apis["billing"] != "https://billing.acme.test" {
		t.Errorf("site overlay = %v, want apis.billing", got["site"])
	}
	if _, ok := got["hosted"]; ok {
		t.Errorf("hosted frontend listed: %v", got["hosted"])
	}
	docs, err := renderFrontendRuntimeDocsWith(t.TempDir(), "prod", got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs["site"], `"billing": "https://billing.acme.test"`) || !strings.Contains(docs["site"], `"apis": {`) {
		t.Errorf("config.js lacks the apis object:\n%s", docs["site"])
	}
}
