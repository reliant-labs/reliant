package contract

import (
	"flag"
	"go/token"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// excludeMu guards excludePatterns.
var excludeMu sync.RWMutex

// excludePatterns is the package-level list of package paths that should be
// skipped by every analyzer in this package. It is populated by SetExcludes
// (called from cmd/contractlint/main.go after parsing the -exclude flag) or
// by the per-analyzer -exclude flag handler below.
var excludePatterns []string

// SetExcludes replaces the package-level exclude list. It's called from
// cmd/contractlint after parsing a top-level -exclude flag, and may also be
// called from tests that want to exercise exclusion behavior.
func SetExcludes(patterns []string) {
	excludeMu.Lock()
	defer excludeMu.Unlock()
	excludePatterns = append(excludePatterns[:0:0], patterns...)
}

// GetExcludes returns a copy of the current exclude list.
func GetExcludes() []string {
	excludeMu.RLock()
	defer excludeMu.RUnlock()
	out := make([]string, len(excludePatterns))
	copy(out, excludePatterns)
	return out
}

// IsExcluded reports whether the given package path matches any
// configured exclude pattern. The matching rule is the canonical
// [config.MatchExclude] (segment-boundary matching: equality |
// "/"-suffix | "/"-prefix subtree | mid-path segment, with empty
// patterns skipped and forward-slash normalisation). Pre-2026-06 this
// package hand-rolled the same rule so it could stay zero-dependency
// on internal/config — but the three copies drifted on empty-pattern
// handling, and a single sourced helper is cheaper to keep correct.
func IsExcluded(pkgPath string) bool {
	excludeMu.RLock()
	defer excludeMu.RUnlock()
	return config.MatchExclude(excludePatterns, pkgPath)
}

// IsExcludedPass reports whether the package under analysis is opted out of
// the contract rules — by EITHER the configured forge.yaml contracts.exclude
// list (matched on the import path) OR the per-package
// //forge:exclude-contract header (matched on the package's source dir). The
// two are a union: a header is the local-source equivalent of a central
// exclude entry, so every contract analyzer must honor both or the header is
// only "half" an exclude (e.g. it would skip mock/shape codegen yet still
// fire the exported-vars rule). Mirrors the generate-time walks
// (discoverPackages, the mock walk, the contractcheck shape walk), which all
// union the central list with the directive.
//
// The directive lives in source, so we resolve the package directory from the
// first file's position. A pass with no files (synthetic) falls back to the
// path-only check.
func IsExcludedPass(pass *analysis.Pass) bool {
	if IsExcluded(pass.Pkg.Path()) {
		return true
	}
	dir := passPkgDir(pass)
	if dir == "" {
		return false
	}
	return codegen.HasExcludeContractDirective(dir)
}

// passPkgDir returns the on-disk directory of the package under analysis,
// derived from the first non-synthetic file's token position. Returns "" when
// the directory cannot be determined.
func passPkgDir(pass *analysis.Pass) string {
	for _, f := range pass.Files {
		pos := pass.Fset.Position(f.Pos())
		if pos.Filename != "" {
			return filepath.Dir(pos.Filename)
		}
	}
	// Defensive: if Files is empty but the fset has entries, no reliable
	// mapping exists. Use a zero Pos lookup as a last resort.
	if p := pass.Fset.Position(token.NoPos); p.Filename != "" {
		return filepath.Dir(p.Filename)
	}
	return ""
}

// excludeFlag is a flag.Value that accepts comma-separated package paths and
// stores them in the shared excludePatterns slice. It is registered on every
// analyzer so that go vet -vettool=contractlint and direct invocation both
// work even without going through cmd/contractlint's top-level parsing.
type excludeFlag struct{}

func (excludeFlag) String() string {
	return strings.Join(GetExcludes(), ",")
}

func (excludeFlag) Set(value string) error {
	if value == "" {
		SetExcludes(nil)
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	SetExcludes(out)
	return nil
}

// registerExcludeFlag attaches a -exclude flag to the given FlagSet. The flag
// value is shared across analyzers via the package-level excludePatterns
// slice, so setting it on any one analyzer is sufficient.
func registerExcludeFlag(fs *flag.FlagSet) {
	fs.Var(excludeFlag{}, "exclude", "comma-separated list of package paths to skip (matches forge.yaml contracts.exclude)")
}
