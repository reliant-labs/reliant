// File: internal/cli/lint/lint_format_standard.go
//
// What the format pre-pass formats TO: the project's own gate, read from its
// golangci-lint config — never forge's scaffold default in its place.
//
// The pre-pass exists so mechanical formatting never surfaces as a gate. That
// argument holds only while it applies the formatting the gate checks. It
// used to apply goimports with the go.mod module as the local prefix
// unconditionally: the setting the scaffolded .golangci.yml declares. reliant,
// adopted in place, declares goimports with no local-prefixes, and its
// pre-commit hook runs `go fmt`. So `forge lint --scope internal/grpc`
// regrouped the imports of six files there that reliant's gate, its hook and
// its author all accepted, and the hook and the pre-pass disagreed on every
// later edit.
//
// The rule now:
//
//   - gofmt is the floor. It is Go's own canonical formatting (`go fmt`,
//     every editor); no gate rejects it, so it is always applied.
//   - Import GROUPING follows the gate. goimports enabled (v2
//     formatters.enable, or v1 linters.enable) → goimports' grouping, with
//     exactly the local-prefixes the config declares (none declared, none
//     applied). gci enabled → gci owns grouping and golangci-lint --fix
//     applies it, so the pre-pass leaves grouping alone. Neither enabled, no
//     config, or a config that does not parse → no regrouping.
//
// A scaffolded project declares goimports with its module as the prefix, so
// nothing changes there: the pre-pass still matches the gate byte for byte.

package lint

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/checksums"
)

// goFormatStandard is the formatting the pre-pass applies to a project's Go.
type goFormatStandard struct {
	// goimports applies goimports' import grouping (with localPrefix);
	// false applies gofmt alone.
	goimports bool
	// localPrefix is the comma-separated local-prefixes the gate declares
	// for goimports ("" for none).
	localPrefix string
}

// format returns src formatted to the standard. An error means src does not
// parse.
func (s goFormatStandard) format(filename string, src []byte) ([]byte, error) {
	if !s.goimports {
		return format.Source(src)
	}
	return checksums.CanonicalGoSource(s.localPrefix, filename, src)
}

// projectGoFormatStandard reads the standard from the golangci-lint config a
// run started in root loads.
func projectGoFormatStandard(root string) goFormatStandard {
	path := findGolangciConfig(root)
	if path == "" {
		return goFormatStandard{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return goFormatStandard{}
	}
	doc := map[string]any{}
	if filepath.Ext(path) == ".toml" {
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			return goFormatStandard{}
		}
	} else if err := yaml.Unmarshal(raw, &doc); err != nil {
		return goFormatStandard{}
	}
	return goFormatStandardOf(doc)
}

// goFormatStandardOf reads a decoded golangci-lint config, v1 or v2. Both
// versions' keys are read together: a v1 key in a v2 file (or the reverse)
// is ignored by golangci-lint, and reading it here can only add a formatter
// the file names, never drop one.
func goFormatStandardOf(doc map[string]any) goFormatStandard {
	enabled := map[string]bool{}
	for _, list := range [][]string{
		stringsAt(doc, "formatters", "enable"), // v2
		stringsAt(doc, "linters", "enable"),    // v1 (goimports was a linter)
	} {
		for _, name := range list {
			enabled[name] = true
		}
	}
	if !enabled["goimports"] || enabled["gci"] {
		return goFormatStandard{}
	}
	var prefixes []string
	for _, list := range [][]string{
		stringsAt(doc, "formatters", "settings", "goimports", "local-prefixes"), // v2: a list
		stringsAt(doc, "linters-settings", "goimports", "local-prefixes"),       // v1: "a,b"
	} {
		for _, p := range list {
			for _, part := range strings.Split(p, ",") {
				if part = strings.TrimSpace(part); part != "" {
					prefixes = append(prefixes, part)
				}
			}
		}
	}
	return goFormatStandard{goimports: true, localPrefix: strings.Join(prefixes, ",")}
}

// stringsAt returns the string, or list of strings, at keys in doc.
func stringsAt(doc map[string]any, keys ...string) []string {
	var cur any = doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	switch v := cur.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
