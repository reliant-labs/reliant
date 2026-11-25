package testreport

import (
	"fmt"
	"path"
	"strings"
)

// Lang is the language a test or a covered file belongs to. Coverage
// percentages are never added across languages without saying so: a Go
// statement and a TypeScript statement are not the same unit.
type Lang string

// The languages forge's own parsers produce. Other values are allowed: a
// JUnit or LCOV input from another toolchain carries the language its caller
// names, or the file extension's.
const (
	LangGo Lang = "go"
	LangTS Lang = "ts"
	// LangOther is a test or file whose language could not be told.
	LangOther Lang = "other"
)

// LangFromPath names the language of a source file by its extension:
// .go is Go; .ts, .tsx, .js, .jsx, .mjs, .cjs, .mts, .cts, .vue and .svelte
// are TypeScript (the frontend lane); anything else is the extension itself
// without its dot, or [LangOther] when there is none.
func LangFromPath(p string) Lang {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(p, `\`, "/")))
	switch ext {
	case ".go":
		return LangGo
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte":
		return LangTS
	case "":
		return LangOther
	}
	return Lang(ext[1:])
}

// TestID identifies one test case: its language, its package (a Go import
// path; a test file's repo-relative path for Vitest; a JUnit classname) and
// its name (a Go test with its subtest path, `TestRefund/partial`).
//
// Its string form, used in every document, is `<lang>:<package>:<name>`, for
// example `go:github.com/acme/shop/internal/billing:TestRefund/partial`. A
// ':' or '%' inside the package is percent-encoded so the form always splits
// back ([ParseTestID]); the name is everything after the second ':' and is
// never encoded.
//
// A TestID with an empty Name stands for its whole package: a build failure,
// or a failure in TestMain that no test case owns.
type TestID struct {
	Lang    Lang
	Package string
	Name    string
}

// String is the `<lang>:<package>:<name>` form.
func (id TestID) String() string {
	pkg := strings.NewReplacer("%", "%25", ":", "%3A").Replace(id.Package)
	return string(id.Lang) + ":" + pkg + ":" + id.Name
}

// ParseTestID reads the `<lang>:<package>:<name>` form back.
func ParseTestID(s string) (TestID, error) {
	lang, rest, ok := strings.Cut(s, ":")
	if !ok || lang == "" {
		return TestID{}, fmt.Errorf("testreport: test id %q: want <lang>:<package>:<name>", s)
	}
	pkg, name, ok := strings.Cut(rest, ":")
	if !ok {
		return TestID{}, fmt.Errorf("testreport: test id %q: want <lang>:<package>:<name>", s)
	}
	pkg = strings.NewReplacer("%3A", ":", "%3a", ":", "%25", "%").Replace(pkg)
	return TestID{Lang: Lang(lang), Package: pkg, Name: name}, nil
}

// MarshalText makes a TestID a JSON string.
func (id TestID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText reads the string form.
func (id *TestID) UnmarshalText(b []byte) error {
	v, err := ParseTestID(string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}

// TopLevel is the test that `-run` and a quarantine entry address: for Go,
// the name up to its first '/', so `TestRefund/partial` is `TestRefund`.
// Flake scoring is per top-level test. Other languages have no subtest path
// in the name, so their TopLevel is the id itself.
func (id TestID) TopLevel() TestID {
	if id.Lang == LangGo {
		if i := strings.IndexByte(id.Name, '/'); i >= 0 {
			id.Name = id.Name[:i]
		}
	}
	return id
}

// IsSubtest reports whether the id names a Go subtest.
func (id TestID) IsSubtest() bool {
	return id.Lang == LangGo && strings.IndexByte(id.Name, '/') >= 0
}

// Lane names the suite a run came from: the project's Taskfile lane, such as
// "unit", "integration" or "e2e". A lane is the caller's name; forge treats
// only the e2e lanes specially ([Lane.IsE2E]).
type Lane string

// The lanes forge scaffolds.
const (
	LaneUnit        Lane = "unit"
	LaneIntegration Lane = "integration"
	LaneE2E         Lane = "e2e"
	// LaneConfirm is the lane `forge test flakes confirm` records its reruns
	// under.
	LaneConfirm Lane = "confirm"
)

// IsE2E reports whether the lane drives a running stack: "e2e", or a name
// starting "e2e-" ("e2e-preview"). The only-e2e coverage view is the
// statements such lanes reach and no other lane does.
func (l Lane) IsE2E() bool {
	return l == LaneE2E || strings.HasPrefix(string(l), string(LaneE2E)+"-")
}

// Status is a test case's result.
type Status string

// The three results. A test that was announced and never finished has no
// Status; its package is [PackageResult.Incomplete].
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Format names an input format: the results formats and the coverage
// formats.
type Format string

// The formats the parsers read.
const (
	// FormatGoJSON is a `go test -json` stream.
	FormatGoJSON Format = "go-json"
	// FormatJUnit is JUnit XML (`<testsuites>` or `<testsuite>`).
	FormatJUnit Format = "junit"
	// FormatVitest is the Vitest (and Jest) JSON reporter's document.
	FormatVitest Format = "vitest"
	// FormatGoCover is a Go text coverage profile: `-coverprofile` output,
	// or `go tool covdata textfmt` output, which is the same format.
	FormatGoCover Format = "go-cover"
	// FormatLCOV is an LCOV tracefile (lcov.info).
	FormatLCOV Format = "lcov"
	// FormatCobertura is Cobertura XML.
	FormatCobertura Format = "cobertura"
	// FormatIstanbul is istanbul's coverage-final.json.
	FormatIstanbul Format = "istanbul"
	// FormatUnknown is input no parser recognised.
	FormatUnknown Format = "unknown"
)

// IsResults reports whether f is a results format.
func (f Format) IsResults() bool {
	return f == FormatGoJSON || f == FormatJUnit || f == FormatVitest
}

// IsCoverage reports whether f is a coverage format.
func (f Format) IsCoverage() bool {
	return f == FormatGoCover || f == FormatLCOV || f == FormatCobertura || f == FormatIstanbul
}
