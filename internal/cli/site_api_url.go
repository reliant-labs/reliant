package cli

import (
	"sort"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

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
		ref, ok := f.RuntimeConfigSpec[deploytarget.SiteAPIRuntimeKey]
		if !ok || ref.WorkloadURL == nil || !hostedWorkloads[ref.WorkloadURL.Name] {
			continue
		}
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
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
		api := deploytarget.SiteAPIURL(w.URL)
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

// claimSiteAPIURL is the API address a claimable deploy's text and --json
// name beside its live URL: the site's /_api, when the env serves its API
// same-origin from one hosted site. The claim's URL is the static site's
// (primaryURL prefers it), and an unclaimed deploy carries at most one site,
// so the one-site case is the only one it answers.
func claimSiteAPIURL(e *KCLEntities, siteURL string) string {
	if len(siteAPIFrontends(e)) != 1 {
		return ""
	}
	return deploytarget.SiteAPIURL(siteURL)
}
