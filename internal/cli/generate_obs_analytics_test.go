package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

func analyticsFrontend(env, name string, a *AnalyticsEntity) renderedEnvFrontend {
	return renderedEnvFrontend{Env: env, Frontend: FrontendEntity{Name: name, Telemetry: &FrontendTelemetryEntity{Analytics: a}}}
}

func TestDeclaredAnalytics_NothingDeclaredIsNil(t *testing.T) {
	got, err := declaredAnalytics([]renderedEnvFrontend{
		{Env: "dev", Frontend: FrontendEntity{Name: "web"}},
		funnelFrontend("prod", FunnelEntity{ID: "f", Steps: []string{"a", "b"}, Window: "1h", By: "session"}),
	})
	if err != nil || got != nil {
		t.Fatalf("declaredAnalytics = %+v, %v; want nil, nil", got, err)
	}
	if got.EffectiveActivationWindow() != obsdash.DefaultActivationWindow || !got.DashboardsEnabled() {
		t.Errorf("a nil declaration must read as every default")
	}
}

func TestDeclaredAnalytics_MergesAcrossEnvsAndFrontends(t *testing.T) {
	checkout := AnalyticsGoalEntity{ID: "checkout", Title: "Checkout", Event: "purchase"}
	pricing := AnalyticsGoalEntity{ID: "pricing", Route: "/pricing"}
	hn := AnalyticsChannelEntity{Host: "news.ycombinator.com", Channel: "Organic social"}
	podcast := AnalyticsChannelEntity{Medium: "podcast", Channel: "Podcasts"}
	got, err := declaredAnalytics([]renderedEnvFrontend{
		analyticsFrontend("dev", "web", &AnalyticsEntity{Mode: "off", Goals: []AnalyticsGoalEntity{pricing, checkout}, Channels: []AnalyticsChannelEntity{hn}, Dashboards: true}),
		analyticsFrontend("prod", "web", &AnalyticsEntity{Mode: "full", Goals: []AnalyticsGoalEntity{checkout}, Activation: "order_created", ActivationWindow: "14d", Channels: []AnalyticsChannelEntity{hn, podcast}, Dashboards: true}),
		// A frontend that leaves activation unset defers to the others.
		analyticsFrontend("prod", "admin", &AnalyticsEntity{Mode: "aggregate", Dashboards: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &obsdash.ProductAnalytics{
		Goals: []obsdash.Goal{
			{ID: "checkout", Title: "Checkout", Event: "purchase"},
			{ID: "pricing", Route: "/pricing"},
		},
		Activation:       "order_created",
		ActivationWindow: "14d",
		Channels: []obsdash.ChannelRule{
			{Host: "news.ycombinator.com", Channel: "Organic social"},
			{Medium: "podcast", Channel: "Podcasts"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declaredAnalytics =\n%+v\nwant\n%+v", got, want)
	}
	if got.EffectiveActivationWindow() != "14d" || !got.DashboardsEnabled() {
		t.Errorf("effective window %q, dashboards %v", got.EffectiveActivationWindow(), got.DashboardsEnabled())
	}
}

func TestDeclaredAnalytics_DashboardsOptOutWins(t *testing.T) {
	got, err := declaredAnalytics([]renderedEnvFrontend{
		analyticsFrontend("dev", "web", &AnalyticsEntity{Mode: "aggregate", Dashboards: true}),
		analyticsFrontend("prod", "web", &AnalyticsEntity{Mode: "aggregate", Dashboards: false}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DashboardsEnabled() {
		t.Error("a declared dashboards = False must drop the built-in dashboards")
	}
}

func TestDeclaredAnalytics_RefusesConflicts(t *testing.T) {
	for name, tc := range map[string]struct {
		a, b *AnalyticsEntity
		want string
	}{
		"goal": {
			&AnalyticsEntity{Goals: []AnalyticsGoalEntity{{ID: "g", Event: "purchase"}}, Dashboards: true},
			&AnalyticsEntity{Goals: []AnalyticsGoalEntity{{ID: "g", Route: "/thanks"}}, Dashboards: true},
			`analytics goal "g" is declared twice`,
		},
		"activation": {
			&AnalyticsEntity{Activation: "order_created", Dashboards: true},
			&AnalyticsEntity{Activation: "project_created", Dashboards: true},
			`analytics activation is "order_created"`,
		},
		"activation window": {
			&AnalyticsEntity{ActivationWindow: "7d", Dashboards: true},
			&AnalyticsEntity{ActivationWindow: "14d", Dashboards: true},
			`analytics activation_window is "7d"`,
		},
		"channel": {
			&AnalyticsEntity{Channels: []AnalyticsChannelEntity{{Medium: "podcast", Channel: "Podcasts"}}, Dashboards: true},
			&AnalyticsEntity{Channels: []AnalyticsChannelEntity{{Medium: "podcast", Channel: "Audio"}}, Dashboards: true},
			`analytics channel rule medium="podcast" credits "Podcasts"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := declaredAnalytics([]renderedEnvFrontend{analyticsFrontend("dev", "web", tc.a), analyticsFrontend("prod", "web", tc.b)})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"dev"`) || !strings.Contains(err.Error(), `"prod"`) {
				t.Fatalf("err = %v, want %q naming both envs", err, tc.want)
			}
		})
	}
}

// TestFrontendDashboardLeavesProductFunnelsToTheProductDashboard: a funnel
// counted by visitor or person reads product_events, which the frontend
// dashboard's funnel panel (browser spans, sessions and users) cannot count.
func TestFrontendDashboardLeavesProductFunnelsToTheProductDashboard(t *testing.T) {
	funnels, err := declaredFunnels([]renderedEnvFrontend{funnelFrontend("prod",
		FunnelEntity{ID: "buy", Steps: []string{"view_product", "purchase"}, Window: "7d", By: "person"},
		FunnelEntity{ID: "land", Steps: []string{"page_viewed", "signup_started"}, Window: "1d", By: "visitor"},
		FunnelEntity{ID: "onboarding", Steps: []string{"a", "b"}, Window: "1h", By: "session"},
	)})
	if err != nil {
		t.Fatal(err)
	}
	if len(funnels) != 3 {
		t.Fatalf("declaredFunnels dropped a funnel: %+v", funnels)
	}
	spec := obsdash.Build(obsdash.Input{Project: "acme", Frontends: []string{"web"}, Funnels: funnels})
	var ids []string
	for _, d := range spec.Dashboards {
		for _, p := range d.Panels {
			if strings.HasPrefix(p.ID, "funnel-") {
				ids = append(ids, p.ID)
			}
		}
	}
	if !reflect.DeepEqual(ids, []string{"funnel-onboarding"}) {
		t.Errorf("frontend dashboard funnel panels = %v, want only the session funnel", ids)
	}
}

// TestFrontendAnalytics_FromRealKCL is the whole path: forge.Analytics written
// in an env's KCL reaches the entity the deploy reads, the runtime config the
// browser reads, the shape the platform records, and the declaration the
// built-in dashboards read.
func TestFrontendAnalytics_FromRealKCL(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in the full tier")
	}
	dir := workloadURLProject(t, "dev", `    project = "acme"
    frontends = [
        forge.Frontend {
            name = "web", path = "frontends/web", type = "vite", port = 5173
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {
                analytics = forge.Analytics {
                    mode = "aggregate"
                    goals = [forge.Goal {id = "pricing", route = "/pricing"}, forge.Goal {id = "checkout", event = "purchase"}]
                    activation = "order_created"
                    channels = [forge.Channel {host = "News.YCombinator.com", channel = "Organic social"}]
                }
                funnels = [forge.Funnel {id = "buy", steps = ["view_product", "purchase"], by = "person", window = "7d"}]
            }
        }
        forge.Frontend {name = "plain", path = "frontends/plain", type = "vite", port = 5174, runtime = forge.OnHost {}}
    ]`)
	for _, fe := range []string{"web", "plain"} {
		if err := os.MkdirAll(filepath.Join(dir, "frontends", fe), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entities, err := RenderKCL(t.Context(), dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]FrontendEntity{}
	for _, f := range entities.Frontends {
		by[f.Name] = f
	}
	web := by["web"]
	wantEntity := &AnalyticsEntity{
		Mode:       "aggregate",
		Goals:      []AnalyticsGoalEntity{{ID: "pricing", Route: "/pricing"}, {ID: "checkout", Event: "purchase"}},
		Activation: "order_created",
		Channels:   []AnalyticsChannelEntity{{Host: "news.ycombinator.com", Channel: "Organic social"}},
		Dashboards: true,
	}
	if web.Telemetry == nil || !reflect.DeepEqual(web.Telemetry.Analytics, wantEntity) {
		t.Fatalf("analytics = %+v, want %+v", web.Telemetry, wantEntity)
	}
	if web.RuntimeConfig["ANALYTICS_MODE"] != "aggregate" {
		t.Errorf("runtime_config = %v, want ANALYTICS_MODE aggregate (the opt-down renders; the default identified does not)", web.RuntimeConfig)
	}
	if plain := by["plain"]; plain.Telemetry != nil || len(plain.RuntimeConfig) != 0 {
		t.Errorf("a frontend that declares nothing must render nothing extra: %+v", plain)
	}
	if got := shapeFrontendTelemetryOf(entities); len(got) != 1 || len(got[0].Funnels) != 1 || got[0].Funnels[0].By != release.FunnelByPerson {
		t.Errorf("shape telemetry = %+v, want the person funnel", got)
	}
	a, err := declaredAnalytics(renderedFrontends(dir))
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || len(a.Goals) != 2 || a.Goals[0].ID != "checkout" || a.Activation != "order_created" || a.EffectiveActivationWindow() != "7d" || len(a.Channels) != 1 {
		t.Errorf("declaredAnalytics = %+v", a)
	}
	project, _ := obsdash.BuildParts(obsdash.Input{Project: "acme", Frontends: []string{"web"}, Analytics: a})
	if project.Input == nil || !reflect.DeepEqual(project.Input.Analytics, a) {
		t.Errorf("the project part's fragments do not carry the analytics a bundle-time build reads: %+v", project.Input)
	}
}
