package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func rcLiteral(v string) deployv1alpha1.RuntimeConfigValue {
	return deployv1alpha1.RuntimeConfigValue{Value: &v}
}

// fixtureEntities is an env as kcl/render.k renders Frontend.fixtures: the two
// keys in each frontend's declared runtime config.
func fixtureEntities() *KCLEntities {
	return &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Runtime: FrontendRuntime{Type: "hosted"}, RuntimeConfigSpec: map[string]deployv1alpha1.RuntimeConfigValue{
			"FIXTURES_SCENARIO":  rcLiteral("shop"),
			"FIXTURES_READ_ONLY": rcLiteral("true"),
			"API_URL":            {WorkloadURL: &deployv1alpha1.WorkloadURLRef{Name: "api"}},
		}},
		{Name: "sandbox", Runtime: FrontendRuntime{Type: "host"}, RuntimeConfigSpec: map[string]deployv1alpha1.RuntimeConfigValue{
			"FIXTURES_SCENARIO":  rcLiteral("empty-account"),
			"FIXTURES_READ_ONLY": rcLiteral("false"),
		}},
		{Name: "live", Runtime: FrontendRuntime{Type: "bucket"}},
	}}
}

func TestFrontendFixtures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rc           map[string]deployv1alpha1.RuntimeConfigValue
		wantScenario string
		wantReadOnly bool
		wantOK       bool
	}{
		{name: "none"},
		{name: "empty scenario", rc: map[string]deployv1alpha1.RuntimeConfigValue{"FIXTURES_SCENARIO": rcLiteral("")}},
		{name: "read-only", rc: map[string]deployv1alpha1.RuntimeConfigValue{"FIXTURES_SCENARIO": rcLiteral("shop"), "FIXTURES_READ_ONLY": rcLiteral("true")}, wantScenario: "shop", wantReadOnly: true, wantOK: true},
		// The browser reads anything but exactly "false" as read-only, and so does the summary.
		{name: "read-only key missing", rc: map[string]deployv1alpha1.RuntimeConfigValue{"FIXTURES_SCENARIO": rcLiteral("shop")}, wantScenario: "shop", wantReadOnly: true, wantOK: true},
		{name: "writes", rc: map[string]deployv1alpha1.RuntimeConfigValue{"FIXTURES_SCENARIO": rcLiteral("shop"), "FIXTURES_READ_ONLY": rcLiteral("false")}, wantScenario: "shop", wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ro, ok := frontendFixtures(FrontendEntity{RuntimeConfigSpec: tc.rc})
			if s != tc.wantScenario || ro != tc.wantReadOnly || ok != tc.wantOK {
				t.Errorf("frontendFixtures = (%q, %v, %v), want (%q, %v, %v)", s, ro, ok, tc.wantScenario, tc.wantReadOnly, tc.wantOK)
			}
		})
	}
}

func TestWriteFixturesSummary(t *testing.T) {
	var buf bytes.Buffer
	writeFixturesSummary(&buf, fixtureEntities())
	got := buf.String()
	for _, want := range []string{
		"fixtures (served from runtime config instead of the backend; the release is unchanged):",
		`web: scenario "shop", read-only: only NO_SIDE_EFFECTS rpcs answer`,
		`sandbox: scenario "empty-account", writes allowed, in the browser's fixture store`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "live") {
		t.Errorf("a frontend with no fixtures is named:\n%s", got)
	}

	buf.Reset()
	writeFixturesSummary(&buf, &KCLEntities{Frontends: []FrontendEntity{{Name: "live"}}})
	writeFixturesSummary(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("an env with no fixtures printed %q", buf.String())
	}
}

func TestWriteRenderedJSONFrontends(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRenderedJSON(&buf, "shop", "demo", "", nil, nil, fixtureEntities()); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Frontends []envRenderDocFrontend `json:"frontends"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Frontends) != 3 {
		t.Fatalf("frontends = %+v, want 3", doc.Frontends)
	}
	web := doc.Frontends[0]
	if web.Name != "web" || web.Runtime != "hosted" || *web.RuntimeConfig["FIXTURES_SCENARIO"].Value != "shop" ||
		web.RuntimeConfig["API_URL"].WorkloadURL == nil || web.RuntimeConfig["API_URL"].WorkloadURL.Name != "api" {
		t.Errorf("web = %+v, want its declared runtime config with the reference kept", web)
	}
	// A frontend with no runtime config is {}, not null: a consumer indexes it.
	if !strings.Contains(buf.String(), `"runtime_config": {}`) {
		t.Errorf("a frontend with no runtime config is not {}:\n%s", buf.String())
	}

	buf.Reset()
	if err := writeRenderedJSON(&buf, "shop", "demo", "", nil, nil, &KCLEntities{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"frontends": []`) {
		t.Errorf("no frontends is not []:\n%s", buf.String())
	}
}
