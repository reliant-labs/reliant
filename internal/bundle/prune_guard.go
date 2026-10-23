package bundle

// The PRUNE GUARD: stamping `kustomize.toolkit.fluxcd.io/prune: disabled` onto
// the objects a reconciler must never delete.
//
// WHY A RECONCILER NEEDS A GUARD AT ALL. A Kustomization with `prune: true` is
// what makes the bundle authoritative: an object the path no longer carries is
// deleted, so a removed workload actually goes away instead of outliving the
// render that declared it. That is the behaviour we want for a Deployment and
// catastrophic for a PersistentVolumeClaim — and the trigger is not a human
// deciding to delete anything. It is a RENDER THAT MERELY FAILED TO PRODUCE
// the object: a `--target` that selected a subset, a KCL conditional that went
// false, a workload renamed in one place and not another. The bundle comes out
// one document short, the Kustomization notices, and the data is gone.
//
// WHY AT BUNDLE WRITE TIME, AND NOT ON THE POINTER. The annotation lives on
// the OBJECT, inside the bundle, rather than being configured on the
// Kustomization forge writes. Three things follow from that, and the third is
// the reason:
//
//  1. every reconciler that ever applies these bytes honours it — including
//     one forge did not write the pointer for, and a `kubectl apply` of the
//     unpacked layer by hand;
//  2. it is sealed under the bundle's digest, so "was this release protected"
//     is answerable from the artifact rather than from the cluster's current
//     configuration;
//  3. it cannot be forgotten by a writer. A guard configured per-pointer is
//     one every future pointer-writing path has to remember, and the failure
//     mode of forgetting is silent until the day a render drops a PVC.
//
// WHICH OBJECTS. The union of two rules, deliberately:
//
//   - [release.StatefulKind] — the well-known kinds whose removal destroys
//     data or everything under them (PVC, PV, StatefulSet, Secret, Namespace,
//     CRD). Recognised by KIND, so no declaration is needed and a project
//     cannot fail to opt in.
//   - `ShapeObject.Stateful` — an object whose KIND does not say so but which
//     holds data anyway, in practice a declared ManagedDatabase's objects (a
//     `postgresql.cnpg.io/v1 Cluster` is just a CR to Kubernetes). This
//     arrives as the workload names in [ShapeInput.StatefulWorkloads].
//
// It is the same union the PLAN uses to raise a stop-class finding on a
// removal, and [release.StatefulKind] exists so the two cannot drift. An
// object the plan calls irreplaceable and the reconciler prunes anyway would
// be a divergence discovered only by losing the data.
//
// STAMPED BEFORE THE HASH, which is what keeps the shape honest. The shape's
// per-object hash is over the document as PACKAGED, so stamping after the
// projection would make every object's recorded hash describe bytes the layer
// does not carry — and drift detection compares the live object against that
// hash, so every guarded object would read as permanently drifted. The stamp
// therefore happens in the parse pass, on `parsedDoc.body`, before
// [shapeObjects] hashes it and before [packManifests] serializes it.
//
// WHAT IT DOES NOT DO: it does not stop forge, a human, or `kubectl delete`
// from removing the object. It removes one actor's ability to do it as a side
// effect of a render — which is the actor that would do it without anybody
// deciding to.

import (
	"github.com/reliant-labs/forge/pkg/release"
)

// Flux's prune-exemption annotation, and its value.
//
// Spelled here rather than imported from internal/flux because the dependency
// would point the wrong way: internal/flux consumes the bundle's layout (it
// reads [release.BundleManifestsLayer] and [release.BundleClusterPath]), so a
// bundle that imported it back would make the two mutually dependent. The
// string is Flux's, not forge's — it is a published annotation, stable across
// Flux's v1 API — and internal/flux's own copy carries the cross-reference.
const (
	annotationPrune  = "kustomize.toolkit.fluxcd.io/prune"
	pruneDisabled    = "disabled"
	metadataKey      = "metadata"
	annotationsKey   = "annotations"
	kindKey          = "kind"
	workloadLabelKey = "forge.dev/workload"
)

// guardStatefulObjects stamps the prune exemption onto every document that
// holds data, in place, and reports how many it stamped.
//
// The count is returned for the build's own report: "N objects guarded" is
// the one line that makes the guard visible to whoever cut the release, and a
// silent guard is one nobody notices has stopped working.
func guardStatefulObjects(docs []parsedDoc, statefulWorkloads []string) int {
	stateful := map[string]bool{}
	for _, name := range statefulWorkloads {
		stateful[name] = true
	}
	stamped := 0
	for i := range docs {
		if !holdsData(docs[i], stateful) {
			continue
		}
		if setAnnotation(docs[i].body, annotationPrune, pruneDisabled) {
			stamped++
		}
	}
	return stamped
}

// holdsData is the union rule: a well-known stateful kind, or an object
// belonging to a workload the env declared stateful.
func holdsData(doc parsedDoc, statefulWorkloads map[string]bool) bool {
	// A PDB holds no data, even inside a stateful workload: guarding it would
	// keep a stale one (a workload that dropped to one replica) alive forever.
	if doc.meta.kind == "PodDisruptionBudget" {
		return false
	}
	return release.StatefulKind(doc.meta.kind) || statefulWorkloads[doc.meta.workload]
}

// setAnnotation writes one annotation onto a decoded document, creating
// `metadata` and `metadata.annotations` as needed. It reports whether it wrote.
//
// It REFUSES TO OVERWRITE an existing value of the same key. A document that
// already carries `prune: disabled` is already guarded and stamping it again
// is a no-op; one carrying some other value said something deliberate, and
// this is not the place to overrule a declaration. Returning false in that
// case keeps the count honest — it reports objects this pass guarded, not
// objects that are guarded.
//
// A body that is not a mapping is skipped rather than refused. parseStream has
// already established that every document has a kind and a name, so a
// non-mapping body cannot occur here; if the parse ever changes, dropping an
// annotation is a better failure than panicking on a type assertion inside
// the build.
func setAnnotation(body any, key, value string) bool {
	root, ok := body.(map[string]any)
	if !ok {
		return false
	}
	meta, ok := root[metadataKey].(map[string]any)
	if !ok {
		meta = map[string]any{}
		root[metadataKey] = meta
	}
	annotations, ok := meta[annotationsKey].(map[string]any)
	if !ok {
		annotations = map[string]any{}
		meta[annotationsKey] = annotations
	}
	if existing, present := annotations[key]; present {
		if s, isString := existing.(string); isString && s == value {
			return false
		}
		return false
	}
	annotations[key] = value
	return true
}
