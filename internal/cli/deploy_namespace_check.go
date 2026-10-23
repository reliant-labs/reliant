// File: internal/cli/deploy_namespace_check.go
//
// `checkNamespaceReferences` is a deploy-time guard for one specific
// silent failure mode: KCL env_var values hardcode an in-cluster DNS
// reference like `nats.<ns>.svc.cluster.local`, but the deploy
// namespace forge resolves (from `environments[].namespace` or the
// `<project>-<env>` fallback) doesn't match `<ns>`. The manifests apply
// cleanly to one namespace; the pods inside them resolve service DNS
// against a different namespace; everything CrashLoops with cryptic
// `no such host` errors that don't point at the misconfiguration.
//
// We catch this BEFORE manifests apply so the broken deploy never ships.
//
// Detection heuristic:
//
//   - Scan every env_var value across services / operators / cronjobs
//     for `*.<ns>.svc.cluster.local` substrings.
//   - Filter the extracted namespaces to "project-prefixed" ones — those
//     equal to the project name or starting with `<project>-`. Foreign
//     namespaces (e.g. `nats.nats-system.svc.cluster.local`, where the
//     project consumes shared infra in a different namespace) are
//     legitimate cross-namespace references and not flagged.
//   - Any project-prefixed namespace that doesn't match the resolved
//     deploy namespace is a mismatch. Fail loud with the values that
//     triggered it, so the user can either declare
//     `environments[].namespace` explicitly or fix the KCL literal.

package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// clusterDNSPattern matches the namespace fragment in a `.<ns>.svc.cluster.local`
// suffix. The capture is the namespace label only (no leading dot, no
// trailing `.svc...`). Namespaces follow DNS-1123: lowercase alphanumeric
// + hyphens, no leading/trailing hyphen. The regex is intentionally
// permissive on the host prefix so any leading service name matches.
var clusterDNSPattern = regexp.MustCompile(`\.([a-z0-9]([-a-z0-9]*[a-z0-9])?)\.svc\.cluster\.local`)

// envVarHit records one offending env var so the error message can name
// the exact value that triggered it. Sorted + de-duplicated before
// formatting.
type envVarHit struct {
	owner     string // "service tasks", "operator scaler", etc.
	name      string // env var name
	namespace string // extracted namespace fragment
	value     string // full env var value
}

// checkNamespaceReferences walks the rendered KCL entities for any
// project-prefixed `*.svc.cluster.local` reference that disagrees with
// the resolved deploy namespace. Returns nil when everything matches
// (or when there are no references at all — most projects).
//
// projectName is forge.yaml `name`. resolvedNamespace is the value
// `runDeploy` ended up with after consulting --namespace, KCL's
// K8sCluster.namespace, and the `<project>-<env>` fallback.
func checkNamespaceReferences(entities *KCLEntities, projectName, resolvedNamespace string) error {
	if entities == nil || projectName == "" || resolvedNamespace == "" {
		return nil
	}

	var hits []envVarHit

	collect := func(owner string, vars []KCLEnvVar) {
		for _, v := range vars {
			for _, m := range clusterDNSPattern.FindAllStringSubmatch(v.Value, -1) {
				ns := m[1]
				if !isProjectPrefixed(ns, projectName) {
					// Legitimate cross-namespace reference (e.g. shared
					// `nats-system`). Out of scope for this check.
					continue
				}
				if ns == resolvedNamespace {
					continue
				}
				hits = append(hits, envVarHit{
					owner:     owner,
					name:      v.Name,
					namespace: ns,
					value:     v.Value,
				})
			}
		}
	}

	// A workload's env is its spec.env, the same on every runtime. Host
	// workloads are in scope too: a host process dialing
	// `*.svc.cluster.local` is the same mistake from the other side.
	// Hosted and build-only workloads are out of scope — the platform owns
	// a hosted workload's namespace, and a build-only one never runs.
	for _, w := range entities.Workloads {
		switch w.Runtime.Type {
		case RuntimeHosted, RuntimeBuildOnly:
			continue
		}
		collect(fmt.Sprintf("%s %q (%s)", w.Kind, w.Name, w.Runtime.Type), w.EnvVars())
	}

	if len(hits) == 0 {
		return nil
	}
	return formatMismatchError(hits, resolvedNamespace)
}

// isProjectPrefixed reports whether ns looks like a namespace that this
// project owns — equal to the project name, or prefixed with
// `<project>-`. The complement (foreign namespaces) is left alone since
// cross-namespace references to shared infra are legitimate.
func isProjectPrefixed(ns, projectName string) bool {
	if ns == projectName {
		return true
	}
	return strings.HasPrefix(ns, projectName+"-")
}

// formatMismatchError builds the loud error message. We dedupe and
// sort so a project with N services all referencing the same wrong
// namespace produces one stable, readable error instead of N noisy
// repeats.
func formatMismatchError(hits []envVarHit, resolvedNamespace string) error {
	type key struct{ owner, name, namespace string }
	seen := make(map[key]envVarHit)
	for _, h := range hits {
		k := key{h.owner, h.name, h.namespace}
		if _, ok := seen[k]; !ok {
			seen[k] = h
		}
	}
	deduped := make([]envVarHit, 0, len(seen))
	for _, h := range seen {
		deduped = append(deduped, h)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].owner != deduped[j].owner {
			return deduped[i].owner < deduped[j].owner
		}
		return deduped[i].name < deduped[j].name
	})

	// Unique referenced namespaces, sorted for stable phrasing.
	nsSet := make(map[string]struct{})
	for _, h := range deduped {
		nsSet[h.namespace] = struct{}{}
	}
	referenced := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		referenced = append(referenced, ns)
	}
	sort.Strings(referenced)

	var b strings.Builder
	fmt.Fprintf(&b, "namespace mismatch: deploy target is %q but env vars reference ", resolvedNamespace)
	if len(referenced) == 1 {
		fmt.Fprintf(&b, "%q", referenced[0])
	} else {
		fmt.Fprintf(&b, "%v", referenced)
	}
	b.WriteString("\n\n")
	b.WriteString("Project-prefixed `*.svc.cluster.local` references must point at the namespace forge\n")
	b.WriteString("is about to deploy into. Otherwise pods will resolve service DNS against an empty\n")
	b.WriteString("namespace and CrashLoop with cryptic `no such host` errors.\n\n")
	b.WriteString("Offending env vars:\n")
	for _, h := range deduped {
		fmt.Fprintf(&b, "  %s — %s=%s\n", h.owner, h.name, h.value)
	}
	b.WriteString("\nFix one of:\n")
	fmt.Fprintf(&b, "  • Declare `environments[<env>].namespace: %s` in forge.yaml to match the KCL literal.\n", referenced[0])
	fmt.Fprintf(&b, "  • Update the KCL env_var values to use `*.%s.svc.cluster.local`.\n", resolvedNamespace)
	b.WriteString("  • Or pass `--namespace` to forge env deploy if the override is a one-off.\n")
	return fmt.Errorf("%s", b.String())
}
