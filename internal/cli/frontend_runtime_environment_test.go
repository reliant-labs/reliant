package cli

import (
	"strings"
	"testing"
)

// Every web frontend forge writes a runtime config document for gets one that
// names its environment, because the generated otel_gen.ts reads ENVIRONMENT
// from config.js first — a build variable, which a promoted bundle would carry
// into every other environment, is only its deprecated fallback. A hosted frontend is left to the
// control plane, which writes its document; React Native has none.
func TestFrontendRuntimeOverlaysListEveryWebFrontendForgeWrites(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "dev", Type: "vite", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}},
		{Name: "site", Type: "nextjs", Runtime: FrontendRuntime{Type: FrontendRuntimeBucket}},
		{Name: "declared", Type: "vite", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}, RuntimeConfig: map[string]string{"APP_NAME": "acme"}},
		{Name: "hosted", Type: "vite", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}},
		{Name: "mobile", Type: "rn", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}},
	}}
	got := frontendRuntimeOverlays(entities)
	for _, name := range []string{"dev", "site", "declared"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s: no overlay entry, so forge would write it no document: %v", name, got)
		}
	}
	for _, name := range []string{"hosted", "mobile"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s: listed, but forge writes it no document: %v", name, got)
		}
	}
	if got["declared"]["APP_NAME"] != "acme" {
		t.Errorf("declared runtime_config lost: %v", got["declared"])
	}
}

func TestFrontendRuntimeDocsNameTheEnvironment(t *testing.T) {
	// No proto/config: no typed frontend config, so the overlays are the
	// whole document.
	docs, err := renderFrontendRuntimeDocsWith(t.TempDir(), "staging", map[string]map[string]string{
		"bare":     {},
		"declared": {"ENVIRONMENT": "canary", "APP_NAME": "acme"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc := docs["bare"]; !strings.Contains(doc, `"ENVIRONMENT": "staging"`) {
		t.Errorf("a frontend with no runtime_config must still get ENVIRONMENT:\n%s", doc)
	}
	if doc := docs["declared"]; !strings.Contains(doc, `"ENVIRONMENT": "canary"`) || !strings.Contains(doc, `"APP_NAME": "acme"`) {
		t.Errorf("a declared ENVIRONMENT must win over the env name:\n%s", doc)
	}
}
