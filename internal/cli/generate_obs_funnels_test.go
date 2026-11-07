package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsdash"
)

func funnelFrontend(env string, funnels ...FunnelEntity) renderedEnvFrontend {
	return renderedEnvFrontend{Env: env, Frontend: FrontendEntity{Name: "web", Telemetry: &FrontendTelemetryEntity{Funnels: funnels}}}
}

func TestDeclaredFunnels_SortedAndDeduplicatedAcrossEnvs(t *testing.T) {
	onboarding := FunnelEntity{ID: "onboarding", Title: "Onboarding", Steps: []string{"a", "b"}, Window: "7d", By: "user"}
	checkout := FunnelEntity{ID: "checkout", Steps: []string{"c", "d"}, Window: "1h", By: "session"}
	got, err := declaredFunnels([]renderedEnvFrontend{
		funnelFrontend("dev", onboarding, checkout),
		funnelFrontend("prod", checkout, onboarding),
		{Env: "prod", Frontend: FrontendEntity{Name: "admin"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "checkout" || got[1].ID != "onboarding" {
		t.Fatalf("funnels = %+v, want checkout then onboarding, once each", got)
	}
	if o := got[1]; o.Title != "Onboarding" || o.Window != "7d" || o.By != obsdash.FunnelByUser || len(o.Steps) != 2 {
		t.Errorf("onboarding = %+v, lost a field", o)
	}
}

func TestDeclaredFunnels_RefusesTwoDefinitionsUnderOneID(t *testing.T) {
	_, err := declaredFunnels([]renderedEnvFrontend{
		funnelFrontend("dev", FunnelEntity{ID: "f", Steps: []string{"a", "b"}, Window: "1h", By: "session"}),
		funnelFrontend("prod", FunnelEntity{ID: "f", Steps: []string{"a", "c"}, Window: "1h", By: "session"}),
	})
	if err == nil || !strings.Contains(err.Error(), `funnel "f" is declared twice`) || !strings.Contains(err.Error(), `"dev"`) || !strings.Contains(err.Error(), `"prod"`) {
		t.Fatalf("err = %v, want a refusal naming the funnel and both envs", err)
	}
}

// TestDeclaredFunnels_FromRealKCL is the whole path: funnels written in an env's
// KCL reach the frontend dashboard as funnel panels.
func TestDeclaredFunnels_FromRealKCL(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in the full tier")
	}
	dir := workloadURLProject(t, "dev", `    project = "acme"
    frontends = [forge.Frontend {
        name = "web", path = "frontends/web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [
            forge.Funnel {id = "onboarding", title = "Onboarding", steps = ["signup_started", "first_chat"], window = "7d", by = "user"}
            forge.Funnel {id = "checkout", steps = ["cart_viewed", "paid"]}
        ]}
    }]`)
	if err := os.MkdirAll(filepath.Join(dir, "frontends", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	funnels, err := declaredFunnels(renderedFrontends(dir))
	if err != nil {
		t.Fatal(err)
	}
	spec := obsdash.Build(obsdash.Input{Project: "acme", Frontends: []string{"web"}, Funnels: funnels})
	panels := map[string]obsdash.Panel{}
	for _, d := range spec.Dashboards {
		for _, p := range d.Panels {
			panels[p.ID] = p
		}
	}
	on, ok := panels["funnel-onboarding"]
	if !ok || on.Title != "Onboarding" || on.Funnel == nil || on.Funnel.Window != "7d" || on.Funnel.By != obsdash.FunnelByUser {
		t.Errorf("funnel-onboarding = %+v", on)
	}
	co, ok := panels["funnel-checkout"]
	if !ok || co.Title != "checkout" || co.Funnel == nil || co.Funnel.Window != obsdash.DefaultFunnelWindow || co.Funnel.By != obsdash.DefaultFunnelBy {
		t.Errorf("funnel-checkout = %+v, want the KCL defaults", co)
	}
}

// TestFrontendTelemetry_DecodesFromRealKCL: every knob of
// forge.BrowserTelemetry reaches the entity the deploy reads, including
// `sourcemaps`, which FrontendEntity carried but no KCL could set.
func TestFrontendTelemetry_DecodesFromRealKCL(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in the full tier")
	}
	dir := workloadURLProject(t, "dev", `    project = "acme"
    frontends = [
        forge.Frontend {
            name = "web", path = "frontends/web", type = "vite", port = 5173
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {
                console_capture = False
                replay_sample_rate = 0.25
                sourcemaps = "public"
                errors = forge.FrontendErrors {ignore = ["AbortError", "re:^Failed to fetch"]}
            }
        }
        forge.Frontend {name = "plain", path = "frontends/plain", type = "vite", port = 5174, runtime = forge.OnHost {}}
    ]
    observability = forge.Observability {
        local_port = 4318
        retention = forge.ObservabilityRetention {replay_days = 7}
    }`)
	entities, err := RenderKCL(t.Context(), dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]FrontendEntity{}
	for _, f := range entities.Frontends {
		by[f.Name] = f
	}
	web := by["web"]
	if web.Telemetry == nil || web.Telemetry.ConsoleCapture || web.Telemetry.ReplaySampleRate != 0.25 ||
		!reflect.DeepEqual(web.Telemetry.Errors.Ignore, []string{"AbortError", "re:^Failed to fetch"}) {
		t.Errorf("telemetry = %+v", web.Telemetry)
	}
	if web.Sourcemaps != "public" {
		t.Errorf("Sourcemaps = %q, want public: the KCL knob must reach the deploy", web.Sourcemaps)
	}
	// F2's deploy path: the knob must reach the stage input of every target
	// that stages a build, not a parallel field.
	if got := frontendToStaticSite(web).Sourcemaps; got != "public" {
		t.Errorf("StaticSiteFrontend.Sourcemaps = %q, want public", got)
	}
	fbWeb := web
	fbWeb.Runtime.Firebase = &FirebaseRuntime{}
	if got := frontendToFirebase(fbWeb).Sourcemaps; got != "public" {
		t.Errorf("FirebaseFrontend.Sourcemaps = %q, want public", got)
	}
	if got := frontendToStaticSite(by["plain"]).Sourcemaps; got != "" {
		t.Errorf("an undeclared frontend must stage with the default (empty = publish), got %q", got)
	}
	if web.RuntimeConfig["CONSOLE_CAPTURE"] != "false" || web.RuntimeConfig["REPLAY_SAMPLE_RATE"] != "0.25" {
		t.Errorf("runtime_config = %v, want the two knobs the browser reads", web.RuntimeConfig)
	}
	if plain := by["plain"]; plain.Telemetry != nil || plain.Sourcemaps != "" || len(plain.RuntimeConfig) != 0 {
		t.Errorf("a frontend that declares nothing must render nothing extra: %+v", plain)
	}
	if r := entities.Observability.Retention; r.ReplayDays != 7 || r.ErrorsDays != 30 || r.ProductEventsDays != 90 {
		t.Errorf("retention = %+v, want replay 7 with the stated defaults for the rest", r)
	}
	if got := shapeFrontendTelemetryOf(entities); len(got) != 1 || got[0].Frontend != "web" || got[0].Sourcemaps != "public" || len(got[0].ErrorsIgnore) != 2 {
		t.Errorf("shape telemetry = %+v", got)
	}
	if r := shapeRetentionOf(entities); r == nil || r.ReplayDays != 7 {
		t.Errorf("shape retention = %+v", r)
	}
}
