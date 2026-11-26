package accesstoken

import "slices"

// Federation says whether, and when, a scope may be minted for a CI
// IDENTITY: a workload that proves who it is with a token its CI provider
// signed (a GitHub Actions OIDC ID token), instead of presenting a secret an
// org stored in the CI. A control plane that exchanges such an identity for a
// short-lived token mints only scopes Federatable allows for it.
//
// ── WHY THE CATALOG SAYS IT, NOT THE EXCHANGE ─────────────────────────
//
// A federated token is held by whoever can run a workflow in the repository
// on the refs the exchange admits — a wider and less reviewed set of holders
// than the people an org hands a CI secret to. So the scopes it may carry are
// a property of each scope, decided once in its catalog row and reviewed in
// forge's PR, like its class and bundles. A consumer derives its exchange's
// list from FederatableScopes and keeps none of its own, so a scope added
// later is never federatable by omission: the zero value is FederationNone.
//
// The classes:
//
//   - FederationNone: never minted for a CI identity. Every scope that
//     promotes or deploys (deploy:write), issues credentials (token:*), acts
//     as a person, is registry-only, or is sensitive stays here; the tests
//     hold that.
//   - FederationAnyRef: minted for any admitted run, whatever its ref. For
//     writes that are append-only evidence about the run itself (tests:write:
//     a test run is recorded against its commit and replaces nothing).
//   - FederationProtectedRef: minted only when the identity's ref is
//     protected (GitHub's ref_protected claim). For writes that are
//     PERMANENT — the first one wins and is never replaced, such as a
//     release's SBOM — so a run on an unprotected branch, which anyone with
//     push access controls, cannot write first.
//
// A federatable scope is always in BundleCI (the tests hold that), so a
// federated token is a subset of what an org's CI token may already hold.
type Federation uint8

const (
	// FederationNone is never minted for a CI identity (the default).
	FederationNone Federation = iota
	// FederationAnyRef is minted for a CI identity on any admitted ref.
	FederationAnyRef
	// FederationProtectedRef is minted for a CI identity only when its ref
	// is protected.
	FederationProtectedRef
)

// FederationOf returns scope's federation class: FederationNone for every
// scope whose catalog row names none, and for a string this binary does not
// know.
func FederationOf(scope Scope) Federation {
	e, _ := entryOf(scope)
	return e.federation
}

// Federatable reports whether a CI identity whose ref is (refProtected) or is
// not protected may be minted scope: always for FederationAnyRef, only on a
// protected ref for FederationProtectedRef, never otherwise.
//
// It also refuses a scope outside BundleCI, whatever its row says, so a
// mistaken row widens nothing: a federated token never exceeds an org CI
// token (which already excludes sensitive, personal, acting-user and
// registry-only scopes).
func Federatable(scope Scope, refProtected bool) bool {
	e, ok := entryOf(scope)
	if !ok || e.class == ClassSensitive || !slices.Contains(e.bundles, BundleCI) {
		return false
	}
	switch e.federation {
	case FederationAnyRef:
		return true
	case FederationProtectedRef:
		return refProtected
	default:
		return false
	}
}

// FederatableScopes returns every scope Federatable allows for a CI identity
// whose ref is (refProtected) or is not protected, in AllScopes order: the
// list an exchange offers that identity.
func FederatableScopes(refProtected bool) []Scope {
	var out []Scope
	for _, e := range catalog {
		if Federatable(e.scope, refProtected) {
			out = append(out, e.scope)
		}
	}
	return out
}
