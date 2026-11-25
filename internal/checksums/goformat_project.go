package checksums

import (
	"errors"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// What a project's Go must be formatted TO: its own gate, read from its
// golangci-lint config.
//
// Every forge step that writes Go the author did not type — `forge lint --fix`'s
// pre-pass, `forge fix narrow-deps`, and every codemod `forge project upgrade`
// runs — has to leave bytes the project's formatter gate accepts, or the
// formatting findings forge produced itself land in front of a person. They
// share this one reading of the gate so they cannot disagree about it.
//
//   - gofmt is the floor: Go's own canonical formatting, which no gate rejects.
//   - Import GROUPING follows the gate. goimports enabled (v2
//     formatters.enable, or v1 linters.enable) → goimports' grouping, with
//     exactly the local-prefixes the config declares (none declared, none
//     applied). gci enabled → gci owns grouping and golangci-lint --fix applies
//     it, so grouping is left alone. Neither enabled → gofmt alone.

// GolangciConfigNames are the files golangci-lint reads its config from, in the
// order it tries them within one directory.
var GolangciConfigNames = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}

// FindGolangciConfig returns the config file golangci-lint loads for a run
// started in dir: the first of GolangciConfigNames in dir, else in the nearest
// ancestor that has one. "" when there is none.
func FindGolangciConfig(dir string) string {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		for _, name := range GolangciConfigNames {
			p := filepath.Join(abs, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return ""
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// GoFormatStandard is the formatting applied to a project's Go.
type GoFormatStandard struct {
	// GoImports applies goimports' import grouping (with LocalPrefix); false
	// applies gofmt alone.
	GoImports bool
	// LocalPrefix is the comma-separated local-prefixes the gate declares for
	// goimports ("" for none).
	LocalPrefix string
	// GroupingOwned is true when gci is enabled: gci owns import grouping and
	// golangci-lint --fix applies it, so forge leaves grouping alone.
	GroupingOwned bool
	// Declared is true when the project has a golangci-lint config that parsed,
	// i.e. the standard above is the project's word and not a default.
	Declared bool
}

// Format returns src formatted to the standard. An error means src does not
// parse.
func (s GoFormatStandard) Format(filename string, src []byte) ([]byte, error) {
	if !s.GoImports {
		return format.Source(src)
	}
	return CanonicalGoSource(s.LocalPrefix, filename, src)
}

// ProjectGoFormat reads the standard from the golangci-lint config a run
// started in root loads. No config, or one that does not parse, states no
// standard: the zero value (gofmt alone) is returned, because rewriting the
// author's files must not regroup what no gate checks. `forge lint --fix`
// formats to this.
func ProjectGoFormat(root string) GoFormatStandard {
	path := FindGolangciConfig(root)
	if path == "" {
		return GoFormatStandard{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return GoFormatStandard{}
	}
	doc := map[string]any{}
	if filepath.Ext(path) == ".toml" {
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			return GoFormatStandard{}
		}
	} else if err := yaml.Unmarshal(raw, &doc); err != nil {
		return GoFormatStandard{}
	}
	std := goFormatStandardOf(doc)
	std.Declared = true
	return std
}

// RewriteGoFormat is the standard for a file forge has just REWRITTEN (a
// codemod, `forge fix narrow-deps`): bytes the project's linters accept, so the
// formatting findings forge produced itself never reach a person.
//
// It is ProjectGoFormat wherever the project has a golangci-lint config: its
// own local-prefixes (or none), gci's hand-off, or gofmt alone. Only a project
// with no config at all gets forge's own convention, the one the scaffolded
// .golangci.yml declares and the generate pipeline writes: goimports with the
// module path as the local prefix.
func RewriteGoFormat(root string) GoFormatStandard {
	std := ProjectGoFormat(root)
	if std.Declared {
		return std
	}
	return GoFormatStandard{GoImports: true, LocalPrefix: GoImportsLocalPrefix(root)}
}

// goFormatStandardOf reads a decoded golangci-lint config, v1 or v2. Both
// versions' keys are read together: a v1 key in a v2 file (or the reverse) is
// ignored by golangci-lint, and reading it here can only add a formatter the
// file names, never drop one.
func goFormatStandardOf(doc map[string]any) GoFormatStandard {
	enabled := map[string]bool{}
	for _, list := range [][]string{
		stringsAt(doc, "formatters", "enable"), // v2
		stringsAt(doc, "linters", "enable"),    // v1 (goimports was a linter)
	} {
		for _, name := range list {
			enabled[name] = true
		}
	}
	if enabled["gci"] {
		return GoFormatStandard{GroupingOwned: true}
	}
	if !enabled["goimports"] {
		return GoFormatStandard{}
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
	return GoFormatStandard{GoImports: true, LocalPrefix: strings.Join(prefixes, ",")}
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
