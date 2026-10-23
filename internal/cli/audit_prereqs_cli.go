// Package cli — `forge project audit` prerequisites category.
//
// Surfaces the env's DECLARED external prerequisites (forge.ExternalSecret /
// forge.DNSRecord on Bundle.required_secrets / required_dns) — the
// out-of-band facts a deploy depends on but forge does NOT create. Today
// these prereqs live only in KCL docstrings, so `forge env deploy` renders
// green and THEN cert-manager's ACME challenge or the workspace-proxy DNS
// hangs silently because the prereq was never satisfied. Modeling them as
// first-class entities turns the docstring into:
//
//   - a render-time CHECKLIST (this audit category + the deploy banner),
//   - a deploy PREFLIGHT block on a declared-required-but-absent
//     ExternalSecret (see cluster.Preflight / RequiredSecrets), and
//   - a cross-secret BYTE-MATCH consistency check: ExternalSecrets sharing
//     a `value_group` carry the SAME logical value; this category reports
//     the group membership (the KCL schema already blocks a group whose
//     members declare different key sets, and the live byte-compare runs in
//     the preflight).
//
// Like auditIngress this renders the dev env (the only env every project is
// guaranteed to have) purely to ENUMERATE the declarations; the category is
// informational (status ok/warn), never an error — a declared prereq is a
// good thing, and whether it's SATISFIED is a live-cluster question the
// deploy preflight answers, not a static audit.
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// auditPrerequisites renders the dev env and reports the declared external
// prerequisites. Degrades to warn (never error) when KCL can't be evaluated
// — a missing toolchain shouldn't fail the whole audit, and an absent prereq
// declaration is the unremarkable default.
func auditPrerequisites(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	if cfg == nil {
		return audittype.Category{Status: audittype.StatusError, Summary: "no forge.yaml"}
	}
	// External prerequisites (ExternalSecret / DNSRecord) are a DEPLOY-time
	// concern — they hang off rendered KCL bundles. A project with no deploy
	// surface (kind=cli or library has no deploy/kcl/) has nothing to render,
	// so the dev-KCL prereq is n/a, not a warning. Reporting ✓ n/a here keeps
	// `forge project audit` green for the CLI/library shapes that never deploy.
	if cfg.IsCLIKind() || cfg.IsLibraryKind() {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: fmt.Sprintf("n/a — kind=%s has no deploy; external prerequisites are a deploy-time concern", cfg.EffectiveKind()),
		}
	}
	// The kind is not the evidence: a service-shaped project that forge does
	// not deploy (no deploy/kcl, or deploy pinned off) has nothing to render
	// either, and reporting its absent dev KCL as a warning is noise.
	if !cfg.Features.DeployEnabled() {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: "n/a — the deploy feature is off (no deploy/kcl, or pinned off); external prerequisites are a deploy-time concern",
		}
	}
	ctx := context.Background()
	entities, err := RenderKCL(ctx, projectDir, "dev")
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("could not evaluate dev KCL: %v", err),
		}
	}
	// Render the dev manifest stream and run the render-time secret
	// back-propagation gate: a workload mounting/referencing a Secret nothing
	// in the bundle provides (no rendered Secret / KubeconfigSecret /
	// ExternalSecret) is an ERROR — the pod would FailedMount forever. This is
	// the static, no-cluster half of the same gate the deploy preflight runs;
	// surfacing it in audit lets CI catch a silently-undeclared mount without a
	// deploy. A render failure here is non-fatal (the declarations above still
	// audit); we just skip the supply check.
	//
	// It runs under the SAME condition the deploy runs it: only when forge
	// itself applies this env's manifest stream to a cluster. See
	// envAppliesManifestsToCluster for why a stream nothing applies is not
	// demand.
	var undeclared []cluster.UndeclaredSecretMount
	supplyChecked := envAppliesManifestsToCluster(entities)
	if supplyChecked {
		mainK := filepath.Join(projectDir, "deploy", "kcl", "dev", "main.k")
		if manifests, merr := cluster.RenderManifests(ctx, mainK, "audit", "", "dev", nil, nil); merr == nil {
			undeclared = cluster.CheckSecretSupply(manifests, secretSupplyForPreflight(entities))
		}
	}
	cat := crossCheckPrereqs(entities.RequiredSecrets, entities.RequiredDNS, undeclared)
	if !supplyChecked {
		cat.Details["secret_supply_check"] = "n/a — dev places no workload in a cluster, so its manifest stream is never applied and no Secret it references can FailedMount"
	}
	return cat
}

// envAppliesManifestsToCluster reports whether `forge env deploy` applies this
// env's rendered manifest stream to a cluster ITSELF — the only case in which a
// secretKeyRef or secret volume in that stream is demand a Secret must meet.
//
// It is the condition the deploy preflight already gates the same check on
// (gateDeployOnPreflight skips it when kclEntitiesHaveK8sCluster is false), so
// the audit and the deploy agree on when a mount is undeclared:
//
//   - Nothing placed in a cluster (no cluster-bound workload, no env-wide
//     cluster_target, no self-managed database): nothing is applied, so
//     there is no demand to check.
//   - Hosted workloads: the control plane owns the cluster and materializes
//     the Secrets it reads (forge-managed-secrets, the CNPG credential), and
//     forge applies nothing — the deploy publishes Workload specs instead.
//
// Anything else (a Cluster runtime, a self-managed database, an env-wide
// cluster_target for support objects, a LOCAL control-plane env) keeps the
// gate. That is safe for the scaffolded dev env, whose workloads all run as
// host processes beside a cluster_target for its Namespace and gateways: a
// host-bound workload never enters `output.manifests`, so its secrets —
// which reach it from the secret store as env values — are never counted
// as cluster demand. Counting them used to fail the audit for any project
// whose workload declares a `config_secrets` credential, on every checkout.
func envAppliesManifestsToCluster(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	// Only a PERSISTENT env's manifests are applied by the platform. A LOCAL
	// or SELF_MANAGED control-plane env is applied from this machine, so its
	// mounts are demand forge must meet.
	if hostedEnvKindOf(e) == deploytarget.HostedEnvPersistent {
		return false
	}
	return kclEntitiesHaveK8sCluster(e)
}

// crossCheckPrereqs is the pure decision core: takes the declared external
// Secret + DNS prerequisites and returns the audittype.Category. Split out
// so unit tests exercise it without shelling kcl.
//
// Findings (all informational — a declared prereq never makes the audit
// fail):
//
//   - one line per declared ExternalSecret (name @ namespace, keys, reason),
//   - one line per declared DNSRecord (host, type, target, reason),
//   - one line per cross-secret byte-match group naming its members (so the
//     operator sees which Secrets must carry identical bytes).
func crossCheckPrereqs(secrets []ExternalSecretEntity, dns []DNSRecordEntity, undeclared []cluster.UndeclaredSecretMount) audittype.Category {
	var findings []string

	for _, s := range secrets {
		line := fmt.Sprintf("secret: %s/%s keys=%v", s.Namespace, s.Name, s.Keys)
		if s.Reason != "" {
			line += " — " + s.Reason
		}
		findings = append(findings, line)
	}
	for _, r := range dns {
		line := fmt.Sprintf("dns: %s (%s)", r.Host, r.Type)
		if r.Target != "" {
			line += " -> " + r.Target
		}
		if r.Reason != "" {
			line += " — " + r.Reason
		}
		findings = append(findings, line)
	}

	// Cross-secret byte-match groups: a value_group with >1 member is a set
	// of Secrets that must carry the same logical bytes (the live compare is
	// a preflight check; here we surface membership). A group of size 1 is a
	// declaration smell — a byte-match group with one member matches nothing
	// — so we surface it as a (non-blocking) note.
	groups := byteMatchGroups(secrets)
	groupIDs := make([]string, 0, len(groups))
	for id := range groups {
		groupIDs = append(groupIDs, id)
	}
	sort.Strings(groupIDs)
	singletonGroups := 0
	for _, id := range groupIDs {
		members := groups[id]
		sort.Strings(members)
		if len(members) == 1 {
			singletonGroups++
			findings = append(findings, fmt.Sprintf(
				"value-group %q: only one member (%s) — a byte-match group with a single Secret matches nothing; add the other ref or drop value_group",
				id, members[0]))
			continue
		}
		findings = append(findings, fmt.Sprintf(
			"value-group %q: %d Secrets must carry identical bytes [%s]",
			id, len(members), joinSorted(members)))
	}

	// Undeclared secret mounts: a workload mounts/references a Secret nothing in
	// the bundle provides. This is the static half of the deploy preflight's
	// back-propagation gate — an ERROR (the pod FailedMounts), not a smell.
	var undeclaredFindings []string
	for _, m := range undeclared {
		who := "a workload"
		if len(m.Workloads) > 0 {
			who = joinSorted(m.Workloads)
		}
		undeclaredFindings = append(undeclaredFindings, fmt.Sprintf(
			"undeclared-mount: Secret %q mounted by %s but nothing declares it (no rendered Secret, KubeconfigSecret, or ExternalSecret) — it will FailedMount",
			m.Secret, who))
	}
	findings = append(findings, undeclaredFindings...)

	sort.Strings(findings)

	status := audittype.StatusOK
	if singletonGroups > 0 {
		status = audittype.StatusWarn
	}
	// An undeclared mount is a hard defect — it dominates the singleton warn.
	if len(undeclared) > 0 {
		status = audittype.StatusError
	}
	summary := fmt.Sprintf("%d external secret(s), %d dns record(s), %d byte-match group(s)",
		len(secrets), len(dns), len(groups))
	if len(undeclared) > 0 {
		summary += fmt.Sprintf(", %d UNDECLARED secret mount(s)", len(undeclared))
	}
	details := map[string]any{
		"external_secrets":         len(secrets),
		"dns_records":              len(dns),
		"byte_match_groups":        len(groups),
		"undeclared_secret_mounts": len(undeclared),
	}
	if len(findings) > 0 {
		details["findings"] = findings
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}

// printPrerequisiteChecklist prints the env's declared external
// prerequisites at deploy time so the out-of-band facts the deploy depends
// on are visible every run (not buried in a docstring). The ExternalSecret
// half is also ENFORCED by the deploy preflight (a missing one BLOCKS); the
// DNS half can't be authoritatively verified, so the checklist is its only
// signal. No-op when the env declares no prereqs.
func printPrerequisiteChecklist(entities *KCLEntities) {
	if entities == nil {
		return
	}
	// A route to an OnHost workload renders an Envoy Gateway Backend, which the
	// controller refuses to resolve unless its Backend extension API is enabled.
	// That is an out-of-band fact about the controller's config, in the same
	// category as the declared Secrets and DNS below, and it fails the same way:
	// a clean apply followed by a silent runtime refusal.
	backendPrereqs := routeBackendPrereqFindings(entities)

	if len(entities.RequiredSecrets) == 0 && len(entities.RequiredDNS) == 0 && len(backendPrereqs) == 0 && len(entities.ControlPlaneSecrets) == 0 {
		return
	}
	if len(entities.ControlPlaneSecrets) > 0 {
		fmt.Println("Secrets the control plane delivers AFTER this deploy (they never block it; pods start without an optional one and pick it up, and wait for a required one):")
		for _, c := range entities.ControlPlaneSecrets {
			wait := ""
			if c.Required {
				wait = " (required: pods wait for the first delivery)"
			}
			fmt.Printf("  - Secret %s/%s [%s] on %s via connected cluster %s%s\n", c.Namespace, c.Name, c.Kind, c.Cluster, emptyAs(c.ConnectedCluster, "(none)"), wait)
		}
		fmt.Println()
	}
	if len(entities.RequiredSecrets) == 0 && len(entities.RequiredDNS) == 0 && len(backendPrereqs) == 0 {
		return
	}
	fmt.Println("External prerequisites this env depends on (provisioned out-of-band; forge does NOT create them):")
	for _, f := range backendPrereqs {
		fmt.Println("  - " + f)
	}
	for _, s := range entities.RequiredSecrets {
		line := fmt.Sprintf("  - Secret %s/%s keys=%v", s.Namespace, s.Name, s.Keys)
		if s.ValueGroup != "" {
			line += fmt.Sprintf(" [value-group %q]", s.ValueGroup)
		}
		if s.Reason != "" {
			line += " — " + s.Reason
		}
		fmt.Println(line)
	}
	for _, r := range entities.RequiredDNS {
		line := fmt.Sprintf("  - DNS %s (%s)", r.Host, r.Type)
		if r.Target != "" {
			line += " -> " + r.Target
		}
		if r.Reason != "" {
			line += " — " + r.Reason
		}
		fmt.Println(line)
	}
	if len(entities.RequiredSecrets) > 0 {
		fmt.Println("  (declared Secrets are verified by the deploy preflight; a missing one blocks the deploy.)")
	}
	fmt.Println()
}

// byteMatchGroups buckets the declared ExternalSecrets by value_group,
// returning each non-empty group's member labels ("<namespace>/<name>").
// Secrets with no value_group (standalone prereqs) are excluded.
func byteMatchGroups(secrets []ExternalSecretEntity) map[string][]string {
	groups := map[string][]string{}
	for _, s := range secrets {
		if s.ValueGroup == "" {
			continue
		}
		groups[s.ValueGroup] = append(groups[s.ValueGroup], s.Namespace+"/"+s.Name)
	}
	return groups
}

// joinSorted renders a sorted, comma-separated member list.
func joinSorted(items []string) string {
	cp := append([]string(nil), items...)
	sort.Strings(cp)
	out := ""
	for i, it := range cp {
		if i > 0 {
			out += ", "
		}
		out += it
	}
	return out
}
