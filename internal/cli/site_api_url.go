package cli

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/deploy"
)

// siteAPIRuntimeKey is the runtime_config key whose workload reference the
// platform serves at a hosted site's /_api (control-plane
// internal/operators/staticsite sameOriginAPI): the key forge's generated
// frontend client reads its API base from.
const siteAPIRuntimeKey = "API_URL"

// siteAPIFrontends names the env's hosted frontends whose API the platform
// serves on the site's own origin: a frontend bound to forge.OnHosted whose
// API_URL references a workload of the env that is itself hosted. Every host
// of such a site answers <origin>/_api/* with that workload, so the API has an
// address on the site's host that the author never wrote down. A literal
// API_URL is the author's own address and is never rewritten, so it is not
// listed. Sorted; nil for none.
func siteAPIFrontends(e *KCLEntities) []string {
	if e == nil {
		return nil
	}
	hostedWorkloads := map[string]bool{}
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHosted) {
			hostedWorkloads[w.Name] = true
		}
	}
	var out []string
	for _, f := range e.Frontends {
		if !frontendIsHosted(f) {
			continue
		}
		ref, ok := f.RuntimeConfigSpec[siteAPIRuntimeKey]
		if !ok || ref.WorkloadURL == nil || !hostedWorkloads[ref.WorkloadURL.Name] {
			continue
		}
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// siteAPIURL is the same-origin API address of a hosted site at siteURL: its
// origin plus /_api (deploy.SiteAPIPath), which the platform routes at the
// host root whatever the site's base path. "" when siteURL has no host.
func siteAPIURL(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + deploy.SiteAPIPath
}

// hostedSiteAPIURLs maps each hosted site of the env that serves its API
// same-origin (siteAPIFrontends) to that API's address, from the live
// status's site URLs. nil when there is none, or no such site has a URL yet.
func hostedSiteAPIURLs(e *KCLEntities, ws []deploytarget.HostedWorkloadStatus) map[string]string {
	sites := siteAPIFrontends(e)
	if len(sites) == 0 {
		return nil
	}
	serves := make(map[string]bool, len(sites))
	for _, s := range sites {
		serves[s] = true
	}
	var out map[string]string
	for _, w := range ws {
		if !serves[w.Name] || deploytarget.HostedTier(w.Tier) != deploytarget.HostedTierStatic {
			continue
		}
		api := siteAPIURL(w.URL)
		if api == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[w.Name] = api
	}
	return out
}

// formatSiteAPIURLs renders the API addresses hostedSiteAPIURLs found, below
// the hosted URL lines: "API: <url>" for one site, "API (<site>): <url>" per
// site when there are several. "" for none.
func formatSiteAPIURLs(indent string, apis map[string]string) string {
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

// claimSiteAPIURL is the API address a claimable deploy's text and --json
// name beside its live URL: the site's /_api, when the env serves its API
// same-origin from one hosted site. The claim's URL is the static site's
// (primaryURL prefers it), and an unclaimed deploy carries at most one site,
// so the one-site case is the only one it answers.
func claimSiteAPIURL(e *KCLEntities, siteURL string) string {
	if len(siteAPIFrontends(e)) != 1 {
		return ""
	}
	return siteAPIURL(siteURL)
}
