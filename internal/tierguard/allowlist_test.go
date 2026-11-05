package tierguard

// The documented exemption list, and the test that keeps it honest.
//
// It lives in a _test.go file because it is consumed only by the guard.
// Keeping it out of the production build also means an EMPTY list is
// representable without leaving a struct in production code whose fields
// nothing ever writes (which internal/deadcodeguard correctly flags as a
// phantom-field: a shape only tests can produce).

import (
	"strings"
	"testing"
)

// allowEntry documents one path that is byte-constant across the
// fixtures TODAY but is genuinely per-project in principle, so Tier-1
// remains the right tier for it.
//
// A Reason is mandatory and is enforced by
// TestAllowListEntriesAreJustified: this list is a record of decisions,
// not a place to park failures. Each reason must name the user input the
// file WOULD respond to and say why the fixtures do not happen to move
// it.
type allowEntry struct {
	Path   string
	Reason string
}

// allowList is EMPTY BY DESIGN. Nothing has been granted an exemption:
// every path the differential render calls constant is reported as a
// mis-tier candidate, and deciding what to do about each one is
// follow-up work, not something this guard should pre-absolve.
//
// Several paths were considered and deliberately NOT exempted, because
// in each case the honest fix was to make the fixtures exercise the
// input, or to change the file's tier:
//
//   - pkg/middleware/procedures_gen.go looked constant until fixture B
//     declared an RPC with `auth_required: false`. It is now genuinely
//     Derived. An exemption would have hidden a real, testable property.
//   - pkg/config/config_gen.go looked like a mis-tier: constant across
//     A/B, and varying only with the module path, which reads as "embeds
//     the user's import paths but no declaration of theirs". The earlier
//     experiment that moved a COMPONENT config block confirmed the
//     `type Config = configv1.AppConfig` alias text is invariant — but
//     the file has a second half nothing was exercising. Fixture B now
//     scaffolds a second binary and gives it a config message carrying
//     `(forge.v1.binary_config)`, which emits a per-binary alias plus
//     Register/Load/ModeOf/Validate. The file is genuinely Derived; the
//     mis-tier reading was an unexercised input, not a property.
//   - db/source_gen.go and db/embed_gen.go project whether
//     db/migrations/ holds any .sql. A and B both had migrations, so
//     both rendered the same branch. Fixture D declares no entity at
//     all: source_gen.go's body flips to the nil branch and embed_gen.go
//     is not emitted, making it presence-derived.
//   - cmd/<bin>/cmd/services/register_gen.go records a NOTE for a
//     service whose kebab name shadows a built-in. Fixture D declares a
//     service named `version`, and the anchor now moves.
//
// Two paths were found to be genuine mis-tiers and FIXED rather than
// exempted: cmd/<bin>/cmd/{workers,operators}/register_gen.go rendered
// from `struct{}{}` — `forge generate` does no worker/operator discovery,
// so they projected nothing in any project. They are now scaffold-once
// register.go (see codegen.GenerateCmdGroups). That is the outcome this
// guard exists to produce; an allowList entry would have preserved the
// defect.
//
// Add an entry only with a reason naming the specific user input the file
// is a function of and why no fixture moves it — and prefer extending a
// fixture instead, which turns the entry into a real Derived verdict and
// is strictly better evidence than an exemption.
var allowList = []allowEntry{}

// allowed indexes allowList by path.
func allowed() map[string]string {
	out := map[string]string{}
	for _, e := range allowList {
		out[e.Path] = e.Reason
	}
	return out
}

// TestAllowListEntriesAreJustified keeps allowList a record of decisions
// rather than a place to hide failures: every entry needs a reason with
// enough substance to argue with, and an entry for a path the guard no
// longer calls constant is dead weight that must be removed.
func TestAllowListEntriesAreJustified(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range allowList {
		if strings.TrimSpace(e.Path) == "" {
			t.Error("allowList entry with an empty Path")
			continue
		}
		if seen[e.Path] {
			t.Errorf("allowList has duplicate entries for %q", e.Path)
		}
		seen[e.Path] = true

		// A reason short enough to be a shrug is not a reason. Entries
		// must name the user input the file would respond to.
		if len(strings.TrimSpace(e.Reason)) < 40 {
			t.Errorf("allowList entry %q has no substantive reason (%q).\n"+
				"State which user input the file IS a function of, and why no fixture moves it — "+
				"the list is a record of decisions, not a mute suppression list.", e.Path, e.Reason)
		}
	}

	if testing.Short() || len(allowList) == 0 {
		t.Logf("allowList has %d entr(ies); nothing to re-verify against a render", len(allowList))
		return
	}

	// Every exemption must still be load-bearing. A path that now
	// classifies as derived does not need an exemption, and leaving one
	// behind would mask a future regression at that path.
	inputs, identity := renders(t)
	byPath := map[string]Classification{}
	for _, c := range Classify(inputs, identity) {
		byPath[c.Path] = c
	}
	for _, e := range allowList {
		c, ok := byPath[e.Path]
		if !ok {
			t.Errorf("allowList exempts %q, which is not a Tier-1 target in any fixture — "+
				"stale entry, remove it", e.Path)
			continue
		}
		if c.Verdict != Constant {
			t.Errorf("allowList exempts %q but the guard classifies it as %s (%s) — "+
				"the exemption is unnecessary and would mask a real regression there; remove it",
				e.Path, c.Verdict, c.Evidence)
		}
	}
}
