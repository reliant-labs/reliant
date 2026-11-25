package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// The runtime's package.json is the public module map. Never search dist
// chunks or other entrypoints for evidence that a symbol is exported here.
type webRuntimeManifest struct {
	Version string                     `json:"version"`
	Main    string                     `json:"main"`
	Types   string                     `json:"types"`
	Exports map[string]json.RawMessage `json:"exports"`
}

type webRuntimeEntry struct{ js, declarations string }

func (m webRuntimeManifest) entry(subpath string) webRuntimeEntry {
	var e webRuntimeEntry
	if raw, ok := m.Exports[subpath]; ok {
		var conditions map[string]string
		if json.Unmarshal(raw, &conditions) == nil {
			e.js, e.declarations = conditions["default"], conditions["types"]
			if e.js == "" {
				e.js = conditions["import"]
			}
		} else {
			_ = json.Unmarshal(raw, &e.js)
		}
	} else if subpath == "." && m.Exports == nil {
		e.js, e.declarations = m.Main, m.Types
	}
	return e
}

// packageFile rejects absolute paths and paths outside the installed package.
func packageFile(root, target string) string {
	if target == "" || !filepath.IsLocal(filepath.FromSlash(target)) {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(target))
}

// jsExports asks the already-linked JS parser/bundler for the entrypoint's
// actual module exports. It reads static imports; it does not execute JS or
// resolve external npm packages. An unsupported shape is not a verdict.
func jsExports(root, target string) (map[string]bool, bool) {
	entry := packageFile(root, target)
	if entry == "" {
		return nil, false
	}
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{entry}, AbsWorkingDir: root, Bundle: true,
		Format: api.FormatESModule, Packages: api.PackagesExternal,
		Metafile: true, Write: false, LogLevel: api.LogLevelSilent,
	})
	if len(res.Errors) != 0 {
		return nil, false
	}
	var meta struct {
		Outputs map[string]struct {
			EntryPoint string
			Exports    []string
		}
	}
	if json.Unmarshal([]byte(res.Metafile), &meta) != nil {
		return nil, false
	}
	for _, out := range meta.Outputs {
		if out.EntryPoint != "" {
			set := map[string]bool{}
			for _, name := range out.Exports {
				set[name] = true
			}
			return set, true
		}
	}
	return nil, false
}

type webRuntimeSurface struct {
	dir      string
	manifest webRuntimeManifest
	entries  map[string]checkedRuntimeEntry
}

type checkedRuntimeEntry struct {
	js, types   map[string]bool
	jsOK, tsOK  bool
	declaration string
}

func (s *webRuntimeSurface) entry(subpath string) checkedRuntimeEntry {
	if entry, ok := s.entries[subpath]; ok {
		return entry
	}
	var entry checkedRuntimeEntry
	declared := s.manifest.entry(subpath)
	entry.js, entry.jsOK = jsExports(s.dir, declared.js)
	if p := packageFile(s.dir, declared.declarations); p != "" {
		if body, err := os.ReadFile(p); err == nil {
			entry.declaration = string(body)
			entry.types, entry.tsOK = namedDeclarationExports(entry.declaration)
		}
	}
	// Absent subpath in an authoritative export map should be reported,
	// unlike an unbuilt local package or unsupported condition target.
	if _, exists := s.manifest.Exports[subpath]; s.manifest.Exports != nil && !exists {
		entry.jsOK, entry.tsOK = true, true
	}
	s.entries[subpath] = entry
	return entry
}

func (s *webRuntimeSurface) missing(source string) map[string]bool {
	missing := map[string]bool{}
	code := stripTSProse(source)
	for _, m := range runtimeImports.FindAllStringSubmatch(tsCommentRe.ReplaceAllString(source, " "), -1) {
		subpath := m[3]
		if subpath == "" {
			subpath = "."
		} else {
			subpath = "." + subpath
		}
		entry := s.entry(subpath)
		for clause := range strings.SplitSeq(m[2], ",") {
			words := strings.Fields(clause)
			if len(words) == 0 {
				continue
			}
			typeOnly := m[1] != "" || (words[0] == "type" && len(words) > 1 && words[1] != "as")
			if words[0] == "type" && len(words) > 1 && words[1] != "as" {
				words = words[1:]
			}
			name, local := words[0], words[len(words)-1]
			if (entry.tsOK && !entry.types[name]) || (!typeOnly && entry.jsOK && !entry.js[name]) {
				missing[name] = true
				continue
			}
			if typeOnly || !entry.types[name] {
				continue
			}
			for _, opt := range runtimeOptionTypes {
				if opt.module != m[3] || opt.function != name {
					continue
				}
				if supported, ok := functionOptionMembers(entry.declaration, name); ok {
					for key := range generatedOptionMembers(code, local) {
						if !supported[key] {
							missing[key] = true
						}
					}
				}
			}
		}
	}
	return missing
}

var (
	runtimeImports = regexp.MustCompile(`(?m)\b(?:import|export)\s+(type\s+)?\{([^}]+)\}\s*from\s*['"]@reliantlabs/forge-web-runtime(/[^'"]+)?['"]`)
	declNamed      = regexp.MustCompile(`(?m)^\s*export\s+(?:declare\s+)?(?:abstract\s+)?(?:const|let|var|function|class|interface|type|enum|namespace)\s+([A-Za-z_$][\w$]*)\b`)
	declList       = regexp.MustCompile(`(?m)^\s*export\s+(?:type\s+)?\{([^}]+)\}`)
	declStar       = regexp.MustCompile(`(?m)^\s*export\s*\*`)
)

func namedDeclarationExports(text string) (map[string]bool, bool) {
	text = stripTSProse(text)
	if declStar.MatchString(text) {
		return nil, false // may re-export a name from another declaration file
	}
	out := map[string]bool{}
	for _, m := range declNamed.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	for _, m := range declList.FindAllStringSubmatch(text, -1) {
		for item := range strings.SplitSeq(m[1], ",") {
			words := strings.Fields(item)
			if len(words) > 0 {
				out[words[len(words)-1]] = true
			}
		}
	}
	return out, true
}

// optionType ties a config literal in generated code to the public function
// that consumes it. A property on some other runtime type proves nothing.
type optionType struct{ module, function string }

var runtimeOptionTypes = []optionType{
	{"/telemetry", "initBrowserTelemetry"},
	{"/mock-transport", "createMockTransport"},
}

func functionOptionMembers(text, function string) (map[string]bool, bool) {
	text = stripTSProse(text)
	decl := regexp.MustCompile(`(?m)^\s*export\s+(?:declare\s+)?function\s+` + regexp.QuoteMeta(function) + `\s*\(\s*\w+\??\s*:\s*([A-Za-z_$][\w$]*)\s*[,)]`).FindStringSubmatch(text)
	if decl == nil {
		return nil, false
	}
	// Forge's emitted options declarations are standalone interfaces. If a
	// future runtime uses a mapped type, extends clause, or index signature,
	// don't claim to know which options are legal from a partial parse.
	iface := regexp.MustCompile(`(?m)^\s*(?:export\s+)?interface\s+` + regexp.QuoteMeta(decl[1]) + `\s*\{`).FindStringIndex(text)
	if iface == nil {
		return nil, false
	}
	return objectMembers(text[iface[1]-1:], true)
}

var objectMember = regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*\??\s*:`)

// objectMembers reads direct member names, not keys or identifiers inside a
// nested value. Unknown declaration forms aren't used as absence evidence.
func objectMembers(text string, declaration bool) (map[string]bool, bool) {
	if !strings.HasPrefix(text, "{") {
		return nil, false
	}
	set := map[string]bool{}
	depth, start := 1, 1
	collect := func(field string) bool {
		field = strings.TrimSpace(field)
		if field == "" {
			return true
		}
		if m := objectMember.FindStringSubmatch(strings.TrimPrefix(field, "readonly ")); m != nil {
			set[m[1]] = true
			return true
		}
		return !declaration
	}
	for i := 1; i < len(text); i++ {
		switch text[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		}
		if depth == 0 {
			return set, collect(text[start:i])
		}
		if depth == 1 && (text[i] == ',' || (declaration && (text[i] == ';' || newlineEndsMember(text, start, i)))) {
			if !collect(text[start:i]) {
				return nil, false
			}
			start = i + 1
		}
	}
	return nil, false
}

// newlineEndsMember reports whether the newline at text[i] separates two
// interface members: TypeScript lets a member end at a line break with no
// comma or semicolon. It does only when the line after it starts a member
// and the text since the last separator holds one, so a type continued on
// the next line (`a:\n | "x"`) stays a single member.
func newlineEndsMember(text string, start, i int) bool {
	if text[i] != '\n' || strings.TrimSpace(text[start:i]) == "" {
		return false
	}
	next := strings.TrimLeft(text[i+1:], " \t\r")
	return objectMember.MatchString(strings.TrimPrefix(next, "readonly "))
}

func generatedOptionMembers(text, local string) map[string]bool {
	callRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\s*\(\s*(\{|[A-Za-z_$][\w$]*\s*\))`)
	out := map[string]bool{}
	for _, call := range callRE.FindAllStringSubmatchIndex(text, -1) {
		arg := text[call[2]:call[3]]
		start := call[2]
		if arg != "{" {
			name := strings.TrimSpace(strings.TrimSuffix(arg, ")"))
			decl := regexp.MustCompile(`\bconst\s+`+regexp.QuoteMeta(name)+`\s*=\s*\{`).FindAllStringIndex(text[:call[0]], -1)
			if len(decl) == 0 {
				continue
			}
			start = decl[len(decl)-1][1] - 1
		}
		keys, _ := objectMembers(text[start:], false)
		for name := range keys {
			out[name] = true
		}
	}
	return out
}
