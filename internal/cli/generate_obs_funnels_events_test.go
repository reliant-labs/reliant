package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Observability.analytics funnels join the frontends' (one panel per id), keep
// their forge.Step filters, and their counts are merged the same way.
func TestDeclaredFunnelsAndCountsFromProjectAnalytics(t *testing.T) {
	steps := []FunnelStepEntity{{Where: map[string]string{"first_for_org": "true"}}, {}, {Where: map[string]string{"first_for_org": "false"}, Within: "14d"}}
	org := FunnelEntity{ID: "forge-deploy", Steps: []string{"deploy_succeeded", "claim_completed", "deploy_succeeded"}, Window: "30d", By: "org", StepFilters: steps}
	analytics := []renderedEnvAnalytics{
		{Env: "prod", Analytics: ProjectAnalyticsEntity{Funnels: []FunnelEntity{org}, Counts: []CountEntity{{ID: "deploys", Event: "deploy_succeeded", GroupBy: []string{"mode"}}}}},
		{Env: "staging", Analytics: ProjectAnalyticsEntity{Funnels: []FunnelEntity{org}, Counts: []CountEntity{{ID: "deploys", Event: "deploy_succeeded", GroupBy: []string{"mode"}}}}},
	}
	web := renderedEnvFrontend{Env: "prod", Frontend: FrontendEntity{Name: "web", Telemetry: &FrontendTelemetryEntity{
		Funnels: []FunnelEntity{{ID: "onboarding", Steps: []string{"a", "b"}, Window: "1h", By: "session"}},
	}}}
	funnels, err := declaredFunnels([]renderedEnvFrontend{web}, analytics...)
	if err != nil {
		t.Fatal(err)
	}
	if len(funnels) != 2 || funnels[0].ID != "forge-deploy" || funnels[1].ID != "onboarding" {
		t.Fatalf("funnels = %+v", funnels)
	}
	if f := funnels[0]; f.By != "org" || len(f.StepFilters) != 3 || f.StepFilters[2].Within != "14d" || f.StepFilters[0].Where["first_for_org"] != "true" {
		t.Errorf("the org funnel lost its steps' filters: %+v", f)
	}
	counts, err := declaredCounts(analytics)
	if err != nil || len(counts) != 1 || counts[0].Event != "deploy_succeeded" {
		t.Fatalf("counts = %+v, %v", counts, err)
	}

	changed := org
	changed.Window = "7d"
	_, err = declaredFunnels(nil, append(analytics, renderedEnvAnalytics{Env: "dev", Analytics: ProjectAnalyticsEntity{Funnels: []FunnelEntity{changed}}})...)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("two definitions of one id must be refused, got %v", err)
	}
	_, err = declaredCounts(append(analytics, renderedEnvAnalytics{Env: "dev", Analytics: ProjectAnalyticsEntity{Counts: []CountEntity{{ID: "deploys", Event: "other"}}}}))
	if err == nil {
		t.Error("two definitions of one count id must be refused")
	}
}

// Every env's user-activity rules are the project's: their union, validated,
// and what `forge obs users` reads back from the generated spec.
func TestDeclaredActivityIsTheUnionOfEveryEnv(t *testing.T) {
	analytics := []renderedEnvAnalytics{
		{Env: "prod", Analytics: ProjectAnalyticsEntity{NotActivity: []string{"billing_subscription_state"}, ExcludeUsersWith: []string{"internal_user"}}},
		{Env: "staging", Analytics: ProjectAnalyticsEntity{NotActivity: []string{"claimable_account_created", "billing_subscription_state"}}},
	}
	got, err := declaredActivity(analytics)
	if err != nil {
		t.Fatal(err)
	}
	want := obsquery.Activity{NotActivity: []string{"billing_subscription_state", "claimable_account_created"}, ExcludeUsersWith: []string{"internal_user"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declaredActivity = %+v, want %+v", got, want)
	}
	if _, err := declaredActivity([]renderedEnvAnalytics{{Env: "dev", Analytics: ProjectAnalyticsEntity{ExcludeUsersWith: []string{"bad name"}}}}); err == nil {
		t.Error("a rule that names no event must be refused")
	}

	dir := t.TempDir()
	if got := declaredUserActivity(dir); !got.IsZero() {
		t.Errorf("a project with no generated spec has rules: %+v", got)
	}
	if _, err := obsdash.Write(dir, obsdash.Input{Project: "shop", Activity: want}, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	if got := declaredUserActivity(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("declaredUserActivity = %+v, want the generated rules %+v", got, want)
	}
}
