package cli

import "fmt"

// A release cut BEFORE forge #322 is not verifiable, and this file is the
// decision about what forge does with that fact.
//
// WHAT CHANGED. #322 made a workload's image carry its registry, and re-keyed
// the OCI release ledger by REPOSITORY — registry host included
// (`ghcr.io/acme/app`) — where it used to be keyed by the bare image name
// (`app`), because the registry lived on the environment. The key IS the
// address a verify dials, so an entry keyed `app` names no host and cannot be
// resolved to a registry at all. Every OCI artifact in a pre-#322 ledger is
// therefore unverifiable, which is what happened to hounders' v0.1.1.
//
// THE OPTION NOT TAKEN: rewriting the keys. The migration COULD prefix each
// bare key with the registry the env used to declare, and it must not, for a
// reason that is not conservatism:
//
// A ledger entry is a CLAIM THAT SPECIFIC BYTES EXIST AT A SPECIFIC ADDRESS.
// Rewriting `app` to `ghcr.io/acme/app` does not make that claim true — it
// asserts, without checking, that the digest recorded back then is present in
// that repository now. Nothing in the migration can know it. The old release
// was pushed under whatever the env's registry was AT THE TIME, which may have
// been a local k3d registry that no longer exists, a registry since rotated, or
// a different host per env — and #322's own rationale is that one release can
// legitimately span several registries, which is exactly why the key had to
// carry the host.
//
// So a rewritten key produces a ledger that VERIFIES GREEN while asserting
// something nobody checked. That is strictly worse than one that fails, because
// the entire purpose of the ledger is to be the thing you can trust when you
// are deciding whether the bytes in prod are the bytes that passed staging. A
// verification system that can be made to pass by a text substitution has no
// value. An honest "this cannot be checked, re-cut it" costs one build; a
// false green costs the guarantee.
//
// THE OPTION TAKEN. forge says so, in the place the user meets the problem.
// `forge release verify` already reports an unresolvable key as UNVERIFIABLE —
// correct, but with a message that describes the symptom ("names no registry
// host") and leaves the user to work out why a release that was fine last month
// stopped being checkable. It now names the cause and the one action that
// resolves it.
//
// The STATUS is deliberately unchanged. Unverifiable is the honest verdict: the
// release is not proven wrong, it is unproveable, and `--strict` already turns
// that into a failure for anyone who needs it to. Promoting it to FAILED would
// claim the artifact is absent or mismatched, which forge has not established.

// bareRepositoryDetail explains an OCI ledger key that names no registry host.
//
// There are two ways to get one, and they need the same action but different
// words: a release cut before #322 (the common case now, and a migration
// artifact), or a build that pushed nowhere. Both are identified by the same
// evidence — a bare key — so the message covers the likely cause first and
// names the other rather than guessing between them.
func bareRepositoryDetail(name, digest string) string {
	return fmt.Sprintf(
		"%s is recorded under %q, which names no registry host, so there is no address to check it at.\n"+
			"      A release cut before forge v0.1.20 keyed OCI artifacts by bare image name, because the\n"+
			"      registry was declared on the ENVIRONMENT. It is part of the image now, so the ledger key is\n"+
			"      the full repository (e.g. \"ghcr.io/<owner>/%s\") — which is the address, so a verify can dial it.\n"+
			"      forge does NOT rewrite the old key: prefixing a registry onto it would assert, unchecked, that\n"+
			"      this digest is in that repository, and a ledger that verifies green on an unchecked claim is\n"+
			"      worth less than one that admits it cannot check.\n"+
			"      Re-cut this release against the current declarations: forge env build <env> --release <version> --push\n"+
			"      (If instead this artifact was never pushed anywhere — a local or compose build — there is\n"+
			"      nothing to verify and nothing to fix.)",
		digest, name, name)
}
