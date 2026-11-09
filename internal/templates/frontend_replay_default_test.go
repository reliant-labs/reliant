package templates

import (
	"strings"
	"testing"
)

// The default belongs to NEW owned environment declarations, never the SDK
// or the schema: upgrading a pin must not start recording existing users.
func TestNewBrowserEnvironmentsDeclareReplayAtBothEnds(t *testing.T) {
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
					for _, want := range []string{"_session_replay = True", "replay = _session_replay", `OTEL_REPLAY = "true" if`, `else "false"`, "DNT/GPC"} {
						if !strings.Contains(string(out), want) {
							t.Errorf("missing %q", want)
						}
					}
					if strings.Contains(string(out), "VITE_OTEL_REPLAY") || strings.Contains(string(out), "NEXT_PUBLIC_OTEL_REPLAY") {
						t.Fatal("environment policy must not be baked into a promoted build")
					}
					if strings.Contains(tmpl, "cloud") && !strings.Contains(string(out), "runtime_config = f.runtime_config | _api_config") {
						t.Fatal("hosted binder discarded the frontend's replay config")
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
