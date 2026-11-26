// Package testreport turns the record of a `go test -json` run into a verdict
// about whether that run actually proved anything.
//
// # THE DISEASE
//
// A Go suite that declines to run its tests exits 0. Measured on the reference
// project (reliant), one package, one environment variable apart:
//
//	internal/threads WITHOUT DATABASE_URL:    9 pass / 124 skip  → exit 0
//	internal/threads WITH    DATABASE_URL:  103 pass /   1 skip  → exit 0
//
// Same exit code, same green check mark, 7% of the tests. Every dashboard
// forge has reads the left column as success. A missing env var, an absent
// service, or a `testing.Short()` guard can evaporate a whole package's
// coverage and nothing anywhere says so — the same shape as a doctor check
// that answers "fine" when it holds no facts.
//
// [vacuousguard] is the STATIC half of this problem: it reads source and
// reports skips that can never fire and loops that can pass over nothing.
// This package is the RUNTIME half: it reads what a run actually did. Neither
// subsumes the other. A skip conditioned on `os.Getenv("DATABASE_URL")` is
// perfectly legitimate source — vacuousguard is right to leave it alone — and
// it is still the thing that erased 94% of a package on the machine that ran it.
//
// # WHY NOT JUST "ANY SKIP IS BAD"
//
// Skips are legitimate. That reliant package keeps one unconditional skip even
// when fully configured; other packages carry documented skips for framework
// limitations; `-short` exists precisely so a suite can decline work. A rule
// that fires on every skip is a rule that gets switched off, and a check
// nobody reads is worse than no check at all — it trains people to skim past
// the real failure sitting in the same output.
//
// So the question this package asks is not "did anything skip". It is:
//
//	Did this package skip so much that its PASS means nothing?
//
// Two rules answer it, both measured against the real distribution (112
// tested packages of the reference project, run in the broken environment):
//
//	zero-evidence  Every test in the package skipped. The package contributed
//	               no evidence at all, so its "ok" is a statement about
//	               nothing. Unambiguous at any size — no sample-size floor.
//
//	mass-skip      The package skipped at least MaxSkipRatio of its tests
//	               (default: more than half) and has at least MinTests tests.
//	               The floor exists because 1-of-2 is noise, not signal.
//
// On that 112-package corpus the pair reports FIVE packages, and the gap in
// the data is wide: the flagged packages sit at 1.00, 0.95, 0.80, 0.60 and
// 0.56 skip ratio, and the next package down is 0.20. The rules are drawing a
// line through a real valley, not through the middle of a crowd.
//
// # AND WHEN A HIGH RATIO IS GENUINELY FINE
//
// A package that is entirely integration tests behind a docker guard skips
// 100% on a laptop, forever, correctly. That is what [Exemption] is for: a
// declaration, committed to forge.yaml, naming the package and REQUIRING a
// written reason — the same shape as `forge project disown <path> --reason`.
// Declared once, reviewed in a diff, silent thereafter.
//
// What this package deliberately does NOT offer is a RECORDED baseline
// ("expected 124 skips here, still 124, fine"). It was considered and
// rejected: the skip count of a database-backed package is a property of the
// MACHINE, not of the code. A baseline recorded on a laptop without
// DATABASE_URL enshrines the broken state as normal — the precise failure
// being fixed — and a baseline recorded in CI is wrong on every laptop. A
// number that is wrong on one of the two machines every time is a number
// people learn to re-record without reading. A human-written exemption with a
// reason survives both machines because it states an intent, not a count.
//
// # THREE STATES, NOT TWO
//
// [Analyze] can return UNDETERMINED, and undetermined is not a pass. Output
// that carries no `go test -json` events, or a stream that ends mid-run so a
// package's totals are partial, means forge could not obtain the facts. It
// says so — it does not report a clean bill of health over a file it failed
// to read. This mirrors `forge env status`, which prints "N UNDETERMINED (not
// a pass — forge could not obtain the facts)" for exactly this reason.
//
// # WHAT IT COSTS
//
// Nothing re-runs. The input is the JSON the suite already emitted, read from
// a pipe or a file, so the signal costs one extra `-json` flag on a run that
// was happening anyway.
//
// [vacuousguard]: https://pkg.go.dev/github.com/reliant-labs/forge/internal/vacuousguard
package testreport
