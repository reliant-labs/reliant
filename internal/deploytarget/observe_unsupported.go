package deploytarget

import "context"

// The provider that declines to observe, and why.
//
// The gap here is STRUCTURAL and permanent: Firebase owns its own release
// history and forge keeps no artifact of its own there, so forge holds no
// identity of its own on the other side.
//
// There is deliberately no third category. Compose and HostInfra were
// once listed here as NOT YET BUILT, and both are now implemented
// (observe_compose.go, observe_hostinfra.go) — which is what that label
// was for. A gap mislabeled as temporary is the thing to avoid; a gap
// that stays temporary forever is the same defect wearing a nicer word.
//
// Declining explicitly, rather than leaving these out of the Provider
// interface, is the whole point of the rule. A provider that cannot
// observe must REPORT UNKNOWN, never green, and never silently vanish
// from a report: a reconciler that quietly skipped the tiers forge had
// not got to yet would show a clean board for an environment it had
// measured half of. TestEveryRegisteredProviderObservesOrDeclares
// enforces exactly that, for every provider in the registry.

// Observe is not supported for Firebase Hosting targets.
//
// Structural: forge ships the assembled tree to
// Firebase and Firebase owns the release history afterwards. forge keeps
// no artifact of its own (contrast StaticSite, whose per-digest release
// prefix is exactly what makes both promotion and observation real), so
// there is no forge-side identity to compare a live site against.
func (p FirebaseProvider) Observe(_ context.Context, group ServiceGroup) (Observed, error) {
	return unsupported(p.Name(),
		"Firebase owns the release history and forge keeps no artifact of its own for this target, "+
			"so there is no forge-side identity to compare the live site against",
		observableNames(group))
}
