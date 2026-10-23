// Package removalguard is the repository-wide proof that a removed feature is
// gone from EVERY surface forge ships — not just the Go tree.
//
// forge's surfaces are Go, internal/templates/** (Go + TS + tmpl),
// internal/templates/project/skills/** (markdown that ships into downstream
// projects), docs/**, kcl/**, proto/** and dotfiles. Three feature removals in
// a row were declared "done" after grepping only the Go tree, and each time a
// non-Go surface survived — including a skill that shipped downstream and
// taught agents an API that does not compile.
//
// The guard itself is the test in removalguard_test.go. This file exists so the
// directory is a buildable package; there is no runtime code here on purpose —
// the guard must never become something production code can call, tune, or
// disable.
package removalguard
