package fluxdelivery

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// The names of the objects the template renders, composed in ONE place.
//
// Moved from control-plane's internal/fluxtenant (BundleObjectName,
// BundleKustomizationName, BundleTreeKustomizationName, boundLabelValue,
// lowerDNS) because self-hosted forge must compute the same names and a name
// composed twice is a name that can disagree: the converger moves one object
// and the observer reads another, which reads as a promotion that never lands.
// The template does not derive them; they arrive as inputs.
//
// # The separator is a DOT, and that is the collision proof
//
// The hub namespace also holds per-deployment config-artifact objects named
// `<org>-<deployment>`, both components RFC-1123 LABELS — which admit no dot.
// Every name here carries at least two, so the images are disjoint by grammar
// (control-plane's TestBundleNamesNeverCollideWithAConfigArtifactName found the
// collision a `-env-` infix allowed). A dot is legal because an object name is
// an RFC-1123 SUBDOMAIN; [Validate] enforces the subdomain rules.

// Names are functions of (org, env, binding). A BINDING is one ledger
// binding: everything one promotion-row stream decides, and the unit one
// [BindingInput] describes. Today the ledger has exactly one binding per
// environment, spelled "", and every function here then returns the name the
// hub already uses, so adopting a live object is a metadata-only no-op. A
// non-empty binding (refused by [Validate] until the ledger has per-binding
// promotion) inserts `.b.<binding>` after the env segment.

// base is the name every object of a binding starts with.
func base(org, env, binding string) string {
	name := org + ".env." + env
	if binding != "" {
		// `.b.` cannot occur in an env id (a label admits no dot), so
		// `<env>.b.<binding>` never equals another environment's name or
		// another binding's: TestBindingNamesNeverCollide.
		name += ".b." + binding
	}
	return name
}

// SourceName is the name of a binding's OCIRepository: `<org>.env.<env>`
// (control-plane's fluxtenant.BundleObjectName) for the environment's binding.
func SourceName(org, env, binding string) string { return base(org, env, binding) }

// KustomizationName is the name of a FLAT (layout v1) cluster's Kustomization
// (control-plane's fluxtenant.BundleKustomizationName).
//
// The cluster is sanitized through release.BundlePathSegment — the function
// the path uses, so the name and the path agree about what a cluster's
// identity reduces to — then folded to the subdomain alphabet ([LowerDNS])
// and bounded as a label value ([BoundLabelValue]): kustomize-controller stamps
// `kustomize.toolkit.fluxcd.io/name: <this>` on every object it applies, and an
// over-long name fails every apply naming the APPLIED object.
func KustomizationName(org, env, binding, cluster string) string {
	return BoundLabelValue(base(org, env, binding) + "-" + LowerDNS(release.BundlePathSegment(cluster)))
}

// TreeKustomizationName is the name of one layout-v2 tree's Kustomization
// (control-plane's fluxtenant.BundleTreeKustomizationName). The unit sits
// between cluster and phase exactly as it does in the path.
func TreeKustomizationName(org, env, binding, cluster, unit string, phase release.Phase) string {
	return BoundLabelValue(base(org, env, binding) + "-" +
		LowerDNS(release.BundlePathSegment(cluster)) + "-" +
		LowerDNS(release.BundlePathSegment(unit)) + "-" + string(phase))
}

// ResourceSetName is the name of the ResourceSet that renders a binding, in
// the org's hub namespace: `delivery.<env>`, or `delivery.<env>.<binding>`.
// Bounded as a label value because flux-operator stamps it on every object it
// applies (`resourceset.fluxcd.controlplane.io/name`).
func ResourceSetName(env, binding string) string {
	name := "delivery." + env
	if binding != "" {
		name += "." + binding
	}
	return BoundLabelValue(name)
}

// LabelValue is the label value for a cluster or unit name, the way the hub
// labels an applier with its cluster (`reliant.dev/cluster`) and unit.
func LabelValue(s string) string {
	return BoundLabelValue(LowerDNS(release.BundlePathSegment(s)))
}

// maxLabelLen is Kubernetes' bound on a label value and on each dot-separated
// segment of an object name.
const maxLabelLen = 63

// boundHashLen is the hash characters BoundLabelValue keeps: 48 bits, so a
// collision among one org's environments and clusters is not a practical
// concern, while leaving 50 characters of readable prefix.
const boundHashLen = 12

// BoundLabelValue fits a composed name into a label value (63 characters)
// WITHOUT merging distinct inputs.
//
// A name that fits is returned unchanged. A longer one becomes a readable
// prefix, a dot, and the first 12 hex characters of the SHA-256 of the WHOLE
// name. Plain truncation would merge two inputs that differ past the cut into
// one object, and the second would overwrite the first; the hash covers every
// byte. The separator is a dot for the collision proof above, and trimming the
// cut's trailing `-`/`.` keeps the result a legal subdomain and label value.
func BoundLabelValue(name string) string {
	if len(name) <= maxLabelLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "." + hex.EncodeToString(sum[:])[:boundHashLen]
	prefix := strings.TrimRight(name[:maxLabelLen-len(suffix)], "-.")
	return prefix + suffix
}

// LowerDNS folds a path segment into the RFC-1123 subdomain alphabet: lower
// case, and `_` (legal in a path, illegal in a name) to `-`.
//
// A DOT IS LEFT ALONE: folding it would collapse two genuinely different
// contexts (`a.b` and `a-b`) onto one object name, the silent overwrite
// [Validate] refuses. Leaving it means the only collisions are ones the
// refusal catches.
func LowerDNS(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		case r == '_':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
