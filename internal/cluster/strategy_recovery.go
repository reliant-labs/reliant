package cluster

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The rollout-strategy recovery: healing a Deployment that was created as
// RollingUpdate and is now declared Recreate.
//
// WHY THIS EXISTS
// ===============
// A control-plane PROD deploy failed with:
//
//	The Deployment "control-plane-workers" is invalid:
//	spec.strategy.rollingUpdate: Forbidden: may not be specified when
//	strategy `type` is 'Recreate'
//
// forge's render was already correct — `strategy: {type: Recreate}`, with no
// rollingUpdate anywhere in the manifest. The failure is a Server-Side Apply
// ownership problem, and it is worth being precise about, because the obvious
// fix does not work:
//
//  1. The Deployment was first applied as RollingUpdate. forge owned
//     `spec.strategy.type`; the API server's own defaulting then materialized
//     `spec.strategy.rollingUpdate` (maxSurge/maxUnavailable 25%) under the
//     field manager `kube-controller-manager`, operation `Update`.
//  2. A later deploy declares Recreate. SSA merges forge's apply config with
//     the fields OTHER managers own. forge's manifest does not mention
//     rollingUpdate, so nothing in the apply removes it — a manager can only
//     drop a field it owns, and forge never owned this one.
//  3. The merged object therefore carries BOTH `type: Recreate` and a
//     populated `rollingUpdate`, which Deployment validation rejects
//     outright. The deploy dies, and it stays dead on every retry: the apply
//     is deterministic, so this is a permanent wedge, not a flake.
//
// --force-conflicts does not help. It overrides a CONFLICT — another manager
// owning a field forge also sets — and here there is no conflict to force:
// forge is not setting rollingUpdate at all.
//
// WHY NOT `rollingUpdate: null` IN THE RENDER
// ===========================================
// The tempting fix is declarative and stateless: have the renderer emit an
// explicit `rollingUpdate: null` next to `type: Recreate`, on the theory that
// SSA reads an explicit null as "remove this field". **It does not work, and
// it was measured against a real API server (kind, k8s v1.34) before this
// code was written.** Two independent reasons:
//
//   - kubectl STRIPS nulls out of the apply configuration before sending it,
//     so `kubectl apply --server-side` with an explicit null is byte-identical
//     to omitting the field, and fails exactly as before.
//   - Bypassing kubectl and PATCHing the API server directly with
//     `Content-Type: application/apply-patch+yaml` and an explicit
//     `rollingUpdate: null` is REJECTED — HTTP 422, the same Forbidden
//     message. In SSA, null means "I do not set this field", never "delete
//     it"; field removal is expressed by ABSENCE from an owning manager's
//     apply config, which is precisely the mechanism unavailable here.
//
// So the fix has to be an operation that can remove a field forge does not
// own. A merge patch can: `null` in a JSON merge patch is a deletion.
//
// WHY THE PATCH MUST CARRY THE TYPE TOO
// =====================================
// Patching only `{"strategy":{"rollingUpdate":null}}` is a NO-OP — measured:
// `deployment.apps/probe patched (no change)`. While the live type is still
// RollingUpdate, defaulting immediately re-materializes rollingUpdate, so the
// removal and the type change must land in the SAME write. The patch sends
// the rendered strategy block with rollingUpdate explicitly nulled, which
// makes the object valid; the SSA re-apply then re-establishes forge's
// ownership of `spec.strategy.type` normally.
//
// SCOPE — this is Deployment-only, and that is a finding, not an assumption
// ========================================================================
// The same RollingUpdate→other-type transition was tested on the other two
// workload kinds with a defaulted update strategy:
//
//   - StatefulSet: the API server does not default
//     `spec.updateStrategy.rollingUpdate` when the type is RollingUpdate, so
//     there is no stale field to strand. RollingUpdate→OnDelete applies
//     cleanly.
//   - DaemonSet: it DOES default `spec.updateStrategy.rollingUpdate`, and the
//     field survives the transition to OnDelete — but DaemonSet validation
//     PERMITS the combination, so the apply succeeds (the stale field is
//     inert).
//
// Deployment is the only kind whose validation makes the leftover field fatal.
// forge also renders no StatefulSets or DaemonSets today. The recovery is
// keyed off the API server's own error text rather than a kind list, so a kind
// that starts rejecting this combination is handled by the same code path
// without a change here.
//
// COST ON THE HAPPY PATH: none. Nothing runs unless an apply has already
// FAILED with this specific validation error.

// strategyTarget identifies one Deployment whose live rollout strategy has to
// be reconciled by a patch before forge's apply can be accepted, and carries
// the strategy block the manifest DECLARES — the patch's payload.
type strategyTarget struct {
	Kind      string
	Name      string
	Namespace string // empty = namespace defaulted at apply time
	// Strategy is the rendered `spec.strategy` map (e.g. {"type":"Recreate"}).
	// The patch sends this with rollingUpdate explicitly nulled, so the type
	// change and the stale-field removal are one atomic write.
	Strategy map[string]any
}

// strategyConflictMarker is the API server's validation error for a Deployment
// carrying a defaulted rollingUpdate under a non-RollingUpdate type. Matching
// the server's text (rather than enumerating kinds) is what keeps the recovery
// correct if another kind adopts the same validation.
const strategyConflictMarker = "spec.strategy.rollingUpdate: Forbidden"

// strategyConflicts returns EVERY resource in one apply whose failure was the
// stale-rollingUpdate wedge, pairing each with the strategy its manifest
// declares. It mirrors immutableResources' segment scan: forge applies the
// whole workload batch in a single SSA call, so one apply can report this for
// more than one Deployment, and healing only the first would leave the rest to
// fail the re-apply.
//
// A resource is returned ONLY when its own error body carries the marker and
// its manifest document actually declares a strategy — without the rendered
// strategy there is nothing to patch TO, and guessing would be worse than
// surfacing the original error.
func strategyConflicts(stderr, manifests string) []strategyTarget {
	const inv = " is invalid:"
	var out []strategyTarget
	seen := map[string]struct{}{}
	rest := stderr
	for {
		idx := strings.Index(rest, inv)
		if idx < 0 {
			break
		}
		head := rest[:idx+len(inv)]
		rest = rest[idx+len(inv):]
		// Scope the marker check to THIS resource's error body — the text up
		// to the next `is invalid:` — so a later resource's strategy failure
		// cannot be attributed to an earlier, unrelated one.
		body := rest
		if next := strings.Index(rest, inv); next >= 0 {
			body = rest[:next]
		}
		if !strings.Contains(body, strategyConflictMarker) {
			continue
		}
		kind, name, ok := parseInvalidResource(head)
		if !ok {
			continue
		}
		strategy, ok := strategyForResource(manifests, kind, name)
		if !ok {
			continue
		}
		ns := namespaceForResource(manifests, kind, name)
		key := kind + "\x00" + name + "\x00" + ns
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, strategyTarget{Kind: kind, Name: name, Namespace: ns, Strategy: strategy})
	}
	return out
}

// strategyForResource pulls the declared `spec.strategy` out of the manifest
// document whose kind+name match. ok=false when no document matches or it
// declares no strategy.
func strategyForResource(manifests, kind, name string) (map[string]any, bool) {
	for _, doc := range splitDocs(manifests) {
		var m struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Strategy map[string]any `yaml:"strategy"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			continue
		}
		if m.Kind == kind && m.Metadata.Name == name {
			if len(m.Spec.Strategy) == 0 {
				return nil, false
			}
			return m.Spec.Strategy, true
		}
	}
	return nil, false
}

// strategyMergePatch builds the JSON merge patch that reconciles one
// Deployment's rollout strategy: the DECLARED strategy block plus an explicit
// `rollingUpdate: null`, which a merge patch (unlike SSA) treats as a
// deletion. Sending both in one write is required — see the file comment.
func strategyMergePatch(t strategyTarget) (string, error) {
	strategy := make(map[string]any, len(t.Strategy)+1)
	if _, declared := t.Strategy["rollingUpdate"]; !declared {
		// Explicit null = delete, for whichever manager owns it. Only when
		// the manifest does NOT declare one: a Deployment that legitimately
		// tunes maxSurge/maxUnavailable must keep its declared values, and
		// copying the declared block below would otherwise be overwritten.
		strategy["rollingUpdate"] = nil
	}
	for k, v := range t.Strategy {
		strategy[k] = v
	}
	b, err := json.Marshal(map[string]any{"spec": map[string]any{"strategy": strategy}})
	if err != nil {
		return "", fmt.Errorf("marshal strategy patch for %s %q: %w", t.Kind, t.Name, err)
	}
	return string(b), nil
}

// applyWithStrategyRecovery runs apply and, on the stale-rollingUpdate
// validation failure ONLY, merge-patches each offending resource's strategy to
// what the manifest declares and re-applies. The kubectl-touching steps are
// closures so the sequencing is unit-testable without a live cluster.
//
// Unlike the immutable recovery this does NOT loop: the patch is a single
// authoritative write with no finalization race to lose, so one cycle either
// fixes it or the failure is something else. Any failure that is not this
// specific case surfaces unchanged, and the returned stdout is always the
// WINNING apply's — the failed first attempt's object list under-reports what
// landed, which would trip the apply-completeness check on a healed deploy.
func applyWithStrategyRecovery(
	manifests string,
	apply func() (stdout, stderr string, err error),
	patch func(strategyTarget) error,
) (stdout, stderr string, err error) {
	stdout, stderr, err = apply()
	if err == nil {
		return stdout, stderr, nil
	}
	targets := strategyConflicts(stderr, manifests)
	if len(targets) == 0 {
		// Not the recoverable strategy case — surface unchanged so the
		// immutable recovery (and ultimately the user) sees the real error.
		return stdout, stderr, err
	}
	for _, t := range targets {
		fmt.Printf("[deploy]   Reconciling rollout strategy of %s %q (created as RollingUpdate, now %v)\n",
			t.Kind, t.Name, t.Strategy["type"])
		if pErr := patch(t); pErr != nil {
			// A patch failure is its own problem — surface it rather than the
			// original validation error so the cause is visible.
			return stdout, stderr, fmt.Errorf("reconciling rollout strategy of %s %q: patch: %w", t.Kind, t.Name, pErr)
		}
	}
	return apply()
}
