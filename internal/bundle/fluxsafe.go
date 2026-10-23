package bundle

// Making a bundle's objects safe for a reconciler. The rules and the incidents
// behind them are in pkg/release/fluxsafe.go; this applies them to a parsed
// stream.

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// makeFluxSafe fixes what can be fixed without changing an object's meaning and
// refuses what cannot, in place on the parsed (and already redacted) bodies.
//
// REFUSED, not fixed, and only when refuse is set (the bundle is one a
// reconciler may apply) or the object is in one of refuseUnits (a unit the hub
// converges while forge still applies the rest): a Job with ttlSecondsAfterFinished, and a Job whose
// name carries no spec hash. Under a reconciler the first re-runs the Job every
// TTL period and the second wedges every reconcile after its first spec change;
// neither is something forge can repair by guessing the author's intent.
func makeFluxSafe(docs []parsedDoc, refuse bool, refuseUnits []string) error {
	narrow := map[string]bool{}
	for _, u := range refuseUnits {
		narrow[u] = true
	}
	var bodies []any
	for i := range docs {
		body, ok := docs[i].body.(map[string]any)
		if !ok {
			continue
		}
		release.SetExplicitRollout(body)
		if release.ForceRecreateKind(docs[i].meta.kind) {
			setAnnotation(body, release.AnnotationFluxForce, release.FluxForceEnabled)
		}
		// Which objects the refusal judges: all of them when the whole bundle is
		// reconciled, only the hub's units when just some have moved.
		if refuse || narrow[docUnit(docs[i])] {
			bodies = append(bodies, body)
		}
	}
	if !refuse && len(narrow) == 0 {
		return nil
	}
	findings := release.CheckFluxSafety(bodies)
	if len(findings) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d object(s) are not safe under a reconciler (Flux re-applies a bundle on every interval and prunes what it no longer carries):", len(findings))
	for _, f := range findings {
		b.WriteString("\n  " + f.String())
	}
	return fmt.Errorf("%w: %s", release.ErrInvalid, b.String())
}
