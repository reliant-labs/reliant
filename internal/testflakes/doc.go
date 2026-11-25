// Package testflakes is the local half of forge's flaky-test handling: the
// quarantine list (ci/quarantine/), the flake gate that runs the tests a
// change adds or edits three times, the confirm pass that reruns a red run's
// failures once, and the flake states computed from the local test record
// (.forge/tests/runs/).
//
// It ports scripts the three Reliant repos ran on their own, so each part
// names the script it replaces:
//
//   - quarantine.go: control-plane's scripts/lib/quarantine.sh and the
//     ci/quarantine/<lang>/<package dir>/<Test>.yaml layout it reads
//     (docs/flake-quarantine.md). The file layout is the contract: a shell
//     reader of the old format reads what this writes.
//   - changed.go, gate.go: forge's scripts/ci-changed-tests.sh and reliant's
//     scripts/test-flake.sh, with control-plane's -cpu=1,2,4 variant.
//   - logs.go, confirm.go: scripts/flake-report.sh (control-plane's, the
//     superset of the three), with amendment 7's host verdict and correlated
//     grouping in place of its `broad` cut-off.
//   - history.go, issues.go: the flake states of pkg/testreport over the local
//     record, and flake-report.sh's `flake:suspected` issue handling.
//
// Classification, host signatures, correlated grouping and the state machine
// are pkg/testreport's; nothing here re-derives them.
package testflakes
