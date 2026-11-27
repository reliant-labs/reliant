package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// statusPage is a declared page over the full document's one uptime check.
func statusPage() *ShapeStatusPage {
	return &ShapeStatusPage{
		Title:       "Acme status",
		LogoURL:     "https://acme.test/logo.svg",
		AccentColor: "#5b21b6",
		Links:       []ShapeStatusLink{{Label: "Support", URL: "https://acme.test/help"}},
		Components:  []ShapeStatusComponent{{Name: "Website", Description: "The shop", Checks: []string{"web"}}},
	}
}

func statusShape(p *ShapeStatusPage) Shape {
	return Shape{
		Kind: EnvSelfManaged,
		UptimeChecks: []ShapeUptimeCheck{
			{Name: "web", Target: "web", Method: "GET", IntervalSeconds: 60, TimeoutSeconds: 10, Source: UptimeSourceDefault},
			{Name: "api-ready", Target: "api", Path: "/readyz", Method: "GET", IntervalSeconds: 60, TimeoutSeconds: 10, Source: UptimeSourceUser},
		},
		StatusPage: p,
	}
}

// TestShapeStatusPageValidate: a declared page follows the forge.status/v1
// presentation rules, and every check a component names is one of the
// shape's own uptime checks.
func TestShapeStatusPageValidate(t *testing.T) {
	if err := statusShape(statusPage()).Validate(); err != nil {
		t.Fatalf("a valid page: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*ShapeStatusPage)
		want string
	}{
		"no title":             {func(p *ShapeStatusPage) { p.Title = "" }, "title is empty"},
		"http logo":            {func(p *ShapeStatusPage) { p.LogoURL = "http://acme.test/logo.svg" }, "must be https"},
		"accent":               {func(p *ShapeStatusPage) { p.AccentColor = "purple" }, "#rrggbb"},
		"link scheme":          {func(p *ShapeStatusPage) { p.Links[0].URL = "javascript:alert(1)" }, "http or https"},
		"no components":        {func(p *ShapeStatusPage) { p.Components = nil }, "no components"},
		"duplicate component":  {func(p *ShapeStatusPage) { p.Components = append(p.Components, p.Components[0]) }, "twice"},
		"no checks":            {func(p *ShapeStatusPage) { p.Components[0].Checks = nil }, "names no uptime check"},
		"duplicate check":      {func(p *ShapeStatusPage) { p.Components[0].Checks = []string{"web", "web"} }, `check "web" twice`},
		"unknown check":        {func(p *ShapeStatusPage) { p.Components[0].Checks = []string{"route-admin"} }, `"route-admin", which the shape does not have`},
		"long component name":  {func(p *ShapeStatusPage) { p.Components[0].Name = strings.Repeat("a", 65) }, "longer than"},
		"long description":     {func(p *ShapeStatusPage) { p.Components[0].Description = strings.Repeat("a", 201) }, "longer than"},
		"too many components":  {func(p *ShapeStatusPage) { p.Components = make([]ShapeStatusComponent, 51) }, "more than 50"},
		"control in component": {func(p *ShapeStatusPage) { p.Components[0].Name = "a\tb" }, "control character"},
	} {
		p := statusPage()
		tc.edit(p)
		err := statusShape(p).Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate = %v, want ErrInvalid naming %q", name, err, tc.want)
		}
	}
	noChecks := statusShape(statusPage())
	noChecks.UptimeChecks = nil
	if err := noChecks.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a page over a shape with no uptime checks: Validate = %v, want ErrInvalid", err)
	}
}

// TestShapeStatusPageCanonical: a component's checks are a set, so their
// order does not change the shape's hash; components and links keep theirs,
// which is the page's.
func TestShapeStatusPageCanonical(t *testing.T) {
	p := statusPage()
	p.Components = []ShapeStatusComponent{
		{Name: "Website", Checks: []string{"web"}},
		{Name: "API", Checks: []string{"web", "api-ready"}},
	}
	got := statusShape(p).Canonical().StatusPage
	if names := []string{got.Components[0].Name, got.Components[1].Name}; !reflect.DeepEqual(names, []string{"Website", "API"}) {
		t.Errorf("canonical reordered the components: %v", names)
	}
	if !reflect.DeepEqual(got.Components[1].Checks, []string{"api-ready", "web"}) {
		t.Errorf("canonical left the checks unsorted: %v", got.Components[1].Checks)
	}
	if p.Components[1].Checks[0] != "web" {
		t.Error("Canonical modified the shape it was given")
	}
	bare := statusPage()
	bare.Links = []ShapeStatusLink{}
	raw, err := json.Marshal(statusShape(bare).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"links"`) {
		t.Errorf("an empty link list is still encoded: %s", raw)
	}
	raw, err = json.Marshal(Shape{Kind: EnvSelfManaged}.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "status_page") {
		t.Errorf("a shape with no page carries the key: %s", raw)
	}
}

// TestStatusPageIsAFeature: a reader that predates Shape.StatusPage is sent
// the shape without it, and so is a reader that does not decode uptime
// checks, which the page names.
func TestStatusPageIsAFeature(t *testing.T) {
	s := statusShape(statusPage())
	for name, features := range map[string][]string{
		"predates the page":        {BundleFeatureUptimeChecks, BundleFeatureChanges},
		"decodes no uptime checks": {BundleFeatureStatusPage},
	} {
		if got := (&BundleReader{Features: features}).LimitShape(s); got.StatusPage != nil {
			t.Errorf("%s: the reader was sent the page: %+v", name, got.StatusPage)
		}
	}
	if s.StatusPage == nil {
		t.Fatal("LimitShape modified the shape it was given")
	}
	got := (&BundleReader{Features: []string{BundleFeatureUptimeChecks, BundleFeatureStatusPage}}).LimitShape(s)
	if !reflect.DeepEqual(got.StatusPage, s.StatusPage) {
		t.Errorf("a reader of the feature lost the page: %+v", got.StatusPage)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape declaring a page reports no optional parts")
	}
}

// TestNoStatusPageMeansNoKey: a status page is opt-in only (owner,
// 2026-11-25). A shape that declares none, however much else it carries,
// encodes with no status_page key, byte for byte as a reader that predates
// the field is sent it, and needs nothing of the reader.
func TestNoStatusPageMeansNoKey(t *testing.T) {
	s := fullBundleDoc().Shape
	s.StatusPage = nil
	raw, err := json.Marshal(s.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "status_page") {
		t.Fatalf("a shape with no page encodes a status_page key: %s", raw)
	}
	every := &BundleReader{Features: BundleDocFeatures()}
	predates := &BundleReader{Features: slices.DeleteFunc(BundleDocFeatures(), func(f string) bool { return f == BundleFeatureStatusPage })}
	a, _ := json.Marshal(every.LimitShape(s).Canonical())
	b, _ := json.Marshal(predates.LimitShape(s).Canonical())
	if string(a) != string(b) {
		t.Errorf("a shape with no page differs between a reader of the feature and one that predates it:\n%s\n%s", a, b)
	}
	if (Shape{Kind: EnvSelfManaged}).HasOptionalParts() {
		t.Error("an empty shape reports optional parts")
	}
}

// TestShapeStatusPageRoundTripsTheBundleDocument: the documented JSON of a
// declared page, through the strict bundle decoder and back.
func TestShapeStatusPageRoundTripsTheBundleDocument(t *testing.T) {
	doc := fullBundleDoc()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	const want = `"status_page":{"title":"Acme status","logo_url":"https://acme.test/logo.svg","accent_color":"#5b21b6","links":[{"label":"Support","url":"https://acme.test/help"}],"components":[{"name":"Website","description":"The shop","checks":["web"]}]}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("the document does not carry the page as documented; want %s in\n%s", want, raw)
	}
	back, err := DecodeBundleDoc(raw)
	if err != nil {
		t.Fatalf("the document no longer decodes: %v", err)
	}
	if !reflect.DeepEqual(back.Shape.StatusPage, doc.Shape.StatusPage) {
		t.Errorf("round trip changed the page:\n got %+v\nwant %+v", back.Shape.StatusPage, doc.Shape.StatusPage)
	}
}
