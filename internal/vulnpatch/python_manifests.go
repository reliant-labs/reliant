package vulnpatch

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// The Python manifests the patcher reads: requirements files (hand-kept, or
// compiled by pip-compile or `uv pip compile`), and poetry.lock and uv.lock
// with the pyproject.toml beside each.

// The Python ecosystems, one per way a tree pins Python packages.
const (
	// EcosystemPip is a requirements file: its pin rewritten, or, when it is
	// compiled (a pip-compile or `uv pip compile` header, or a sibling
	// .in), recompiled with --upgrade-package.
	EcosystemPip Ecosystem = "pip"
	// EcosystemPoetry is poetry.lock: `poetry add` or `poetry update`.
	EcosystemPoetry Ecosystem = "poetry"
	// EcosystemUV is uv.lock: `uv lock --upgrade-package`.
	EcosystemUV Ecosystem = "uv"
)

// pyManifest is one requirements file, poetry.lock or uv.lock.
type pyManifest struct {
	eco  Ecosystem
	root string              // the tree, absolute
	rel  string              // the requirements file or lockfile, relative to the tree
	dir  string              // its directory, absolute
	pins map[string][]string // normalized name -> the versions pinned (==) or locked

	// A requirements file.
	reqs    map[string]pyRequirement // every requirement it names, pinned or not
	hashed  bool                     // --hash (pip's hash-checking mode)
	compile *pyCompile               // nil when it is kept by hand
	noRegen string                   // why a compiled file cannot be regenerated
	rank    int                      // compiled files it reads come first

	// poetry.lock and uv.lock: the pyproject.toml's declarations.
	direct map[string][]pyDecl
}

// pinsAt reports whether m pins name at version.
func (m *pyManifest) pinsAt(name, version string) bool {
	for _, v := range m.pins[name] {
		if pyEqual(v, version) {
			return true
		}
	}
	return false
}

// pyCompile is how a compiled requirements file is regenerated.
type pyCompile struct {
	argv    []string // the compile command, without --upgrade-package
	runDir  string   // where it runs, absolute
	sources []string // its .in and pyproject.toml inputs, absolute
	refs    []string // compiled files its .in sources read (-r, -c), absolute
}

// pyDecl is one declaration of a dependency in a pyproject.toml.
type pyDecl struct {
	section    string   // where: [project] dependencies, [tool.poetry.group.dev.dependencies], ...
	group      string   // poetry's --group ("" for main)
	constraint string   // the declared range
	poetry     bool     // constraint is poetry syntax (^, ~, bare versions), not PEP 440
	extras     []string // extras to keep when re-adding it
	addable    string   // "" when `poetry add` may rewrite it; else why not
}

// isRequirementsFile reports the requirements-format files: requirements*.txt,
// constraints*.txt, and any .txt in a requirements/ directory.
func isRequirementsFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(name, ".txt") {
		return false
	}
	return strings.HasPrefix(name, "requirements") || strings.HasPrefix(name, "constraints") ||
		strings.EqualFold(filepath.Base(filepath.Dir(path)), "requirements")
}

// otherPythonLockfiles are Python lockfiles with no patcher; a finding only
// they pin says so.
var otherPythonLockfiles = map[string]bool{"Pipfile.lock": true, "pdm.lock": true, "pylock.toml": true}

// addPython indexes path when it is a Python manifest; it returns a note
// when one is unreadable.
func (idx *index) addPython(root, path string) string {
	name := filepath.Base(path)
	var m *pyManifest
	var err error
	switch {
	case name == "poetry.lock" || name == "uv.lock":
		m, err = readPyLock(root, path)
	case isRequirementsFile(path):
		m, err = readRequirements(root, path)
	case otherPythonLockfiles[name]:
		idx.pyOther = append(idx.pyOther, relTo(root, path))
		return ""
	default:
		return ""
	}
	if err != nil {
		return fmt.Sprintf("%s: unreadable, ignored: %v", relTo(root, path), err)
	}
	idx.python = append(idx.python, m)
	return ""
}

// rankPython orders compiled requirements files after the compiled files
// their sources read (`-c requirements.txt` in dev.in), so a recompile
// never runs against a constraint not yet raised.
func (idx *index) rankPython() {
	byPath := map[string]*pyManifest{}
	for _, m := range idx.python {
		if m.eco == EcosystemPip {
			byPath[filepath.Join(m.dir, filepath.Base(m.rel))] = m
		}
	}
	var depth func(m *pyManifest, seen map[*pyManifest]bool) int
	depth = func(m *pyManifest, seen map[*pyManifest]bool) int {
		if m.compile == nil || seen[m] {
			return 0
		}
		seen[m] = true
		d := 0
		for _, ref := range m.compile.refs {
			if r := byPath[ref]; r != nil && r != m {
				d = max(d, depth(r, seen)+1)
			}
		}
		delete(seen, m)
		return d
	}
	for _, m := range idx.python {
		m.rank = depth(m, map[*pyManifest]bool{})
	}
}

// readRequirements reads a requirements file, and how it is compiled.
func readRequirements(root, abs string) (*pyManifest, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	m := &pyManifest{eco: EcosystemPip, root: root, rel: relTo(root, abs), dir: filepath.Dir(abs), pins: map[string][]string{}, reqs: map[string]pyRequirement{}}
	reqs, _, hashed, _ := parseRequirements(data)
	m.hashed = hashed
	for _, r := range reqs {
		if _, ok := m.reqs[r.name]; !ok {
			m.reqs[r.name] = r
		}
		if v, ok := r.pin(); ok {
			m.pins[r.name] = append(m.pins[r.name], v)
		}
	}
	for name, vs := range m.pins {
		m.pins[name] = uniqueSortedPy(vs)
	}
	m.compile, m.noRegen = compileOf(root, abs, data)
	return m, nil
}

// parseRequirements reads a requirements-format file: the requirements it
// names, the files it reads (-r, -c), whether it uses --hash, and the lines
// it could not read as any of those (a name with a malformed version
// specifier, say), which pip would read some other way or refuse.
func parseRequirements(data []byte) (reqs []pyRequirement, refs []string, hashed bool, unread []string) {
	for _, line := range logicalLines(data) {
		if i := commentStart(line); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "--hash") {
			hashed = true
		}
		if strings.HasPrefix(line, "-") {
			if ref := optionValue(line, "-r", "--requirement", "-c", "--constraint"); ref != "" {
				refs = append(refs, ref)
			}
			continue
		}
		if i := strings.Index(line, " -"); i >= 0 {
			line = line[:i] // per-requirement options: --hash=...
		}
		if strings.Contains(line, "://") && !strings.Contains(line, "@") {
			continue // a bare URL or VCS link
		}
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "/") || strings.HasPrefix(line, "~") {
			continue // a local path
		}
		if r, ok := parsePEP508(line); ok && validSpecifierSet(r) {
			reqs = append(reqs, r)
		} else {
			unread = append(unread, line)
		}
	}
	return reqs, refs, hashed, unread
}

// validSpecifierSet reports whether r's version specifier is one pip reads:
// none, a URL, or comma-separated PEP 440 clauses.
func validSpecifierSet(r pyRequirement) bool {
	if r.url || r.spec == "" {
		return true
	}
	for _, clause := range strings.Split(r.spec, ",") {
		if !pySpecifierClause.MatchString(strings.TrimSpace(clause)) {
			return false
		}
	}
	return true
}

// logicalLines joins lines continued with a trailing backslash.
func logicalLines(data []byte) []string {
	var out []string
	var cur strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasSuffix(line, "\\") {
			cur.WriteString(strings.TrimSuffix(line, "\\"))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// commentStart is where a requirements comment starts: a "#" at the start
// of the line or after whitespace (a URL fragment is not one); -1 if none.
func commentStart(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// optionValue is the value of the first of names that line sets ("-r
// base.txt", "--constraint=c.txt", "-rbase.txt").
func optionValue(line string, names ...string) string {
	for _, n := range names {
		rest, ok := strings.CutPrefix(line, n)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "="):
			return strings.TrimSpace(rest[1:])
		case strings.HasPrefix(rest, " "), strings.HasPrefix(rest, "\t"):
			return strings.TrimSpace(rest)
		case len(n) == 2 && rest != "": // -rfile
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// compileHeader finds the command a compiled requirements file records:
// "#    pip-compile --output-file=requirements.txt requirements.in", or
// "#    uv pip compile requirements.in -o requirements.txt".
var compileHeader = regexp.MustCompile(`^#\s+((?:pip-compile|uv pip compile)(?:\s.*)?)$`)

// compileOf says how the requirements file at abs is regenerated: by the
// command its header records, else by pip-compile from a sibling .in. It is
// (nil, "") for a file kept by hand, and (nil, why) for a compiled one it
// cannot regenerate.
func compileOf(root, abs string, data []byte) (*pyCompile, string) {
	var header []string
	for i, line := range strings.Split(string(data), "\n") {
		if i > 40 || line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		if m := compileHeader.FindStringSubmatch(strings.TrimRight(line, "\r ")); m != nil {
			header = shellFields(m[1])
			break
		}
	}
	sibling := strings.TrimSuffix(abs, filepath.Ext(abs)) + ".in"
	if header != nil {
		if c := compileFromHeader(root, abs, header); c != nil {
			return c, ""
		}
		if !exists(sibling) {
			return nil, fmt.Sprintf("its header records %#q, whose inputs are in neither its directory nor the tree's root", strings.Join(header, " "))
		}
	}
	if !exists(sibling) {
		return nil, ""
	}
	// No usable header: pip-compile the sibling .in, keeping hashes.
	argv := []string{"pip-compile", "--quiet"}
	if d, err := os.ReadFile(abs); err == nil && bytes.Contains(d, []byte("--hash")) {
		argv = append(argv, "--generate-hashes")
	}
	argv = append(argv, "--output-file="+filepath.Base(abs), filepath.Base(sibling))
	c := &pyCompile{argv: argv, runDir: filepath.Dir(abs), sources: []string{sibling}}
	c.refs = sourceRefs(c.sources)
	return c, ""
}

// compileFromHeader rebuilds the header's command: without the one-time
// flags (--upgrade, --upgrade-package, --dry-run), with an explicit output
// file, run where its input files resolve (the file's directory, else the
// tree's root). nil when its inputs resolve in neither.
func compileFromHeader(root, abs string, header []string) *pyCompile {
	tool := header[:1]
	args := header[1:]
	if header[0] == "uv" {
		tool, args = header[:3], header[3:]
	}
	var kept, inputs, files []string
	output := ""
	takesValue := map[string]bool{
		"-o": true, "--output-file": true, "-c": true, "--constraint": true, "--constraints": true,
		"--extra": true, "--group": true, "-i": true, "--index-url": true, "--extra-index-url": true,
		"-f": true, "--find-links": true, "--python-version": true, "--python": true,
		"--pip-args": true, "--resolver": true, "--cache-dir": true, "--override": true, "--overrides": true,
		"--python-platform": true, "--custom-compile-command": true, "--unsafe-package": true,
		"--no-emit-package": true, "--index": true, "--default-index": true,
		"-p": tool[0] == "uv", // uv's --python; pip-compile has no -p
	}
	inputFlag := map[string]bool{"-c": true, "--constraint": true, "--constraints": true, "--override": true, "--overrides": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, _, hasEq := strings.Cut(a, "=")
		switch flag {
		case "-U", "--upgrade", "--dry-run", "-n", "--rebuild", "-q", "--quiet", "-v", "--verbose":
			continue
		case "-P", "--upgrade-package":
			if !hasEq {
				i++
			}
			continue
		case "-o", "--output-file":
			if hasEq {
				output = strings.SplitN(a, "=", 2)[1]
			} else if i+1 < len(args) {
				output = args[i+1]
				i++
			}
			continue
		}
		kept = append(kept, a)
		if strings.HasPrefix(a, "-") {
			value := ""
			if takesValue[flag] && !hasEq && i+1 < len(args) {
				i++
				value = args[i]
				kept = append(kept, value)
			} else if hasEq {
				value = strings.SplitN(a, "=", 2)[1]
			}
			if inputFlag[flag] && value != "" {
				files = append(files, value)
			}
			continue
		}
		inputs = append(inputs, a)
	}
	runDir := ""
	for _, dir := range []string{filepath.Dir(abs), root} {
		ok := true
		for _, in := range append(append([]string{}, inputs...), files...) {
			ok = ok && exists(filepath.Join(dir, in))
		}
		if ok {
			runDir = dir
			break
		}
	}
	if runDir == "" {
		return nil
	}
	if output == "" {
		output = relTo(runDir, abs)
	}
	argv := append(append([]string{}, tool...), kept...)
	if tool[0] == "uv" {
		argv = append(argv, "--quiet", "-o", output)
	} else {
		argv = append(argv, "--quiet", "--output-file="+output)
	}
	c := &pyCompile{argv: argv, runDir: runDir}
	if len(inputs) == 0 && tool[0] == "pip-compile" {
		inputs = []string{"requirements.in"} // pip-compile's default input
	}
	for _, in := range inputs {
		if p := filepath.Join(runDir, in); exists(p) && (strings.HasSuffix(in, ".in") || filepath.Base(in) == "pyproject.toml") {
			c.sources = append(c.sources, p)
		}
	}
	c.refs = sourceRefs(c.sources)
	return c
}

// sourceRefs are the compiled files .in sources read: "-c requirements.txt"
// names one; "-r base.in" names the file base.in compiles to.
func sourceRefs(sources []string) []string {
	var out []string
	for _, src := range sources {
		if !strings.HasSuffix(src, ".in") {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		_, refs, _, _ := parseRequirements(data)
		for _, ref := range refs {
			p := filepath.Join(filepath.Dir(src), ref)
			if strings.HasSuffix(p, ".in") {
				p = strings.TrimSuffix(p, ".in") + ".txt"
			}
			out = append(out, p)
		}
	}
	return out
}

// shellFields splits a recorded command line: whitespace-separated, with
// '...' and "..." quoting.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// readPyLock reads poetry.lock or uv.lock, and the pyproject.toml beside it.
func readPyLock(root, abs string) (*pyManifest, error) {
	var lock struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
	}
	if _, err := toml.DecodeFile(abs, &lock); err != nil {
		return nil, err
	}
	m := &pyManifest{eco: EcosystemPoetry, root: root, rel: relTo(root, abs), dir: filepath.Dir(abs), pins: map[string][]string{}}
	if filepath.Base(abs) == "uv.lock" {
		m.eco = EcosystemUV
	}
	for _, p := range lock.Package {
		if p.Name != "" && p.Version != "" {
			m.pins[pyName(p.Name)] = append(m.pins[pyName(p.Name)], p.Version)
		}
	}
	for name, vs := range m.pins {
		m.pins[name] = uniqueSortedPy(vs)
	}
	var err error
	m.direct, err = readPyProject(filepath.Join(m.dir, "pyproject.toml"))
	if err != nil {
		return nil, fmt.Errorf("pyproject.toml beside it: %w", err)
	}
	return m, nil
}

// readPyProject is every dependency a pyproject.toml declares: PEP 621
// [project] dependencies and optional-dependencies, PEP 735
// [dependency-groups], [tool.uv] dev-dependencies, and poetry's
// [tool.poetry] dependencies, dev-dependencies and groups.
func readPyProject(path string) (map[string][]pyDecl, error) {
	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		return nil, err
	}
	out := map[string][]pyDecl{}
	add508 := func(section, group string, entries any, addable string) {
		list, _ := entries.([]any)
		for _, e := range list {
			s, ok := e.(string)
			if !ok {
				continue // a table: an include-group, nothing pinned
			}
			r, ok := parsePEP508(s)
			if !ok {
				continue
			}
			d := pyDecl{section: section, group: group, constraint: r.spec, extras: r.extras, addable: addable}
			switch {
			case r.url:
				d.constraint, d.addable = "", "it is a URL dependency"
			case r.markers && addable == "":
				d.addable = "it carries environment markers"
			}
			out[r.name] = append(out[r.name], d)
		}
	}
	project, _ := doc["project"].(map[string]any)
	add508("[project] dependencies", "", project["dependencies"], "")
	if opt, ok := project["optional-dependencies"].(map[string]any); ok {
		for extra, entries := range opt {
			add508("[project.optional-dependencies] "+extra, "", entries, "it is an optional extra ("+extra+")")
		}
	}
	if groups, ok := doc["dependency-groups"].(map[string]any); ok {
		for g, entries := range groups {
			add508("[dependency-groups] "+g, g, entries, "")
		}
	}
	tool, _ := doc["tool"].(map[string]any)
	if uv, ok := tool["uv"].(map[string]any); ok {
		add508("[tool.uv] dev-dependencies", "dev", uv["dev-dependencies"], "")
	}
	poetry, _ := tool["poetry"].(map[string]any)
	addPoetry := func(section, group string, deps any) {
		table, _ := deps.(map[string]any)
		for name, v := range table {
			if strings.EqualFold(name, "python") {
				continue
			}
			d := poetryDecl(section, group, v)
			if section == "[tool.poetry.dev-dependencies]" && d.addable == "" {
				// `poetry add --group dev` writes [tool.poetry.group.dev],
				// leaving a second declaration here.
				d.addable = "it is in the legacy dev-dependencies section"
			}
			out[pyName(name)] = append(out[pyName(name)], d)
		}
	}
	addPoetry("[tool.poetry.dependencies]", "", poetry["dependencies"])
	addPoetry("[tool.poetry.dev-dependencies]", "dev", poetry["dev-dependencies"])
	if groups, ok := poetry["group"].(map[string]any); ok {
		for g, body := range groups {
			if t, ok := body.(map[string]any); ok {
				addPoetry("[tool.poetry.group."+g+".dependencies]", g, t["dependencies"])
			}
		}
	}
	for name := range out {
		sort.SliceStable(out[name], func(i, j int) bool { return out[name][i].section < out[name][j].section })
	}
	return out, nil
}

// poetryDecl reads one [tool.poetry] dependency: a constraint string, a
// table, or a list of tables (one per environment).
func poetryDecl(section, group string, v any) pyDecl {
	d := pyDecl{section: section, group: group, poetry: true}
	switch v := v.(type) {
	case string:
		d.constraint = v
	case map[string]any:
		d.constraint, _ = v["version"].(string)
		if xs, ok := v["extras"].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					d.extras = append(d.extras, s)
				}
			}
		}
		var other []string
		for k := range v {
			if k != "version" && k != "extras" {
				other = append(other, k)
			}
		}
		sort.Strings(other)
		if len(other) > 0 {
			d.addable = "it is declared with " + strings.Join(other, ", ")
		}
		if d.constraint == "" {
			d.addable = "it is declared with no version (" + strings.Join(other, ", ") + ")"
		}
	default:
		d.addable = "it is declared with several constraints"
	}
	return d
}

func uniqueSortedPy(xs []string) []string {
	var out []string
	for _, x := range xs {
		dup := false
		for _, y := range out {
			dup = dup || pyEqual(x, y)
		}
		if !dup {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return comparePyVersions(out[i], out[j]) < 0 })
	return out
}
