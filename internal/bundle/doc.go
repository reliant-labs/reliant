// Package bundle produces and consumes the artifact that answers "exactly
// what bytes, from exactly which source" for one environment.
//
// A bundle is one env's render, from one checkout, pinned to one release,
// packaged as an immutable OCI artifact (doc §4). It is what "what was
// shipped" means: `forge env build` writes one, the deploy path applies FROM
// it rather than from a second render, and a ledger records its digest. That
// is the change that ends the class of bugs where `env render` and
// `env deploy` rendered subtly differently — "what was applied" and "what was
// recorded" become the same bytes by construction.
//
// # The two halves
//
// PRODUCTION. [Build] takes one env's render and returns the artifact:
// a deterministic manifest layer, the [release.BundleDoc] config blob, the
// config digest, and the OCI manifest binding them. [Push] writes it to a
// registry; [LocalLayout] writes it to an OCI image layout on disk, which is
// where a local env's bundle goes because it is not shipped.
//
// CONSUMPTION. [Fetch] pulls one back, verifying every byte against the
// digest that named it. [Unpack] extracts a layer under the hostile-input
// discipline in unpack.go. [Cache] keys unpacked bundles by digest, which is
// what makes re-applying an unchanged bundle cost one HTTP HEAD.
//
// [ProjectShape] is the projection both halves share: one env's render
// becomes the [release.Shape] a ledger indexes — what runs, where, which
// secrets it needs, which domains it binds, and one hash per object.
//
// # Two invariants, both structural
//
// THE SHAPE AND THE MANIFESTS CANNOT DISAGREE. Both come from one parse of
// one stream (parse.go). A bundle whose shape described objects its layer did
// not carry would be a record of a deploy that never happened, and nothing
// could detect it — both halves are sealed under the same digest, so they
// would be consistently wrong.
//
// NO SECRET VALUE REACHES A BUNDLE (F-13). parseStream redacts on the way
// through, so no unredacted document is available to be packaged at all, and
// [Build] re-checks the final bytes before returning. Bundles are pushed,
// cached on every machine that applies them, and kept forever; a value in one
// is a permanent leak.
//
// # The dependency direction
//
// ONE WAY: internal/cli → internal/bundle, never back. This package knows
// nothing about KCL, cobra, a project on disk, or a control plane. It takes a
// rendered manifest stream plus the few facts the stream does not carry, and
// returns a value. That is what makes it testable from a literal fixture, and
// it is what stops "which cluster does this document land on" from being
// answered twice: the routing stays in internal/cluster, and this package
// reads the answer off the stream.
//
// It also does not import the machine ledger. [NewLocalLayout] takes its
// root as an argument, so where bundles live on disk stays one decision made
// in one place — in internal/ledgerfile, which depends on this package rather
// than the other way round.
package bundle
