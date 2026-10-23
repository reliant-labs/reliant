package contract

import (
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Test-file exemption — the principle:
//
// The contract rules judge a package's API, and a _test.go file is not part
// of it: it compiles only into the package's test binary, so nothing that
// imports the package can reach what it declares. A fake that satisfies a
// contract interface is the contract being used, not a second surface beside
// it; an export_test.go method exists only so the package's own external
// tests can reach an internal.
//
// Every driver hands test files to the analyzers anyway — `go vet` and
// multichecker analyze each package's test variant, and `forge lint
// --contract` loads with Tests: true for parity — so each analyzer skips them
// itself, and every driver gets the same answer. Measured in reliant:
// internal/mcpserver/http_test.go's memStore fake satisfied the package's
// BindingStore contract, and the single-seam rule flagged every other
// exported method on it — 14 findings, none of them about the API.
//
// No contract rule reads tests on purpose. The exclusion gate
// (exclude_directive.go) already reads only non-test source
// (isNonSourceFile), and external test packages were already skipped by the
// require-contract rule.

// isTestFile reports whether filename is a Go test file.
func isTestFile(filename string) bool {
	return strings.HasSuffix(filepath.Base(filename), "_test.go")
}

// exemptFilenames returns the on-disk filenames in the pass that are not the
// package's hand-written API: generated files (generated.go) and test files.
// Inspector-driven analyzers resolve a node's filename via pass.Fset and skip
// findings in these, while staying strict on the rest of the package.
func exemptFilenames(pass *analysis.Pass) map[string]bool {
	out := generatedFilenames(pass)
	for _, f := range pass.Files {
		if name := pass.Fset.Position(f.Pos()).Filename; isTestFile(name) {
			out[name] = true
		}
	}
	return out
}
