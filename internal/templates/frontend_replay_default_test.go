package templates

import (
	"strings"
	"testing"
)

// The default belongs to NEW owned environment declarations, never the SDK
// or the schema: upgrading a pin must not start recording existing users.
func TestNewBrowserEnvironmentsDeclareReplayOnTheFrontend(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"vite", "nextjs"} {
		for _, env := range []string{"dev", "staging", "prod"} {
			for _, tmpl := range []string{"kcl/frontend-only/main.k.tmpl", "kcl/dev/main.k.tmpl", "kcl/cloud/main.k.tmpl"} {
				t.Run(kind+"/"+env+"/"+tmpl, func(t *testing.T) {
					out, err := DeployTemplates().Render(tmpl, EnvTemplateData{
						ProjectName: "shop", EnvName: env, HasFrontend: true, SessionReplay: true,
						FrontendName: "web", FrontendIdent: "web", FrontendType: kind,
						FrontendPath: ".", FrontendPublicDir: "out", PrimaryWorkload: "shop",
					})
					if err != nil {
						t.Fatal(err)
					}
					// One switch, on the frontend: telemetry.replay renders
					// OTEL_REPLAY and opts the env in. Nothing explicit beside
					// it (a runtime_config OTEL_REPLAY would outrank it).
					for _, want := range []string{"_session_replay = True", "telemetry = forge.BrowserTelemetry {replay = ", "_session_replay}", "DNT/GPC"} {
						if !strings.Contains(string(out), want) {
							t.Errorf("missing %q", want)
						}
					}
					for _, never := range []string{"OTEL_REPLAY = ", "ObservabilitySignals {replay"} {
						if strings.Contains(string(out), never) {
							t.Errorf("writes %q beside the per-frontend switch", never)
						}
					}
					if strings.Contains(string(out), "VITE_OTEL_REPLAY") || strings.Contains(string(out), "NEXT_PUBLIC_OTEL_REPLAY") {
						t.Fatal("environment policy must not be baked into a promoted build")
					}
					// The hosted binder lives in the shared env base: it must
					// keep the frontend's own runtime config (the replay
					// switch renders into it), adding only the API URL.
					if strings.Contains(tmpl, "cloud") {
						base, err := DeployTemplates().Render("kcl/base/env.k.tmpl", EnvTemplateData{ProjectName: "shop", HasFrontend: true, FrontendName: "web"})
						if err != nil {
							t.Fatal(err)
						}
						if !strings.Contains(string(out), "envbase.hosted_frontend(f, _api_config)") ||
							!strings.Contains(string(base), "runtime_config = f.runtime_config | api") {
							t.Fatal("hosted binder discarded the frontend's replay config")
						}
					}
				})
			}
		}
	}
}

func TestBackendOnlyEnvironmentsDoNotOptInToReplay(t *testing.T) {
	t.Parallel()
	for _, tmpl := range []string{"kcl/dev/main.k.tmpl", "kcl/cloud/main.k.tmpl"} {
		out, err := DeployTemplates().Render(tmpl, EnvTemplateData{ProjectName: "shop", EnvName: "prod", PrimaryWorkload: "shop"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "_session_replay") {
			t.Fatalf("%s opts a backend into replay", tmpl)
		}
	}
}
