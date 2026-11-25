package templates

import (
	"strings"
	"testing"
)

// TestRenderDoesNotKnowAboutFrontends pins the BOUNDARY, not the behaviour.
//
// Render serves eleven unrelated template categories. An earlier version of
// this defaulting type-asserted FrontendTemplateData directly inside Render and
// called into the npm-peer package from there, which made the shared renderer
// know one payload's shape and one domain's vocabulary — and made it the
// obvious place for the next domain to special-case too.
//
// Render's whole knowledge is now the one-method selfDefaulting interface,
// declared at the consumer. This test fails if someone reintroduces a payload
// type switch: an arbitrary payload that does NOT implement selfDefaulting must
// pass through untouched, and a payload that does must be completed without
// Render naming its type.
func TestRenderDoesNotKnowAboutFrontends(t *testing.T) {
	// A payload from a different domain, structurally unrelated to frontends.
	type unrelatedPayload struct{ Name string }

	// It names a template whose only field it has, so the render must
	// succeed and carry the payload through as given. (It used to render
	// service.go.tmpl, which this payload cannot satisfy, and tolerate the
	// error; internal/tmplfields checks every call site's data now.)
	out, err := ProjectTemplates().Render("middleware.go.tmpl", unrelatedPayload{Name: "widget"})
	if err != nil {
		t.Fatalf("render with a payload Render knows nothing about: %v", err)
	}
	if !strings.Contains(string(out), `"widget_session"`) {
		t.Errorf("the payload did not pass through Render untouched; rendered:\n%s", out)
	}

	// The frontend payload completes itself through the interface, and the
	// completion is visible in the rendered output.
	rendered, err := FrontendTemplates().Render("vite-spa/tsconfig.json.tmpl",
		FrontendTemplateData{FrontendName: "web", ProjectName: "proj"})
	if err != nil {
		t.Fatalf("render frontend tsconfig: %v", err)
	}
	if !strings.Contains(string(rendered), `"@connectrpc/connect": ["./node_modules/@connectrpc/connect"]`) {
		t.Errorf("frontend payload did not self-default its peer pins; rendered:\n%s", rendered)
	}
}

// TestFrontendTemplateDataExplicitPinsWin proves the default is a default and
// not a wall — the 20%-are-never-disempowered rule applied to one field.
func TestFrontendTemplateDataExplicitPinsWin(t *testing.T) {
	d := FrontendTemplateData{
		FrontendName:       "web",
		ProjectName:        "proj",
		WebRuntimeTypePins: []string{"@example/only"},
	}
	rendered, err := FrontendTemplates().Render("vite-spa/tsconfig.json.tmpl", d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(rendered)
	if !strings.Contains(got, `"@example/only"`) {
		t.Errorf("explicit pins were not honoured:\n%s", got)
	}
	if strings.Contains(got, "@connectrpc/connect") {
		t.Error("defaults were merged into an explicit list; the caller's set must win outright")
	}
}
