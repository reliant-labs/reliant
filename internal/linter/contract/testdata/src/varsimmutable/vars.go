package varsimmutable

import (
	"fmt"
	"regexp"
)

const (
	gitURL = "https://example.com/x.git"
	tag    = "v0.1.0"
)

// Immutable lookup tables and compiled regexes: values that are `var`
// only because Go has no const slice, map or regexp. They are data, not
// mutable state, and a getter around them adds a call and copies nothing
// meaningful — the caller still gets the same shared backing array.

// KnownMarkers is a fixed vocabulary shared by several scanners.
var KnownMarkers = []string{"forge:entity", "forge:soft-delete"}

// Reserved documents a fixed option vocabulary.
var Reserved = map[string]string{
	"env":       "the environment name",
	"namespace": "the k8s namespace",
}

// MarkerRE is a package-level compiled regex — regexp.MustCompile cannot
// be a const, and recompiling per call is the thing this avoids.
var MarkerRE = regexp.MustCompile(`//\s*forge:gen\s+(\S+)`)

// DepLine is a fmt.Sprintf over consts — const-shaped, but Sprintf is not
// a constant expression.
var DepLine = fmt.Sprintf("forge = { git = %q, tag = %q }", gitURL, tag)
