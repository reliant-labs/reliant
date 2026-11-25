package deploytarget

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// SiteAPIRuntimeKey is the runtime_config key whose workload reference the
// platform serves at a hosted site's /_api (control-plane
// internal/operators/staticsite sameOriginAPI): the key forge's generated
// frontend client reads its API base from.
const SiteAPIRuntimeKey = "API_URL"

// SiteAPIURL is the same-origin API address of a hosted site at siteURL: its
// origin plus /_api (deploy.SiteAPIPath), which the platform routes at the
// host root whatever the site's base path. "" when siteURL has no host.
func SiteAPIURL(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + deploy.SiteAPIPath
}

// FormatSiteAPIURLs renders site → API addresses below the hosted URL lines:
// "API: <url>" for one site, "API (<site>): <url>" per site when there are
// several. "" for none. `forge env deploy` and `forge env status` share it,
// as they share FormatHostedURLs.
func FormatSiteAPIURLs(indent string, apis map[string]string) string {
	if len(apis) == 0 {
		return ""
	}
	if len(apis) == 1 {
		for _, api := range apis {
			return indent + "API: " + api + "\n"
		}
	}
	sites := make([]string, 0, len(apis))
	for s := range apis {
		sites = append(sites, s)
	}
	sort.Strings(sites)
	var b strings.Builder
	for _, s := range sites {
		fmt.Fprintf(&b, "%sAPI (%s): %s\n", indent, s, apis[s])
	}
	return b.String()
}

// planSiteAPIURLs maps each static site of a deploy plan whose API_URL
// references a workload published in the same plan — the site the platform
// answers <origin>/_api/* for with that workload — to that API's address,
// from the site's published URL. A literal API_URL is the author's own
// address and is never rewritten, so it is not listed. nil for none.
func planSiteAPIURLs(plan []hostedPlanItem, published map[string]hostedPublished) map[string]string {
	workloads := map[string]bool{}
	for _, item := range plan {
		if item.Tier == HostedTierWorkload {
			workloads[item.Name] = true
		}
	}
	var out map[string]string
	for _, item := range plan {
		if item.Tier != HostedTierStatic {
			continue
		}
		var rc map[string]v1alpha1.RuntimeConfigValue
		switch spec := item.Spec.(type) {
		case v1alpha1.StaticSiteSpec:
			rc = spec.RuntimeConfig
		case *v1alpha1.StaticSiteSpec:
			if spec != nil {
				rc = spec.RuntimeConfig
			}
		}
		ref := rc[SiteAPIRuntimeKey].WorkloadURL
		if ref == nil || !workloads[ref.Name] {
			continue
		}
		api := SiteAPIURL(published[item.Name].URL)
		if api == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[item.Name] = api
	}
	return out
}
