package cluster

import (
	"fmt"
	"strings"
)

// Apply-completeness verification.
//
// `kubectl apply --server-side` can REJECT an individual object and still
// exit 0 for the batch. A field-manager conflict is the usual way in: any
// `kubectl set image` / `kubectl edit` ever run against the cluster leaves
// a foreign manager owning that field, and from then on the apply of that
// ONE object is refused while every other object in the same stream lands
// normally. kubectl skips it, carries on, and reports success.
//
// That is not hypothetical. The control-plane v1.5.6 prod deploy rendered
// 105 objects, applied 103, and reported success — the three that never
// reached the cluster were the reliant-api-server, daemon-gateway and
// reliant-temporal-worker Deployments, which sat on a stale image for
// hours with nothing red anywhere to say so. The rollout wait did not
// catch it either: an object that was never applied has no new ReplicaSet
// to roll out, so `rollout status` reports the OLD generation as perfectly
// ready.
//
// The exit status cannot see this and neither can the error text — the
// only evidence is an object that has no line in the apply output at all.
// So we count: every rendered object must be confirmed by the apply, and a
// deploy that cannot show that is INCOMPLETE, not successful.

// confirmedApplyVerbs are the outcomes that prove an object actually
// reached the cluster. kubectl prints exactly one line per object it acted
// on, `<resource>.<group>/<name> <verb>`, and these four are the verbs a
// server-side apply produces for a successful write. Anything else on a
// line — a warning, an error, a progress note — is not a confirmation.
var confirmedApplyVerbs = map[string]struct{}{
	"serverside-applied": {},
	"configured":         {},
	"created":            {},
	"unchanged":          {},
}

// maxMissingObjectsReported caps how many missing objects the error names.
// A render that lost eighty objects lost them for one reason, and printing
// all eighty buries it; the shell gate this replaces capped at the same
// number for the same reason.
const maxMissingObjectsReported = 20

// objectRef identifies one object by the two things that appear in BOTH
// the rendered manifest and kubectl's apply output: its kind (lowercased)
// and its name.
//
// Kind AND name together, never name alone. A Deployment shares its name
// with the Service, ServiceAccount and Role rendered beside it — all of
// which apply fine — so a name-only comparison finds every name present
// and reports nothing missing, which is exactly the silence this check
// exists to break.
type objectRef struct {
	Kind string
	Name string
}

func (o objectRef) String() string { return o.Kind + "/" + o.Name }

// IncompleteApplyError says the apply exited 0 while fewer objects reached
// the cluster than were rendered, and names the ones that did not.
//
// It is a distinct type because the deploy pipeline treats it as the same
// CLASS of failure as a rollout that never became ready: fatal under the
// default RolloutWait policy, a warning under RolloutWarn. See
// RolloutPolicy.noteApplyFailure.
type IncompleteApplyError struct {
	// Rendered is how many identifiable objects the manifest stream held.
	Rendered int
	// Confirmed is how many of those the apply reported acting on.
	Confirmed int
	// Missing names the objects with no line in the apply output, as
	// `kind/name`, in render order, capped at maxMissingObjectsReported.
	Missing []string
	// Truncated is how many missing objects were elided from Missing.
	Truncated int
}

func (e *IncompleteApplyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "apply confirmed %d of %d rendered object(s) — %d never reached the cluster; "+
		"the deploy is INCOMPLETE and must not be treated as success. "+
		"The usual cause is a server-side-apply field-manager conflict, which kubectl skips "+
		"for that object while exiting 0 for the batch. Objects with no line in the apply output:",
		e.Confirmed, e.Rendered, e.Rendered-e.Confirmed)
	for _, name := range e.Missing {
		b.WriteString("\n  - ")
		b.WriteString(name)
	}
	if e.Truncated > 0 {
		fmt.Fprintf(&b, "\n  ... and %d more", e.Truncated)
	}
	return b.String()
}

// renderedObjectRefs returns the identity of every object in a
// `---`-separated manifest stream.
//
// Documents that carry no kind or no name are SKIPPED rather than counted:
// a trailing `---`, a comment-only chunk, or a doc that doesn't parse is
// not something kubectl will print a line for, so counting it would make
// the gate fire on a render that is perfectly fine. The cost of skipping
// is that an unnameable object goes unverified — acceptable, because forge
// renders every object from KCL with an explicit name.
func renderedObjectRefs(manifests string) []objectRef {
	docs := splitDocs(manifests)
	refs := make([]objectRef, 0, len(docs))
	for _, doc := range docs {
		m, ok := parseDoc(doc)
		if !ok || m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		refs = append(refs, objectRef{
			Kind: strings.ToLower(m.Kind),
			Name: m.Metadata.Name,
		})
	}
	return refs
}

// confirmedObjectRefs returns the objects kubectl reported acting on.
//
// The line shape is `<resource>.<group>/<name> <verb>` — e.g.
// `deployment.apps/admin-api serverside-applied`. The group suffix varies
// by kind and carries no information the comparison needs, so the leading
// token is truncated at the first dot: `deployment.apps` → `deployment`,
// which is what `kind: Deployment` lowercases to. Multi-word kinds survive
// this unchanged (`HTTPRoute` → `httproute` on both sides).
func confirmedObjectRefs(applyOutput string) map[objectRef]struct{} {
	confirmed := map[objectRef]struct{}{}
	for _, line := range strings.Split(applyOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, ok := confirmedApplyVerbs[fields[1]]; !ok {
			continue
		}
		slash := strings.Index(fields[0], "/")
		if slash <= 0 || slash == len(fields[0])-1 {
			continue
		}
		resource, name := fields[0][:slash], fields[0][slash+1:]
		if dot := strings.Index(resource, "."); dot > 0 {
			resource = resource[:dot]
		}
		confirmed[objectRef{Kind: strings.ToLower(resource), Name: name}] = struct{}{}
	}
	return confirmed
}

// verifyApplyComplete proves the apply that just exited 0 actually wrote
// every object the render asked for, and returns an *IncompleteApplyError
// naming the ones it did not.
//
// Compared as a SET, not as two counts. A count comparison answers "how
// many are missing" but not "which", and which is the only part that is
// actionable at 3am; it also mis-reports a stream that renders the same
// object twice, where kubectl legitimately prints one line for two docs.
func verifyApplyComplete(manifests, applyOutput string) error {
	rendered := renderedObjectRefs(manifests)
	if len(rendered) == 0 {
		return nil
	}
	confirmed := confirmedObjectRefs(applyOutput)

	var missing []objectRef
	seen := make(map[objectRef]struct{}, len(rendered))
	for _, ref := range rendered {
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		if _, ok := confirmed[ref]; !ok {
			missing = append(missing, ref)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	err := &IncompleteApplyError{
		Rendered:  len(seen),
		Confirmed: len(seen) - len(missing),
	}
	for i, ref := range missing {
		if i >= maxMissingObjectsReported {
			err.Truncated = len(missing) - maxMissingObjectsReported
			break
		}
		err.Missing = append(err.Missing, ref.String())
	}
	return err
}
