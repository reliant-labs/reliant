package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// lifecycleResolver is a stub resolver that also declares a per-env lifecycle.
type lifecycleResolver struct {
	stubResolver
	lifecycles map[string]string
}

func (l lifecycleResolver) Lifecycle(_ context.Context, _, env string) string {
	return l.lifecycles[env]
}

func TestEnvStatusJSON_ReportsLifecycle(t *testing.T) {
	dir := t.TempDir()
	declareEnvDir(t, dir, "dev")
	declareEnvDir(t, dir, "prod")
	resolver := lifecycleResolver{lifecycles: map[string]string{"dev": "local"}}

	// All-envs form: one entry per env under .environments[].lifecycle.
	opts := topologyBindings(t, envTopologyOptions{ProjectDir: dir, Resolver: resolver}, newMemBindingStore(map[string]release.Promotion{}))
	report, _, out := runTopologyJSON(t, []string{"dev", "prod"}, opts)
	got := map[string]string{}
	for _, e := range report.Environments {
		got[e.Env] = e.Lifecycle
	}
	if got["dev"] != "local" || got["prod"] != "" {
		t.Errorf("lifecycles = %v, want dev=local prod=\"\"", got)
	}
	if !strings.Contains(out, `"lifecycle":"local"`) && !strings.Contains(out, `"lifecycle": "local"`) {
		t.Errorf("raw JSON lacks lifecycle local:\n%s", out)
	}

	// Single-env form: top-level .lifecycle, present even when empty and unbound.
	for env, want := range map[string]string{"dev": "local", "prod": ""} {
		t.Chdir(dir)
		var err error
		raw := captureStdout(t, func() {
			err = runEnvStatusRelease(context.Background(), env, envStatusOptions{
				JSON: true, Bindings: newMemBindingStore(nil),
				Lister: &stubLister{}, Resolver: resolver,
			})
		})
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		var doc map[string]any
		if jerr := json.Unmarshal([]byte(raw), &doc); jerr != nil {
			t.Fatalf("%s: %v\n%s", env, jerr, raw)
		}
		if v, ok := doc["lifecycle"]; !ok || v != want {
			t.Errorf("%s: lifecycle = %v (present=%v), want %q", env, v, ok, want)
		}
	}
}
