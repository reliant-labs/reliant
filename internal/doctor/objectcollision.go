// Copyright (c) 2025 Reliant Labs
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CheckObjectCollision reports two environments that write to the SAME object
// address. It is one check because it is one defect — last writer wins,
// silently — that shows up at two scopes.
//
// Nothing else catches it. Each env renders correctly on its own, every
// manifest is applyable, and `kubectl apply` is happy to overwrite one env's
// object with another's under the same name — so the collision is only ever
// observed downstream, as one env's workloads mysteriously carrying another
// env's config.
//
// NAMESPACED scope, measured 2026-08-24: `dev-k8s` defaulted to
// `control-plane-dev`, the namespace `dev` deploys into. A dev-k8s deploy
// replaced dev's daemon-gateway in place, repointing it at a different
// database, where the daemon's credentials had never been written. Every
// token came back "invalid" — not expired, looked up in the wrong place — and
// the product reported "no machine is connected" for ten hours.
//
// CLUSTER-SCOPED scope: forge's own `render_cluster_rbac` emits
// `<name>-clusterrolebinding`, which is cluster-scoped and has exactly ONE
// name, so every env that renders it writes the same object — while its
// `subjects[0].namespace` is the ENV's namespace. deploy/kcl/lib/
// control_plane_rbac.k documents the consequence verbatim: a singleton
// whichever env deploys last steals, observed as e2e breaking while the live
// binding's subject pointed at control-plane-dev.
//
// The two scopes differ in ONE respect, and it is the difference between a
// check people read and one they learn to ignore:
//
//   - A namespace is an environment's private address space. Two envs writing
//     the same object into one namespace are fighting over ownership whatever
//     the bytes say today, because being different environments is precisely
//     what makes their content diverge tomorrow.
//   - Cluster scope has no such privacy, and a legitimately-shared singleton
//     is normal there: every env expects the same CRD and the same
//     GatewayClass to exist, identically. So a cluster-scoped address is only
//     a finding when the envs render DIFFERENT content for it — that is what
//     makes it last-writer-wins rather than N idempotent applies of one fact.
//
// WARN, never fail: deliberate co-location exists (a shared-infra namespace
// two envs both attach to), and forge cannot tell intent from accident. What
// it can do is refuse to let the sharing stay invisible.
func CheckObjectCollision(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "object collisions", objectCollisions)
}

// objectCollisions is the body, handed ONLY the environments that rendered.
//
// That matters more here than for a per-object check. This one reasons about
// what environments have in COMMON, so an env missing from the list does not
// merely go unexamined — it silently narrows the search space, and every
// collision it was half of disappears with it. Under the old preamble a
// partial render therefore produced a confident "no two environments write
// the same object" that was false BECAUSE an environment was missing: a green
// on precisely the defect the check exists to catch. examineRendered folds
// that Pass to UNDETERMINED and names the env nobody read.
func objectCollisions(renders []envRender) CheckResult {
	claims := map[objectAddress]map[string]envObject{}
	for _, r := range renders {
		for _, o := range r.objects {
			kind, name := strings.TrimSpace(o.Kind), strings.TrimSpace(o.Metadata.Name)
			if kind == "" || name == "" {
				// Not addressable, so not applyable either — that is
				// CheckDeployManifests' finding, not a second copy of it here.
				continue
			}
			ns := strings.TrimSpace(o.Metadata.Namespace)
			clusters := r.clustersOf(o)
			if len(clusters) == 0 {
				// The env declares no cluster at all. Recorded under the empty
				// cluster and reported as UNDETERMINED — never merged with the
				// real ones, which would be assuming the very thing (do these
				// envs share a cluster?) that could not be determined.
				clusters = []string{""}
			}
			for _, c := range clusters {
				addr := objectAddress{cluster: c, namespace: ns, kind: kind, name: name}
				if claims[addr] == nil {
					claims[addr] = map[string]envObject{}
				}
				// An env that renders one address twice is not colliding with
				// ITSELF; first rendering keeps the slot.
				if _, dup := claims[addr][r.env]; !dup {
					claims[addr][r.env] = canonicalize(o.raw)
				}
			}
		}
	}

	var collisions, undetermined []string
	for addr, envs := range claims {
		if len(envs) < 2 {
			continue
		}
		who := strings.Join(sortedEnvNames(envs), ", ")
		switch {
		case addr.cluster == "":
			undetermined = append(undetermined, fmt.Sprintf(
				"%s/%s ← %s  (which cluster these envs deploy to could not be determined)",
				addr.kind, addr.name, who))
		case addr.namespace != "":
			collisions = append(collisions, fmt.Sprintf("%s / %s  %s/%s ← %s",
				addr.cluster, addr.namespace, addr.kind, addr.name, who))
		default:
			field, comparable := contentDiff(envs)
			switch {
			case !comparable:
				undetermined = append(undetermined, fmt.Sprintf(
					"%s  %s/%s ← %s  (content could not be compared, so shared-infra vs. collision is unknown)",
					addr.cluster, addr.kind, addr.name, who))
			case field == "":
				// Byte-identical everywhere: shared infrastructure every env
				// expects to exist, not a collision. Silent by design.
			default:
				collisions = append(collisions, fmt.Sprintf("%s  %s/%s ← %s  (%s differs)",
					addr.cluster, addr.kind, addr.name, who, field))
			}
		}
	}
	sort.Strings(collisions)
	sort.Strings(undetermined)

	res := CheckResult{}
	switch {
	case len(collisions) > 0:
		res.Status = StatusWarn
		res.Message = fmt.Sprintf(
			"%d object address(es) are written by MORE THAN ONE environment — whichever deploys last silently replaces the other's",
			len(collisions))
		res.Evidence = evidenceLines(collisions) + "\n\n" +
			"Each env renders fine alone and kubectl overwrites a same-named object without complaint,\n" +
			"so this surfaces only as one env's workloads carrying another env's config. Give each env\n" +
			"its own namespace, or — for a cluster-scoped singleton that cannot be per-env — make the\n" +
			"object's name carry the env so each gets its own."
		if len(undetermined) > 0 {
			res.Evidence += "\n\nAlso undetermined:\n" + evidenceLines(undetermined)
		}
	case len(undetermined) > 0:
		res.Status = StatusUnknown
		res.Message = fmt.Sprintf(
			"%d shared object address(es) could not be judged — the render did not say enough to tell a collision from shared infrastructure",
			len(undetermined))
		res.Evidence = evidenceLines(undetermined)
	default:
		res.Status = StatusPass
		res.Message = fmt.Sprintf("%d object address(es) across %d environment(s) — no two environments write the same one",
			len(claims), len(renders))
	}
	return res
}

// objectAddress is what `kubectl apply` overwrites BY. An object is
// identified by the cluster it lands on, its namespace (EMPTY for a
// cluster-scoped object, which has none), its kind and its name — so two envs
// rendering one address are two envs writing one object.
type objectAddress struct {
	cluster   string
	namespace string
	kind      string
	name      string
}

// envObject is one environment's rendering of one address, canonicalized so
// two renderings can be compared for content.
type envObject struct {
	canonical string
	tree      any
	ok        bool
}

// canonicalize decodes a rendered object and re-encodes it in a form where
// key ORDER cannot make two identical objects look different: Go marshals a
// map with its keys sorted, and KCL's emission order is not a contract worth
// depending on. ok is false when the bytes do not decode, which the caller
// reports as undetermined rather than as "different".
func canonicalize(raw []byte) envObject {
	if len(raw) == 0 {
		return envObject{}
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return envObject{}
	}
	stripEnvOwnershipLabel(tree)
	c, err := json.Marshal(tree)
	if err != nil {
		return envObject{}
	}
	return envObject{canonical: string(c), tree: tree, ok: true}
}

// contentDiff answers whether every env renders one cluster-scoped address
// identically, and if not, WHICH field first disagrees. The second return is
// false when some rendering could not be canonicalized — undetermined, not
// equal, because reporting "identical" on bytes nobody could read would
// silently clear the exact case this check exists for.
func contentDiff(envs map[string]envObject) (string, bool) {
	names := sortedEnvNames(envs)
	base := envs[names[0]]
	if !base.ok {
		return "", false
	}
	for _, n := range names[1:] {
		other := envs[n]
		if !other.ok {
			return "", false
		}
		if other.canonical == base.canonical {
			continue
		}
		if field := firstJSONDiff(base.tree, other.tree, ""); field != "" {
			return field, true
		}
		// Canonical forms differ but the walk found no path — should not
		// happen; say so rather than report a benign singleton.
		return "content", true
	}
	return "", true
}

// stripEnvOwnershipLabel removes forge's per-env ownership stamp from a
// decoded object before its content is compared.
//
// forge stamps `forge.dev/env: <env>` onto EVERY object it renders
// (kcl/lib/labels.k) so `kubectl get all -l forge.dev/env=dev` can answer who
// owns what. Its value is the environment name, which means it differs
// between any two environments BY CONSTRUCTION. That makes it worthless as
// evidence of a content difference, and actively harmful as evidence of one:
//
//   - Every legitimately-shared cluster-scoped singleton — an identical CRD,
//     GatewayClass or ClusterRole that all envs expect to exist — would read
//     as "differing" and warn. That is the permanent yellow this check is
//     built to avoid, and it would arrive not from a defect but from a label
//     doing its job.
//   - A REAL collision would be misattributed. firstJSONDiff reports the
//     FIRST differing path in sorted key order, and `metadata` sorts before
//     `subjects` — so the ClusterRoleBinding whose subject namespace is the
//     actual outage would be reported as `metadata.labels.forge.dev/env
//     differs`, pointing at the one field that was always going to differ.
//
// Removing it asks the question that matters: are these identical APART from
// the stamp that could never be identical? The walk is recursive because the
// stamp reaches pod templates too, and deleting a key that is per-env by
// construction is safe wherever it appears.
func stripEnvOwnershipLabel(tree any) {
	switch v := tree.(type) {
	case map[string]any:
		if labels, ok := v["labels"].(map[string]any); ok {
			delete(labels, forgeEnvLabel)
		}
		for _, child := range v {
			stripEnvOwnershipLabel(child)
		}
	case []any:
		for _, child := range v {
			stripEnvOwnershipLabel(child)
		}
	}
}

// firstJSONDiff walks two decoded JSON trees together and returns a dotted
// path to the FIRST place they disagree ("subjects[0].namespace"), or "" when
// they match. One path, not a full diff: it turns "these differ somehow" into
// the single field to go look at, which is the whole difference between a
// finding someone acts on and one they scroll past.
func firstJSONDiff(a, b any, path string) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return pathOrRoot(path)
		}
		seen := map[string]bool{}
		for k := range av {
			seen[k] = true
		}
		for k := range bv {
			seen[k] = true
		}
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		// Sorted so the reported field is deterministic — a finding that
		// names a different field each run reads as flapping.
		sort.Strings(keys)
		for _, k := range keys {
			x, inA := av[k]
			y, inB := bv[k]
			if !inA || !inB {
				return childPath(path, k)
			}
			if d := firstJSONDiff(x, y, childPath(path, k)); d != "" {
				return d
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return pathOrRoot(path)
		}
		for i := range av {
			if d := firstJSONDiff(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	default:
		// Only scalars reach here (string / float64 / bool / nil): the two
		// composite kinds are handled above, so != cannot panic on an
		// uncomparable dynamic type.
		if a != b {
			return pathOrRoot(path)
		}
		return ""
	}
}

func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// pathOrRoot names the whole document when the disagreement is at the top
// level, so the evidence never reads "( differs)".
func pathOrRoot(path string) string {
	if path == "" {
		return "the object"
	}
	return path
}

func sortedEnvNames(envs map[string]envObject) []string {
	names := make([]string, 0, len(envs))
	for e := range envs {
		names = append(names, e)
	}
	sort.Strings(names)
	return names
}

// evidenceLines caps the listing. Two envs sharing a namespace collide on
// EVERY object in it, and a hundred-line wall of them buries the one line
// that says which envs to look at.
func evidenceLines(lines []string) string {
	const max = 12
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:max], "\n") +
		fmt.Sprintf("\n… and %d more", len(lines)-max)
}
