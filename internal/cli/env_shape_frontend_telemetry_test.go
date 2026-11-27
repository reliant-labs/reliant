package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func obsWithRetention(replay, errs, events int) *ObservabilityEntity {
	o := &ObservabilityEntity{Enabled: true, Policy: &ObservabilityPolicyEntity{Enabled: true}}
	o.Retention.ReplayDays, o.Retention.ErrorsDays, o.Retention.ProductEventsDays = replay, errs, events
	return o
}

func TestShapeRetentionOf(t *testing.T) {
	if got := shapeRetentionOf(&KCLEntities{Observability: obsWithRetention(14, 30, 90)}); got != nil {
		t.Errorf("default retention = %+v, want nil so the shape encodes as it did", got)
	}
	got := shapeRetentionOf(&KCLEntities{Observability: obsWithRetention(7, 30, 90)})
	if want := (&release.ShapeRetention{ReplayDays: 7, ErrorsDays: 30, ProductEventsDays: 90}); !reflect.DeepEqual(got, want) {
		t.Errorf("retention = %+v, want %+v", got, want)
	}
	// A render that predates the fields decodes to zeros: that is the defaults.
	if got := shapeRetentionOf(&KCLEntities{Observability: obsWithRetention(0, 0, 0)}); got != nil {
		t.Errorf("zero retention (an older render) = %+v, want the defaults (nil)", got)
	}
	if shapeRetentionOf(nil) != nil || shapeRetentionOf(&KCLEntities{}) != nil {
		t.Error("no observability declares no retention")
	}
}

// Retention alone must reach the shape: an env that opts out of nothing and
// moves only a TTL still records a policy.
func TestShapeObservabilityPolicyOf_CarriesRetentionAlone(t *testing.T) {
	if p := shapeObservabilityPolicyOf(&KCLEntities{Observability: obsWithRetention(14, 30, 90)}); p != nil {
		t.Errorf("an env that moves nothing records no policy, got %+v", p)
	}
	p := shapeObservabilityPolicyOf(&KCLEntities{Observability: obsWithRetention(14, 90, 90)})
	if p == nil || p.Retention == nil || p.Retention.ErrorsDays != 90 || !p.Enabled {
		t.Fatalf("policy = %+v, want enabled with errors_days 90", p)
	}
}

func TestShapeFrontendTelemetryOf(t *testing.T) {
	var tel FrontendTelemetryEntity
	tel.ConsoleCapture, tel.ReplaySampleRate = true, 1
	tel.Errors.Ignore = []string{"AbortError", "re:^Failed"}
	tel.Sourcemaps = "off"
	tel.Funnels = []FunnelEntity{{ID: "onboarding", Title: "Onboarding", Steps: []string{"a", "b"}, Window: "7d", By: "user"}}
	e := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Telemetry: &tel},
		{Name: "plain"},
		{Name: "defaults", Telemetry: &FrontendTelemetryEntity{ConsoleCapture: true, ReplaySampleRate: 1}},
		// console_capture and replay_sample_rate are runtime config, not shape:
		// a frontend that moves only those declares nothing the platform acts on.
		{Name: "quiet", Telemetry: &FrontendTelemetryEntity{ConsoleCapture: false, ReplaySampleRate: 0.1}},
	}}
	got := shapeFrontendTelemetryOf(e)
	want := []release.ShapeFrontendTelemetry{{
		Frontend: "web", Sourcemaps: "off", ErrorsIgnore: []string{"AbortError", "re:^Failed"},
		Funnels: []release.ShapeFunnel{{ID: "onboarding", Title: "Onboarding", Steps: []string{"a", "b"}, Window: "7d", By: "user"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shapeFrontendTelemetryOf =\n%+v\nwant\n%+v", got, want)
	}
	if shapeFrontendTelemetryOf(nil) != nil {
		t.Error("nil entities declare nothing")
	}
}
