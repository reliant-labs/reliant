package release

import (
	"errors"
	"reflect"
	"testing"
)

func productFunnelTelemetry() []ShapeFrontendTelemetry {
	return []ShapeFrontendTelemetry{
		{Frontend: "web", Funnels: []ShapeFunnel{
			{ID: "buy", Steps: []string{"view_product", "purchase"}, Window: "7d", By: FunnelByPerson},
			{ID: "onboarding", Steps: []string{"a", "b"}, Window: "1h", By: FunnelBySession},
		}},
		{Frontend: "landing", Funnels: []ShapeFunnel{
			{ID: "land", Steps: []string{"page_viewed", "signup_started"}, Window: "1d", By: FunnelByVisitor},
		}},
		{Frontend: "admin", ErrorsIgnore: []string{"AbortError"}, Funnels: []ShapeFunnel{
			{ID: "cohort", Steps: []string{"a", "b"}, Window: "7d", By: FunnelByPerson},
		}},
	}
}

// TestShapeFunnelAcceptsEveryIdentity: a funnel counts by session, user,
// visitor or person, and by nothing else.
func TestShapeFunnelAcceptsEveryIdentity(t *testing.T) {
	for _, by := range []string{FunnelBySession, FunnelByUser, FunnelByVisitor, FunnelByPerson} {
		tel := ShapeFrontendTelemetry{Frontend: "web", Funnels: []ShapeFunnel{{ID: "f", Steps: []string{"a", "b"}, Window: "1h", By: by}}}
		if err := tel.validate(); err != nil {
			t.Errorf("by %q: %v", by, err)
		}
	}
	tel := ShapeFrontendTelemetry{Frontend: "web", Funnels: []ShapeFunnel{{ID: "f", Steps: []string{"a", "b"}, Window: "1h", By: "device"}}}
	if err := tel.validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("by device: got %v, want ErrInvalid", err)
	}
}

// TestAReaderWithoutFunnelPersonGetsOnlyTheFunnelsItDecodes: a control plane
// that decodes frontend telemetry but predates the visitor and person funnels
// refuses those values, so it is sent the session and user funnels alone; an
// entry left declaring nothing is dropped, and the shape it was given is not
// modified. A reader of both gets the funnels whole.
func TestAReaderWithoutFunnelPersonGetsOnlyTheFunnelsItDecodes(t *testing.T) {
	s := Shape{Kind: EnvSelfManaged, FrontendTelemetry: productFunnelTelemetry()}
	old := (&BundleReader{Features: []string{BundleFeatureFrontendTelemetry}}).LimitShape(s)
	want := []ShapeFrontendTelemetry{
		{Frontend: "web", Funnels: []ShapeFunnel{{ID: "onboarding", Steps: []string{"a", "b"}, Window: "1h", By: FunnelBySession}}},
		{Frontend: "admin", ErrorsIgnore: []string{"AbortError"}},
	}
	if !reflect.DeepEqual(old.FrontendTelemetry, want) {
		t.Errorf("old reader got\n%+v\nwant\n%+v", old.FrontendTelemetry, want)
	}
	if !reflect.DeepEqual(s.FrontendTelemetry, productFunnelTelemetry()) {
		t.Fatal("LimitShape modified the shape it was given")
	}
	for _, f := range old.FrontendTelemetry {
		if err := f.validate(); err != nil {
			t.Errorf("the limited entry %q does not validate: %v", f.Frontend, err)
		}
	}
	both := (&BundleReader{Features: []string{BundleFeatureFrontendTelemetry, BundleFeatureFunnelPerson}}).LimitShape(s)
	if !reflect.DeepEqual(both.FrontendTelemetry, s.FrontendTelemetry) {
		t.Errorf("a reader of both features lost funnels: %+v", both.FrontendTelemetry)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape with person funnels reports no optional parts")
	}
	plain := Shape{Kind: EnvSelfManaged, FrontendTelemetry: want[:1]}
	if got := (&BundleReader{Features: []string{BundleFeatureFrontendTelemetry}}).LimitShape(plain); !reflect.DeepEqual(got.FrontendTelemetry, plain.FrontendTelemetry) {
		t.Errorf("a shape with only session funnels was changed: %+v", got.FrontendTelemetry)
	}
}
