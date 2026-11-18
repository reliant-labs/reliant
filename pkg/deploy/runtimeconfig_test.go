package deploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func strp(s string) *string { return &s }

func resolverOf(urls map[string]string) WorkloadURLResolver {
	return func(name string) (string, error) {
		if u, ok := urls[name]; ok {
			return u, nil
		}
		return "", errors.New("no such workload")
	}
}

// TestResolveRuntimeConfig: literals pass through (including an explicit
// empty string), references resolve through the resolver, and EVERY dangling
// reference is reported in one error.
func TestResolveRuntimeConfig(t *testing.T) {
	rc := map[string]v1alpha1.RuntimeConfigValue{
		"API_URL":  {WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "api"}},
		"FEATURE":  {Value: strp("")},
		"APP_NAME": {Value: strp("acme")},
	}
	got, err := ResolveRuntimeConfig(rc, resolverOf(map[string]string{"api": "https://api.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"API_URL": "https://api.example.com", "FEATURE": "", "APP_NAME": "acme"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	rc["OTHER"] = v1alpha1.RuntimeConfigValue{WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "ghost"}}
	rc["API_URL"] = v1alpha1.RuntimeConfigValue{WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "missing"}}
	_, err = ResolveRuntimeConfig(rc, resolverOf(nil))
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), `"missing"`) {
		t.Fatalf("err = %v, want both dangling references named", err)
	}

	// Both channels set is refused before anything resolves.
	bad := map[string]v1alpha1.RuntimeConfigValue{"X": {Value: strp("a"), WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "api"}}}
	if _, err := ResolveRuntimeConfig(bad, resolverOf(map[string]string{"api": "u"})); err == nil {
		t.Fatal("both value and workloadURL resolved; want a refusal")
	}
}

// TestRuntimeConfigJSIsByteStable pins the exact document shape both the
// operator and forge write, and that it does not depend on map order.
func TestRuntimeConfigJSIsByteStable(t *testing.T) {
	a, err := RuntimeConfigJS(map[string]string{"B": "2", "A": "1", "URL": "https://x/</script>"})
	if err != nil {
		t.Fatal(err)
	}
	want := "window.__FORGE_CONFIG__ = {\"A\":\"1\",\"B\":\"2\",\"URL\":\"https://x/\\u003c/script\\u003e\"};\n"
	if a != want {
		t.Fatalf("doc =\n%s\nwant\n%s", a, want)
	}
	empty, _ := RuntimeConfigJS(nil)
	if empty != "window.__FORGE_CONFIG__ = {};\n" {
		t.Fatalf("empty doc = %q", empty)
	}
}

// TestRenderRefusesUnresolvedWorkloadURL: a pod cannot carry a reference, and
// rendering one as an empty variable would silently break CORS. Render
// refuses; ResolveEnvWorkloadURLs is the step that makes it renderable.
func TestRenderRefusesUnresolvedWorkloadURL(t *testing.T) {
	w := svc("api", httpPort(true))
	w.Spec.Env = []v1alpha1.EnvVar{{Name: "CORS_ORIGINS", WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "web"}}}
	if _, err := RenderWorkloads([]v1alpha1.Workload{w}, v1alpha1.ProfileFull, ctx); err == nil || !strings.Contains(err.Error(), "unresolved workloadURL") {
		t.Fatalf("err = %v, want an unresolved-reference refusal", err)
	}
	resolved, err := ResolveEnvWorkloadURLs(w.Spec.Env, resolverOf(map[string]string{"web": "https://web.example.com/app"}))
	if err != nil {
		t.Fatal(err)
	}
	if w.Spec.Env[0].WorkloadURL == nil {
		t.Fatal("ResolveEnvWorkloadURLs mutated its input")
	}
	w.Spec.Env = resolved
	objs := render(t, v1alpha1.ProfileFull, w)
	env := get(podOf(objects(t, objs)["Deployment/api"]), "containers", 0, "env", 0)
	if get(env, "value") != "https://web.example.com/app" {
		t.Fatalf("rendered env = %v", env)
	}
}
