// Package deadcodeguard is the repository-wide proof that forge's own code
// cannot make a claim its own data refutes.
//
// It exists because of a specific, repeated failure. In one night of hand
// auditing, three separate defects turned out to share a single shape:
// something declared a capability, nothing ever supplied the input that
// capability needed, and no test noticed because the tests supplied the input
// by hand.
//
//   - lookupServicePort/matchServicePort was an elaborate port heuristic in
//     which EVERY branch was unreachable. It read a per-component port map the
//     discovery path never set, so the accessor returned 0 and both callers hit
//     their fallback 100% of the time. (Deleting the heuristic without the field
//     it read is how this rule found the same defect a second time, in five more
//     consumers; the field is gone now, and a port is a KCL deploy fact.)
//   - AppendServiceToConfig was `return nil`, kept "as part of the contract".
//     Its caller invented a port number, PRINTED it to the user as fact, and
//     handed it to the no-op.
//   - TestMatchServicePort hand-built that port map in every case — the one
//     data shape production can never produce — so it was green forever while
//     proving nothing.
//
// Each was found by a human reading code. That does not scale. The two rules
// here are the mechanical versions of that reading:
//
//	phantom-field  A struct field that production code READS but production
//	               code never WRITES. Every read can only ever observe the
//	               zero value, so any branch, format string or heuristic
//	               downstream of it is decoration. When the only writers are
//	               tests, the tests are additionally proving behavior on a
//	               data shape that cannot occur — the false green above.
//
//	noop-func      A plain function whose entire body returns zero values and
//	               which nonetheless declares parameters. The parameters are a
//	               lie: callers compute arguments that go nowhere, and — as
//	               AppendServiceToConfig showed — sometimes report them to the
//	               user as if they had an effect.
//
// SCOPE, and why it is drawn here.
//
// Both rules judge only types and functions declared under
// github.com/reliant-labs/forge/internal/... . An internal package cannot be
// imported from outside the module, so the loaded program is the COMPLETE set
// of writers — the analysis is sound. forge/pkg is deliberately excluded: it
// is a published library whose fields are legitimately written only by
// downstream generated code that this scan cannot see, so the same rule there
// would be unsound in exactly the direction that produces false positives.
//
// The analysis is a package library rather than a bare test so its rules can
// be unit-tested against planted defects in testdata/. Nothing in forge's
// production path imports it, and nothing should: a guard that production can
// call is a guard production can tune off.
package deadcodeguard
