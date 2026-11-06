package cluster

// Leaving the objects of hub-converged delivery units to the hub.
//
// An env moves onto the Flux path one delivery unit at a time (design §11), so
// during the move the hub converges some units and `forge env deploy` applies
// the rest. A second applier over a unit is the one outcome the move must never
// produce: forge would revert the hub's work (or the hub forge's), and the
// field-manager handoff the adoption does by hand would be undone on the next
// deploy.
//
// So the apply is given the units to SKIP (ApplyOpts.SkipUnits) and drops their
// objects from what it SENDS. Three things deliberately do not follow that
// filter, and each is a bug if it does:
//
//   - PRUNING reads the full stream. Prune, PrunePDBs and PruneCRDs delete what
//     the render no longer produces; computed from the filtered stream they
//     would read every hub-converged Deployment as an orphan and delete it.
//   - THE ROLLOUT WAIT excludes the skipped units' Deployments. forge did not
//     roll them, the hub did, and a deploy that blocks on (or fails because of)
//     a rollout it does not own would report the hub's trouble as its own.
//   - A helm chart belongs to no unit and is always forge's.

import (
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// docUnit is the delivery unit a parsed document belongs to: its
// forge.dev/delivery-unit label, or the default unit when it carries none.
func docUnit(m parsedDoc) string {
	if u := strings.TrimSpace(m.Metadata.Labels[release.LabelDeliveryUnit]); u != "" {
		return u
	}
	return release.DefaultDeliveryUnit
}

// DropDeliveryUnits removes from a `---`-separated stream every document that
// belongs to one of units, and returns the rest byte-identical. A document that
// does not parse is kept: nothing can prove it belongs to a skipped unit, and
// dropping what cannot be read would silently ship less than the render holds.
// An empty units is a no-op.
func DropDeliveryUnits(manifests string, units []string) string {
	if len(units) == 0 {
		return manifests
	}
	skip := unitSet(units)
	var kept []string
	for _, doc := range splitDocs(manifests) {
		if m, ok := parseDoc(doc); ok && skip[docUnit(m)] {
			continue
		}
		kept = append(kept, doc)
	}
	return strings.Join(kept, docDelimiter)
}

// DeploymentsInUnits names the Deployments of the stream that belong to one of
// units, sorted. The rollout wait excludes them: they are the hub's to roll.
func DeploymentsInUnits(manifests string, units []string) []string {
	if len(units) == 0 {
		return nil
	}
	skip := unitSet(units)
	seen := map[string]bool{}
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind != "Deployment" || !skip[docUnit(m)] {
			continue
		}
		seen[m.Metadata.Name] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func unitSet(units []string) map[string]bool {
	set := make(map[string]bool, len(units))
	for _, u := range units {
		set[u] = true
	}
	return set
}
