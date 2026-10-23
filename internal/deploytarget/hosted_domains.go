package deploytarget

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Custom domains: forge's half.
//
// On hosted, a custom hostname is NOT spec. It is an org-scoped resource in
// the control plane, created once and BOUND to one environment's workload or
// frontend:
//
//	forge domain add hounders.club
//	forge domain bind hounders.club --env prod --target web
//
// Three things make it a resource rather than a field. A name you bring
// needs an action at YOUR registrar, ownership verification and a
// certificate — asynchronous and human-gated, none of which a deploy
// converges. It binds to exactly ONE environment, while a single workloads.k
// renders to many. And the platform may allocate a hostname itself, which a
// spec field would then contradict as a second source of truth.
//
// forge.OnCluster is the opposite case and keeps `Port.domains`: there you
// own the ingress, forge renders the Gateway and HTTPRoute itself, and
// nothing is waiting on a human.
//
// So forge has two jobs here. It REFUSES a hosted spec that carries a domain
// (below, and at render — kcl/render.k `_hosted_domain_violations`, which is
// the refusal an author actually sees). And it REPORTS what the control
// plane observes about the domains bound to the env, which is the rest of
// this file: per-domain state, the DNS still owed, and the last error.

// hostedDomainClaim is one workload's declared custom hostnames.
type hostedDomainClaim struct {
	// Workload is the deployment name that declared them.
	Workload string
	// Tier distinguishes a Workload port's domains from a StaticSite's,
	// so the refusal can name where to go and edit.
	Tier    HostedTier
	Domains []string
}

// hostedDomainClaims is every custom domain a plan declares, sorted by
// workload. Pure, and read by both the deploy's refusal and the dry-run
// report, so the two can never disagree about what was declared.
func hostedDomainClaims(plan []hostedPlanItem) []hostedDomainClaim {
	var out []hostedDomainClaim
	for _, item := range plan {
		switch spec := item.Spec.(type) {
		case v1alpha1.WorkloadSpec:
			var domains []string
			for _, p := range spec.Ports {
				domains = append(domains, p.Domains...)
			}
			if len(domains) > 0 {
				out = append(out, hostedDomainClaim{Workload: item.Name, Tier: item.Tier, Domains: domains})
			}
		case v1alpha1.StaticSiteSpec:
			if len(spec.Domains) > 0 {
				out = append(out, hostedDomainClaim{Workload: item.Name, Tier: item.Tier, Domains: spec.Domains})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workload < out[j].Workload })
	return out
}

// checkCustomDomains refuses a hosted deploy whose plan carries a custom
// domain in spec, BEFORE any EnsureDeployment or publish.
//
// This is a BACKSTOP, not the primary gate. The render refuses the same
// declaration first (kcl/render.k `_hosted_domain_violations`), so an author
// editing KCL sees it from `forge env render` and `--plan`, not only against
// a live control plane. This catches the paths that reach a hosted plan
// without going through that lowering — a spec built in Go, or a stale
// release artifact rendered by an older forge — where publishing would
// otherwise store a domain the control plane does not read as a binding and
// never serves.
func checkCustomDomains(envName string, plan []hostedPlanItem) error {
	claims := hostedDomainClaims(plan)
	if len(claims) == 0 {
		return nil
	}
	var lines []string
	for _, c := range claims {
		lines = append(lines, fmt.Sprintf("    %s (%s): %s", c.Workload, c.Tier, strings.Join(c.Domains, ", ")))
	}
	first := claims[0].Domains[0]
	return fmt.Errorf("hosted env %q: refusing to publish anything — %d workload(s) carry a custom domain in their spec, "+
		"and on hosted a domain is not spec:\n%s\n"+
		"  A hosted hostname needs DNS at your registrar, ownership verification and a certificate. "+
		"That is asynchronous and human-gated, so it is a control-plane resource bound to ONE environment, "+
		"not a field this deploy converges.\n"+
		"  fix: `forge domain add %s`, then `forge domain bind %s --env %s --target %s`, "+
		"and remove `domains` from the spec. (forge.OnCluster keeps `Port.domains`: there you own the ingress.)",
		envName, len(claims), strings.Join(lines, "\n"), first, first, envName, claims[0].Workload)
}

// ─── Per-domain status ───────────────────────────────────────────────────────

// The DeployCustomDomainState value names the control plane reports. Decoded
// tolerantly: an unrecognised value is "unknown", never defaulted toward
// live. A domain forge reports as live is one the platform said is live.
const (
	wireDomainStatePrefix = "DEPLOY_CUSTOM_DOMAIN_STATE_"
)

// wireDNSRecord is one DNS record the author must set at their registrar.
type wireDNSRecord struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// wireCustomDomainStatus is one declared domain as the control plane
// observes it.
type wireCustomDomainStatus struct {
	Domain          string          `json:"domain,omitempty"`
	State           string          `json:"state,omitempty"`
	RequiredRecords []wireDNSRecord `json:"requiredRecords,omitempty"`
	LastError       string          `json:"lastError,omitempty"`
	LiveSince       *time.Time      `json:"liveSince,omitempty"`
}

// HostedDNSRecord is one record the author must publish at their registrar
// before a domain can go live.
type HostedDNSRecord struct {
	// Type is A | CNAME | TXT.
	Type string `json:"type,omitempty"`
	// Name is the record name, as the registrar wants it.
	Name string `json:"name,omitempty"`
	// Value is the record's value — an address, a target, or a token.
	Value string `json:"value,omitempty"`
}

// HostedCustomDomain is one declared custom hostname's observed state.
type HostedCustomDomain struct {
	Domain string `json:"domain"`
	// State is the lower-case vocabulary: pending_dns | verifying |
	// issuing | live | failed | conflict | unknown.
	State string `json:"state"`
	// RequiredRecords are the DNS records still to be set. The platform
	// reports them for as long as they matter, so a domain stuck in
	// pending_dns always carries the reason it is stuck.
	RequiredRecords []HostedDNSRecord `json:"required_records,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	// LiveSince is RFC3339 for when the domain started serving, empty
	// until it does.
	LiveSince string `json:"live_since,omitempty"`
}

// The domain-state vocabulary forge reports.
const (
	DomainStatePendingDNS = "pending_dns"
	DomainStateVerifying  = "verifying"
	DomainStateIssuing    = "issuing"
	DomainStateLive       = "live"
	DomainStateFailed     = "failed"
	DomainStateConflict   = "conflict"
	DomainStateUnknown    = "unknown"
)

// DomainStateName renders a DeployCustomDomainState value name in forge's
// lower-case vocabulary. UNSPECIFIED and anything forge does not recognise
// are "unknown" — a control plane newer than this forge reports a state this
// binary has never heard of, and guessing at it is how a not-yet-serving
// domain reads as serving.
//
// Exported so `forge domain` decodes the state with THIS function rather
// than its own copy: the same enum reaches the CLI from two services
// (DomainService.Domain.state and DeployService's observed domains), and two
// decoders would eventually disagree about what "live" means.
func DomainStateName(wire string) string {
	switch v := strings.ToLower(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(wire)), wireDomainStatePrefix)); v {
	case DomainStatePendingDNS, DomainStateVerifying, DomainStateIssuing, DomainStateLive, DomainStateFailed, DomainStateConflict:
		return v
	default:
		return DomainStateUnknown
	}
}

// customDomainsOf decodes the observed per-domain statuses, in the order the
// control plane reported them.
func customDomainsOf(obs *wireObserved) []HostedCustomDomain {
	if obs == nil || len(obs.Domains) == 0 {
		return nil
	}
	out := make([]HostedCustomDomain, 0, len(obs.Domains))
	for _, d := range obs.Domains {
		hd := HostedCustomDomain{Domain: d.Domain, State: DomainStateName(d.State), LastError: d.LastError}
		if d.LiveSince != nil {
			hd.LiveSince = d.LiveSince.UTC().Format(time.RFC3339)
		}
		for _, r := range d.RequiredRecords {
			hd.RequiredRecords = append(hd.RequiredRecords, HostedDNSRecord(r))
		}
		out = append(out, hd)
	}
	return out
}

// FormatCustomDomains renders the per-domain block that `forge env status`
// and the post-deploy summary print, one line per domain plus the DNS
// records still required, indented under indent. Empty for a workload with
// no custom domains, so a caller can print it unconditionally.
//
// The DNS records are printed in the shape a registrar's form asks for —
// type, name, value — because the author's next action is to type them into
// that form, and a status line that says "pending DNS" without saying WHICH
// record is missing sends them to a doc instead.
func FormatCustomDomains(indent string, domains []HostedCustomDomain) string {
	if len(domains) == 0 {
		return ""
	}
	var b strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&b, "%s%s: %s", indent, d.Domain, d.State)
		if d.LiveSince != "" {
			fmt.Fprintf(&b, " since %s", d.LiveSince)
		}
		b.WriteString("\n")
		if d.LastError != "" {
			fmt.Fprintf(&b, "%s  error: %s\n", indent, d.LastError)
		}
		for _, r := range d.RequiredRecords {
			fmt.Fprintf(&b, "%s  DNS record to set: %s %s → %s\n", indent, r.Type, r.Name, r.Value)
		}
	}
	return b.String()
}
