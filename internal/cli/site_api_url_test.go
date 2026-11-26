package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// siteAPIEnv is a hosted full-stack env: the site "web" with API_URL a
// reference to the hosted workload "api", which the platform serves at the
// site's /_api.
func siteAPIEnv() *KCLEntities {
	return &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp.example"},
		Workloads:    []WorkloadEntity{hostedWL("api")},
		Frontends: []FrontendEntity{{
			Name: "web", Type: "nextjs", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted},
			RuntimeConfigSpec: map[string]deployv1alpha1.RuntimeConfigValue{
				"API_URL": {WorkloadURL: &deployv1alpha1.WorkloadURLRef{Name: "api"}},
			},
		}},
	}
}

// Only a hosted site whose API_URL references a hosted workload gets the
// platform's same-origin /_api route; a literal API_URL is the author's own
// address, never rewritten, and a reference to something that is not a hosted
// workload is not routed either.
func TestSiteAPIFrontends(t *testing.T) {
	if got := siteAPIFrontends(siteAPIEnv()); len(got) != 1 || got[0] != "web" {
		t.Fatalf("siteAPIFrontends = %v, want [web]", got)
	}
	for name, mutate := range map[string]func(*KCLEntities){
		"literal API_URL": func(e *KCLEntities) {
			e.Frontends[0].RuntimeConfigSpec["API_URL"] = deployv1alpha1.RuntimeConfigValue{Value: strPtr("https://api.example")}
		},
		"no API_URL": func(e *KCLEntities) { e.Frontends[0].RuntimeConfigSpec = nil },
		"reference to no workload": func(e *KCLEntities) {
			e.Frontends[0].RuntimeConfigSpec["API_URL"] = deployv1alpha1.RuntimeConfigValue{WorkloadURL: &deployv1alpha1.WorkloadURLRef{Name: "other"}}
		},
		"workload not hosted": func(e *KCLEntities) { e.Workloads[0].Runtime = RuntimeEntity{Type: RuntimeCluster} },
		"site not hosted":     func(e *KCLEntities) { e.Frontends[0].Runtime = FrontendRuntime{} },
	} {
		e := siteAPIEnv()
		mutate(e)
		if got := siteAPIFrontends(e); len(got) != 0 {
			t.Errorf("%s: siteAPIFrontends = %v, want none", name, got)
		}
	}
	if got := siteAPIFrontends(nil); got != nil {
		t.Errorf("siteAPIFrontends(nil) = %v", got)
	}
}

// `forge env status` names the API's same-origin address under the site's
// URL, so an author never has to guess the /_api prefix (new.txt F26), and
// --json carries it as hosted_api_urls.
func TestWriteHostedStatusNamesTheSiteAPI(t *testing.T) {
	e := siteAPIEnv()
	st := deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{
		{Name: "api", Tier: string(deploytarget.HostedTierWorkload), URL: "https://api-x.example"},
		{Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: "https://ancient-quokka.sites.example"},
	}}
	read := func(context.Context, string, *KCLEntities) (deploytarget.HostedEnvStatus, error) { return st, nil }
	var out bytes.Buffer
	writeHostedStatus(context.Background(), &out, "prod", e, read)
	want := "\nhosted (prod):\n  url api: https://api-x.example\n  url web: https://ancient-quokka.sites.example\n" +
		"  API: https://ancient-quokka.sites.example/_api\n"
	if got := out.String(); got != want {
		t.Errorf("status block = %q, want %q", got, want)
	}

	apis := hostedSiteAPIURLs(e, st.Workloads)
	if len(apis) != 1 || apis["web"] != "https://ancient-quokka.sites.example/_api" {
		t.Errorf("hostedSiteAPIURLs = %v", apis)
	}
	b, err := json.Marshal(upServicesReport{HostedAPIURLs: apis})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hosted_api_urls":{"web":"https://ancient-quokka.sites.example/_api"}`) {
		t.Errorf("--json lacks hosted_api_urls: %s", b)
	}
	if b, _ := json.Marshal(upServicesReport{}); strings.Contains(string(b), "hosted_api_urls") {
		t.Errorf("an env with no site API must not carry hosted_api_urls: %s", b)
	}

}

// A claimable full-stack deploy prints the API address under the live URL,
// and its --json claim and the MCP result carry it as api_url. A static-only
// deploy has neither.
func TestClaimBlockNamesTheSiteAPI(t *testing.T) {
	prev := claimableNow
	claimableNow = func() time.Time { return time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC) }
	defer func() { claimableNow = prev }()
	site := "https://copper-cricket.reliantpreview.dev"
	doc := &deployClaimJSON{
		State: "live", Published: true, URL: site, ClaimURL: "https://app.reliant.dev/claim#rcl_9f3KQe",
		APIURL:    claimSiteAPIURL(siteAPIEnv(), site),
		ExpiresAt: time.Date(2026, 10, 29, 14, 2, 0, 0, time.UTC), PurgeAfter: time.Date(2026, 11, 5, 14, 2, 0, 0, time.UTC),
	}
	var b strings.Builder
	printClaimBlock(&b, "prod", doc)
	if want := "✓ Live:   " + site + "\n  API:    " + site + "/_api\n"; !strings.Contains(b.String(), want) {
		t.Errorf("claim block lacks %q:\n%s", want, b.String())
	}
	j, _ := json.Marshal(doc)
	if !strings.Contains(string(j), `"api_url":"`+site+`/_api"`) {
		t.Errorf("claim --json lacks api_url: %s", j)
	}
	res := &claimableDeployResult{Env: "prod"}
	fillClaim(res, doc)
	res.RolloutVerified = true
	if res.APIURL != site+"/_api" {
		t.Errorf("MCP api_url = %q", res.APIURL)
	}
	if steps := claimNextSteps(res); !strings.Contains(steps, site+"/_api") {
		t.Errorf("next_steps does not name the API: %s", steps)
	}

	static := siteAPIEnv()
	static.Workloads = nil
	doc.APIURL = claimSiteAPIURL(static, site)
	b.Reset()
	printClaimBlock(&b, "prod", doc)
	if strings.Contains(b.String(), "API:") {
		t.Errorf("a static-only deploy names an API:\n%s", b.String())
	}
	if j, _ := json.Marshal(doc); strings.Contains(string(j), "api_url") {
		t.Errorf("a static-only claim carries api_url: %s", j)
	}
}
