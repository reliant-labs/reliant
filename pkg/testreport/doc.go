// Package testreport is forge's test record vocabulary: what a test case, a
// lane run, a coverage profile and a flaky test ARE, how each input format is
// read into them, and the one merge and scoring function both backends use.
//
// # One model, two backends
//
// forge records test runs on the machine that ran them (.forge/tests/) and,
// when a project records to a control plane, uploads them there. Both read
// and write these types, and both compute their numbers with the functions in
// this package, so "62% in the CLI, 61% in the console" cannot happen: there
// is one definition of a covered statement, one merge and one flake state
// machine. That is why the package is public and held to v1.
//
// # What is here
//
//   - Identity: [TestID] (`go:<importpath>:<Test>/<sub>`), [Lane], [Status].
//   - Results: [ParseResults] reads `go test -json`, JUnit XML and the Vitest
//     JSON reporter into [Results], the forge.test.results/v1 document.
//   - Coverage: [ParseCoverage] reads Go text profiles (`-coverprofile`, and
//     `go tool covdata textfmt` output), LCOV, Cobertura and istanbul JSON into
//     [Coverage], the forge.test.coverage/v1 document. [Merge] unions them by
//     statement block and refuses inputs that cannot describe the same source.
//     [ConvertCovdata] is the one function here that runs a program (`go tool
//     covdata textfmt`); nothing else does I/O beyond the reader it is handed.
//   - Views: [Coverage.Summary], [Coverage.Rollup], [Coverage.Diff] and
//     [Delta], the numbers every page and every --json document shows.
//   - Flakes: [Classify], [MatchHost], [GroupCorrelated], [DetectFlakeEvents]
//     and [FlakeState], with [WilsonLowerBound] as the displayed flake rate.
//   - Documents: [Results], [Coverage], [Report] and [Flakes], each carrying
//     a [Head] (`schema`, `ok`, `exit_code`, `error`).
//
// # Untrusted input
//
// The control plane parses uploads from projects it does not control, so
// every parser is bounded by [Limits]: bytes read, line length, cases, blocks
// and retained failure output. A parser that reaches a limit returns a
// [*LimitError] rather than allocating without bound.
//
//forge:exclude-contract: a pure-computation library of value types and functions over the input it is handed, with nothing to substitute
package testreport
