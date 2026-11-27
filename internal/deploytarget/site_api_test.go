package deploytarget

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The route is at the host root, whatever path the site URL carries.
func TestSiteAPIURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://copper-cricket.reliantpreview.dev":   "https://copper-cricket.reliantpreview.dev/_api",
		"https://copper-cricket.reliantpreview.dev/":  "https://copper-cricket.reliantpreview.dev/_api",
		"https://ancient-quokka.sites.example/admin/": "https://ancient-quokka.sites.example/_api",
		"":          "",
		"not a url": "",
	} {
		if got := SiteAPIURL(in); got != want {
			t.Errorf("SiteAPIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSiteAPIURLs(t *testing.T) {
	if got := FormatSiteAPIURLs("  ", nil); got != "" {
		t.Errorf("no site: %q", got)
	}
	if got := FormatSiteAPIURLs("  ", map[string]string{"web": "https://a/_api"}); got != "  API: https://a/_api\n" {
		t.Errorf("one site: %q", got)
	}
	// Several sites: each is named.
	if got := FormatSiteAPIURLs("  ", map[string]string{"web": "https://a/_api", "admin": "https://b/_api"}); got !=
		"  API (admin): https://b/_api\n  API (web): https://a/_api\n" {
		t.Errorf("several sites: %q", got)
	}
}

// siteAPIPlan is a full-stack hosted deploy: the workload "api" and the site
// "web" whose API_URL references it, both published.
func siteAPIPlan(apiURL v1alpha1.RuntimeConfigValue) ([]hostedPlanItem, map[string]hostedPublished) {
	plan := []hostedPlanItem{
		{Name: "api", Tier: HostedTierWorkload, Spec: v1alpha1.WorkloadSpec{}},
		{Name: "db", Tier: HostedTierDatabase},
		{Name: "web", Tier: HostedTierStatic, Spec: v1alpha1.StaticSiteSpec{
			RuntimeConfig: map[string]v1alpha1.RuntimeConfigValue{SiteAPIRuntimeKey: apiURL},
		}},
	}
	published := map[string]hostedPublished{
		"api": {URL: "https://api-acme.apps.example"},
		"web": {URL: "https://ancient-quokka.sites.example/"},
	}
	return plan, published
}

// A signed-in `forge env deploy` names the API's same-origin address under the
// site's URL, as `forge env status` and a claimable deploy do, so an author
// never has to guess the /_api prefix.
func TestHostedDeployURLsNameTheSiteAPI(t *testing.T) {
	plan, published := siteAPIPlan(v1alpha1.RuntimeConfigValue{WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "api"}})
	want := "  url api: https://api-acme.apps.example\n" +
		"  url web: https://ancient-quokka.sites.example/\n" +
		"  API: https://ancient-quokka.sites.example/_api\n"
	if got := formatHostedDeployURLs(plan, published); got != want {
		t.Errorf("deploy URLs =\n%s\nwant\n%s", got, want)
	}

	// A pointer spec reads the same.
	spec := plan[2].Spec.(v1alpha1.StaticSiteSpec)
	plan[2].Spec = &spec
	if got := formatHostedDeployURLs(plan, published); got != want {
		t.Errorf("deploy URLs (pointer spec) =\n%s\nwant\n%s", got, want)
	}
}

// Only a site whose API_URL references a workload this deploy publishes is
// served its API on its own host: a literal API_URL is the author's address,
// never rewritten, and a site with no URL yet has no address to name.
func TestPlanSiteAPIURLsOnlyForAPIsTheSiteServes(t *testing.T) {
	str := func(s string) *string { return &s }
	for name, apiURL := range map[string]v1alpha1.RuntimeConfigValue{
		"literal API_URL":          {Value: str("https://api.example")},
		"reference to no workload": {WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "other"}},
		"reference to a database":  {WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "db"}},
		"no API_URL":               {},
	} {
		plan, published := siteAPIPlan(apiURL)
		if got := planSiteAPIURLs(plan, published); got != nil {
			t.Errorf("%s: planSiteAPIURLs = %v, want none", name, got)
		}
	}

	plan, published := siteAPIPlan(v1alpha1.RuntimeConfigValue{WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "api"}})
	delete(published, "web")
	if got := planSiteAPIURLs(plan, published); got != nil {
		t.Errorf("site with no URL: planSiteAPIURLs = %v, want none", got)
	}
}
