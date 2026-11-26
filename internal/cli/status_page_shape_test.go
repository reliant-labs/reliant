package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// KCL -> render contract -> shape field -> Shape.Validate, for a declared
// status page over derived and declared uptime checks. renderUptime writes
// the expression after `uptime = `, so the page follows a Uptime.
func TestStatusPageReachesTheShape(t *testing.T) {
	e := renderUptime(t, `forge.Uptime {
        checks = [forge.UptimeCheck {name = "shop", url = "https://shop.example.com/healthz"}]
    }
    status_page = forge.StatusPage {
        title = "Acme status"
        links = [forge.StatusLink {label = "Support", url = "https://acme.example.com/help"}]
        components = [
            forge.StatusComponent {name = "Website", checks = ["site"]}
            forge.StatusComponent {name = "Shop", checks = ["shop", "api"], description = "Checkout and API"}
        ]
    }`)
	got := shapeStatusPageOf(e)
	want := &release.ShapeStatusPage{
		Title: "Acme status",
		Links: []release.ShapeStatusLink{{Label: "Support", URL: "https://acme.example.com/help"}},
		Components: []release.ShapeStatusComponent{
			{Name: "Website", Checks: []string{"site"}},
			{Name: "Shop", Description: "Checkout and API", Checks: []string{"api", "shop"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status page = %+v, want %+v", got, want)
	}
	shape := release.Shape{Kind: release.EnvPersistent, UptimeChecks: shapeUptimeChecksOf(e), StatusPage: got}.Canonical()
	if err := shape.Validate(); err != nil {
		t.Errorf("a page KCL rendered must satisfy the shape's validation: %v", err)
	}
	if !reflect.DeepEqual(shape.StatusPage, want) {
		t.Errorf("the render is not already canonical: %+v", shape.StatusPage)
	}

	if shapeStatusPageOf(renderUptime(t, "")) != nil {
		t.Error("an env with no status_page recorded one")
	}
}
