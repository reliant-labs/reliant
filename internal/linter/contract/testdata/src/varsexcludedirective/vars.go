// Package varsexcludedirective has exported package vars that the
// exported-vars rule would normally flag, but it carries the per-package
// //forge:exclude-contract header — the local-source equivalent of a
// forge.yaml contracts.exclude entry — so the analyzer must skip it
// entirely. Zero findings are expected (no expectation comments below).
//
//forge:exclude-contract
package varsexcludedirective

// Would normally be flagged, but the package is opted out via header.
var GlobalConfig = "ok-because-excluded"
var DefaultTimeout = 30
var MaxRetries = 3
