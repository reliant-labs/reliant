// Package changes is the deterministic evaluation core of Forge Changes:
// remote config, gradual rollouts and A/B tests as one model.
//
// # One object: a Change
//
// A change has a default behavior, one or more named variants, and an answer
// to "who gets it?", its delivery mode:
//
//   - off: the declared default for every caller, previews included. Off is
//     the kill switch.
//   - preview: the default for everyone but the units on the change's
//     preview list, who see their listed variant. A preview list also
//     applies under the three modes below, so a team keeps seeing its
//     variant; a previewed unit is never measured.
//   - everyone: one variant for every caller. This is remote config.
//   - rollout: one variant for a stable percentage of an audience. This is a
//     gradual rollout.
//   - experiment: a random, stable split of that percentage across variants.
//     This is an A/B test.
//
// The same change moves from preview to experiment to everyone without being
// recreated, and without the app changing a call site.
//
// # Sticky cohorts from a hash, not a table
//
// Evaluate decides from a versioned policy snapshot and the caller's context
// alone: no network call, no stored assignment. The unit that stays together
// (a user, an account, or an anonymous visitor) is hashed with MurmurHash3
// x86_32 into a bucket 0..9999, and percentages are basis points. Enrollment
// and variant are hashed under different domain labels, so the two are
// independent: raising a rollout from 5% to 25% only adds units, lowering it
// and raising it again returns the same units, and the variant a unit gets
// does not depend on the percentage. A new measured run carries a new salt,
// which re-randomizes both.
//
// A caller with no usable unit id gets the default behavior and is reported
// as not enrolled (ReasonNoUnit), never as a control. So are previews,
// everyone-mode results and callers outside the audience: only
// ReasonRollout and ReasonExperiment are enrolled, and only enrolled results
// belong in an experiment's denominator.
//
// # Two layers
//
// The core is provider-agnostic and pure: parse a snapshot (ParseSnapshot),
// decide one change (Evaluate), and hash a preview unit (PreviewHash). It
// does no I/O, records no telemetry and holds no globals.
//
// The Store is what an app uses. OpenFile reads the snapshot file the
// platform maintains (its path is in EnvFile), keeps the last good snapshot
// current by polling, and records each useful evaluation as the product
// event flag_evaluated, once per unit, change, run and variant per day:
//
//	store := changes.OpenFile(ctx, os.Getenv(changes.EnvFile), changes.Options{})
//	defer store.Close()
//	ec := changes.EvalContext{TargetingKey: userID}
//	if changes.Bool(store.Evaluate(ctx, "new-checkout", ec), false) { … }
//
// Bool, String, Float64 and Decode read a result with the default written in
// code. An app that evaluates through OpenFeature registers the provider in
// the openfeature subpackage instead, which wraps a Store.
//
// The web runtime's changes module (@reliantlabs/forge-web-runtime,
// src/changes.ts) is this package's twin. Both read the same golden vectors
// (testdata/vectors.json), so a browser and a backend give one unit the same
// answer for the same snapshot.
//
// # Stability
//
// The snapshot schema is "forge.changes/v1", and the hash, the domain
// strings and the order of evaluation are part of it: changing any of them
// would move units between cohorts mid-run. They change only under a new
// schema version. ParseSnapshot rejects a snapshot of an unknown schema, so
// a caller keeps its last good one.
package changes
