package staticexport

// The Vite half of the lane.
//
// A Vite build is static by construction, so none of the Next.js rules apply.
// What CAN break a Vite frontend on a static runtime is the seam between the
// build and the deploy, which nothing checks until a page loads blank:
//
//   - build.outDir — where `vite build` writes — must be the directory the env
//     publishes (the Frontend's `public_dir`, "dist" when unset). If they
//     differ the deploy publishes a directory the build never wrote: missing,
//     or a stale one from an older build.
//   - base — the prefix Vite writes into every asset URL in index.html — must
//     match where the env mounts the frontend (`base_path`, the site root when
//     unset). forge passes no base to the build, so a mismatch makes every
//     script and stylesheet 404.
//
// Both are read from the config's TEXT, conservatively: a string literal is a
// value, an absent key is Vite's default, and anything else (a computed value,
// two different literals, a spread or mergeConfig that could carry the key, a
// config object this reader cannot find) is reported as NOT DETERMINABLE in a
// note rather than guessed at. A rule that invents findings trains people to
// suppress it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Vite rule IDs.
const (
	// RuleViteOutDir: the env publishes a public_dir Vite does not build into.
	RuleViteOutDir = "static-export-vite-out-dir"
	// RuleViteBase: Vite's base disagrees with where the env mounts the
	// frontend.
	RuleViteBase = "static-export-vite-base"
)

// ViteResult is one Vite frontend's verdict: its findings, and a note per
// thing it checked — passed, or why it could not be determined.
type ViteResult struct {
	Findings []finding.Finding
	Notes    []string
}

// viteConfigNames are the files Vite loads its config from, in its own order
// (vite's DEFAULT_CONFIG_FILES).
var viteConfigNames = []string{"vite.config.js", "vite.config.mjs", "vite.config.ts", "vite.config.cjs", "vite.config.mts", "vite.config.cts"}

// CheckVite analyses one Vite frontend bound to a static runtime. fe.Bindings
// carry each env's public_dir and base_path. A frontend directory that does
// not exist yields nothing.
func CheckVite(fe Frontend) (ViteResult, error) {
	var res ViteResult
	if fi, err := os.Stat(fe.Dir); err != nil || !fi.IsDir() {
		return res, nil //nolint:nilerr // absent frontend dir: nothing to judge
	}
	v := &viteChecker{fe: fe}
	v.cfg = loadViteConfig(fe.Dir)
	pkg, err := loadPackageJSON(fe.Dir)
	if err != nil {
		return res, err
	}
	v.pkg = pkg
	script := v.buildScript()
	v.checkOutDir(script)
	v.checkBase(script)
	sort.SliceStable(v.out, func(i, j int) bool {
		if v.out[i].File != v.out[j].File {
			return v.out[i].File < v.out[j].File
		}
		return v.out[i].Line < v.out[j].Line
	})
	res.Findings, res.Notes = v.out, v.notes
	return res, nil
}

type viteChecker struct {
	fe    Frontend
	cfg   *viteConfig
	pkg   *packageJSON
	out   []finding.Finding
	notes []string
}

func (v *viteChecker) rel(frontendRel string) string {
	if v.fe.RelDir == "" || v.fe.RelDir == "." {
		return frontendRel
	}
	return path.Join(v.fe.RelDir, frontendRel)
}

func (v *viteChecker) add(rule string, sev finding.Severity, file string, line int, msg, fix string) {
	v.out = append(v.out, finding.Finding{Rule: rule, Severity: sev, File: file, Line: line, Message: msg, Remediation: fix})
}

// ── Resolved values ──────────────────────────────────────────────────────

// viteValue is one setting as the build will see it.
type viteValue struct {
	known bool
	value string
	// file/line anchor a finding: where the value is set, or where it would
	// be set. file is frontend-relative; "" means the frontend dir itself.
	file string
	line int
	// where says, for messages, where the value came from: "vite.config.ts:41",
	// "Vite's default", "--outDir in package.json's build script".
	where string
	// why says why the value is not known.
	why string
}

func unknownValue(why string) viteValue { return viteValue{why: why} }

// viteScript is what package.json's build script overrides on the command
// line. The flags win over the config file.
type viteScript struct {
	present bool
	// unknown says the script makes both settings undeterminable.
	unknown string
	outDir  *viteValue
	base    *viteValue
}

// buildScript reads `vite build`'s flags out of package.json's build script —
// what forge runs (`npm run build`) to build a static frontend.
func (v *viteChecker) buildScript() viteScript {
	if v.pkg == nil || v.pkg.Scripts["build"] == "" {
		return viteScript{}
	}
	raw := v.pkg.Scripts["build"]
	s := viteScript{present: true}
	line := v.pkg.lineOf(`"build"\s*:`)
	vite, reason := viteBuildArgs(raw)
	if reason != "" {
		s.unknown = fmt.Sprintf("package.json's build script (%q) %s", raw, reason)
		return s
	}
	flag := func(name string) (*viteValue, bool) {
		for i, a := range vite {
			val, ok := "", false
			switch {
			case a == "--"+name && i+1 < len(vite):
				val, ok = vite[i+1], true
			case strings.HasPrefix(a, "--"+name+"="):
				val, ok = strings.TrimPrefix(a, "--"+name+"="), true
			}
			if ok {
				return &viteValue{known: true, value: val, file: "package.json", line: line, where: fmt.Sprintf("--%s in package.json's build script", name)}, true
			}
		}
		return nil, false
	}
	for i, a := range vite {
		switch {
		case a == "-c" || a == "--config" || strings.HasPrefix(a, "--config="):
			s.unknown = fmt.Sprintf("package.json's build script loads another config (%q)", raw)
			return s
		case !strings.HasPrefix(a, "-") && (i == 0 || !strings.HasPrefix(vite[i-1], "-") || strings.Contains(vite[i-1], "=")):
			// A positional argument is `vite build [root]`.
			s.unknown = fmt.Sprintf("package.json's build script passes `vite build` a root (%q)", raw)
			return s
		}
	}
	s.outDir, _ = flag("outDir")
	s.base, _ = flag("base")
	return s
}

// resolveOutDir is where `vite build` writes, relative to the frontend dir.
func (v *viteChecker) resolveOutDir(s viteScript) viteValue {
	if s.unknown != "" {
		return unknownValue(s.unknown)
	}
	root := viteValue{known: true, value: "."}
	if v.cfg != nil {
		root = v.cfg.topLevel("root", ".")
		if !root.known {
			return unknownValue(root.why)
		}
	}
	out := viteValue{known: true, value: "dist", where: "Vite's default — no build.outDir is set"}
	switch {
	case s.outDir != nil:
		out = *s.outDir
	case v.cfg != nil:
		out = v.cfg.buildOutDir()
		if !out.known {
			return out
		}
	}
	if path.IsAbs(out.value) || path.IsAbs(root.value) {
		return unknownValue(fmt.Sprintf("build.outDir %q (%s) is an absolute path", out.value, out.where))
	}
	if root.value != "." && root.value != "" {
		// Vite resolves outDir against its root.
		out.value = path.Join(root.value, out.value)
		out.where += fmt.Sprintf(", under root %q", root.value)
	}
	out.value = path.Clean(out.value)
	return out
}

// ── outDir vs public_dir ─────────────────────────────────────────────────

func (v *viteChecker) checkOutDir(s viteScript) {
	if v.cfg == nil && !s.present {
		v.notes = append(v.notes, "build.outDir not checked — it has no vite config and no build script")
		return
	}
	out := v.resolveOutDir(s)
	groups := groupBindings(v.fe.Bindings, func(b Binding) string { return cleanPublicDir(b.PublicDir) })
	if !out.known {
		v.notes = append(v.notes, fmt.Sprintf("build.outDir is not statically determinable: %s — make sure it builds into public_dir %s by hand", out.why, describeGroups(groups)))
		return
	}
	var ok []string
	for _, g := range groups {
		if g.key == out.value {
			ok = append(ok, g.envs...)
			continue
		}
		file, line := v.anchor(out)
		buildFix := fmt.Sprintf("`build: { outDir: %q }` in %s", g.key, v.configName())
		if out.file == "package.json" {
			buildFix = fmt.Sprintf("`--outDir %s` in %s's build script", g.key, v.rel("package.json"))
		}
		v.add(RuleViteOutDir, finding.SeverityError, file, line,
			fmt.Sprintf("frontend %q is bound to %s, which publish its public_dir %q — but `vite build` writes to %q (%s), so the deploy publishes a directory the build never wrote: missing, or stale from an older build",
				v.fe.Name, describeBindings(g.bindings), g.key, out.value, out.where),
			fmt.Sprintf("make the two agree: set public_dir = %q on frontend %q's forge.Frontend in deploy/kcl/<env>/main.k (env %s), or build into %q with %s",
				out.value, v.fe.Name, strings.Join(g.envs, ", "), g.key, buildFix))
	}
	if len(ok) > 0 {
		v.notes = append(v.notes, fmt.Sprintf("build.outDir %q (%s) matches the public_dir published in env %s", out.value, out.where, strings.Join(ok, ", ")))
	}
}

// ── base vs base_path ────────────────────────────────────────────────────

// historyRouters are client-side routers whose default mode puts the route in
// the URL path — where a relative base breaks deep links.
var historyRouters = []string{"react-router", "react-router-dom", "@tanstack/react-router", "vue-router", "wouter", "@solidjs/router", "preact-router", "svelte-routing", "@reach/router"}

func (v *viteChecker) checkBase(s viteScript) {
	var base viteValue
	switch {
	case s.unknown != "":
		base = unknownValue(s.unknown)
	case s.base != nil:
		base = *s.base
	case v.cfg != nil:
		base = v.cfg.topLevel("base", "/")
	default:
		base = viteValue{known: true, value: "/", where: "Vite's default"}
	}
	groups := groupBindings(v.fe.Bindings, func(b Binding) string { return cleanMount(b.BasePath) })
	if !base.known {
		v.notes = append(v.notes, fmt.Sprintf("base is not statically determinable: %s — make sure it matches the mount (%s) by hand", base.why, describeMounts(groups)))
		return
	}
	b := strings.TrimSpace(base.value)
	switch {
	case strings.Contains(b, "://") || strings.HasPrefix(b, "//"):
		v.notes = append(v.notes, fmt.Sprintf("base %q (%s) is an absolute URL (assets served from another origin) — not compared with the mount", b, base.where))
	case b == "" || b == "." || strings.HasPrefix(b, "./"):
		v.checkRelativeBase(base, groups)
	case strings.HasPrefix(b, "/"):
		v.checkAbsoluteBase(base, strings.TrimRight(b, "/"), groups)
	default:
		v.notes = append(v.notes, fmt.Sprintf("base %q (%s) starts with neither \"/\" nor \"./\" — not checked", b, base.where))
	}
}

func (v *viteChecker) checkAbsoluteBase(base viteValue, want string, groups []bindingGroup) {
	var ok []string
	for _, g := range groups {
		if g.key == want {
			ok = append(ok, g.envs...)
			continue
		}
		file, line := v.anchor(base)
		assets := want + "/assets/…"
		v.add(RuleViteBase, finding.SeverityError, file, line,
			fmt.Sprintf("frontend %q is mounted at %s by %s, but Vite's base is %q (%s), so every script and stylesheet URL index.html references (%s) points outside the mount and 404s — the page loads blank",
				v.fe.Name, mountLabel(g.key), describeBindings(g.bindings), base.value, base.where, assets),
			fmt.Sprintf("set `base: %q` in %s (Vite's base must equal the mount; \"/\" at the site root), or change base_path on frontend %q's forge.Frontend in deploy/kcl/<env>/main.k (env %s)",
				g.key+"/", v.configName(), v.fe.Name, strings.Join(g.envs, ", ")))
	}
	if len(ok) > 0 {
		v.notes = append(v.notes, fmt.Sprintf("base %q (%s) matches the mount in env %s", base.value, base.where, strings.Join(ok, ", ")))
	}
}

// checkRelativeBase: a relative base resolves asset URLs against the page's
// own path. On a host whose SPA fallback answers /a/b with index.html, a
// router that puts routes in the path then asks for /a/assets/…, which is a
// missing file — 404. Without such a router, or with hash routing, it works.
func (v *viteChecker) checkRelativeBase(base viteValue, groups []bindingGroup) {
	router := ""
	if v.pkg != nil {
		for _, r := range historyRouters {
			if v.pkg.depends(r) {
				router = r
				break
			}
		}
	}
	if router == "" {
		v.notes = append(v.notes, fmt.Sprintf("base %q (%s) is relative; no known client-side router is a dependency", base.value, base.where))
		return
	}
	want := "/"
	if len(groups) == 1 && groups[0].key != "" {
		want = groups[0].key + "/"
	}
	file, line := v.anchor(base)
	v.add(RuleViteBase, finding.SeverityWarning, file, line,
		fmt.Sprintf("frontend %q builds with a relative base (%q, %s) and declares %s in package.json; its routing mode could not be verified. With path-based routing, a direct visit to /a/b loads index.html but resolves relative asset URLs under /a/, where they can return 404",
			v.fe.Name, base.value, base.where, router),
		fmt.Sprintf("set `base: %q` in %s (the mount, with a trailing slash), or verify the app exclusively uses hash routing or no nested client routes", want, v.configName()))
}

// anchor is where a finding about val points: where it is set, else the
// config file, else the frontend dir.
func (v *viteChecker) anchor(val viteValue) (string, int) {
	switch {
	case val.file != "":
		return v.rel(val.file), val.line
	case v.cfg != nil:
		return v.rel(v.cfg.f.rel), v.cfg.objectLine()
	}
	return v.fe.RelDir, 0
}

// configName is the config file to name in a remedy.
func (v *viteChecker) configName() string {
	if v.cfg != nil {
		return v.rel(v.cfg.f.rel)
	}
	return v.rel("vite.config.ts")
}

// ── Bindings, grouped ────────────────────────────────────────────────────

// bindingGroup is the envs that agree on one value (a public_dir, a mount).
type bindingGroup struct {
	key      string
	envs     []string
	bindings []Binding
}

func groupBindings(bs []Binding, key func(Binding) string) []bindingGroup {
	var out []bindingGroup
	idx := map[string]int{}
	for _, b := range bs {
		k := key(b)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, bindingGroup{key: k})
		}
		out[i].envs = append(out[i].envs, b.Env)
		out[i].bindings = append(out[i].bindings, b)
	}
	return out
}

func describeGroups(gs []bindingGroup) string {
	parts := make([]string, 0, len(gs))
	for _, g := range gs {
		parts = append(parts, fmt.Sprintf("%q (env %s)", g.key, strings.Join(g.envs, ", ")))
	}
	return strings.Join(parts, ", ")
}

func describeMounts(gs []bindingGroup) string {
	parts := make([]string, 0, len(gs))
	for _, g := range gs {
		parts = append(parts, fmt.Sprintf("%s in env %s", mountLabel(g.key), strings.Join(g.envs, ", ")))
	}
	return strings.Join(parts, "; ")
}

func mountLabel(mount string) string {
	if mount == "" {
		return "the site root"
	}
	return fmt.Sprintf("base_path %q", mount)
}

// cleanPublicDir normalises a rendered public_dir; "" is the render's Vite
// default.
func cleanPublicDir(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return "dist"
	}
	return path.Clean(d)
}

// cleanMount normalises a base_path: no trailing slash, "" for the root.
func cleanMount(p string) string {
	return strings.TrimRight(strings.TrimSpace(p), "/")
}

// ── package.json ─────────────────────────────────────────────────────────

type packageJSON struct {
	raw             string
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// loadPackageJSON reads the frontend's package.json; nil when it has none.
// One that does not parse is an error: every command that builds the frontend
// would fail on it too.
func loadPackageJSON(dir string) (*packageJSON, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, nil //nolint:nilerr // no package.json: nothing to read
	}
	p := &packageJSON{raw: string(raw)}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, "package.json"), err)
	}
	return p, nil
}

func (p *packageJSON) depends(name string) bool {
	_, a := p.Dependencies[name]
	_, b := p.DevDependencies[name]
	return a || b
}

// lineOf is the 1-based line of the first match of re in package.json, 1
// when absent.
func (p *packageJSON) lineOf(re string) int {
	loc := regexp.MustCompile(re).FindStringIndex(p.raw)
	if loc == nil {
		return 1
	}
	return strings.Count(p.raw[:loc[0]], "\n") + 1
}

// ── vite.config, read conservatively ─────────────────────────────────────

type viteConfig struct {
	f *sourceFile
	// masked is f.code with every string literal's contents blanked (quotes
	// kept, same length), so brackets and keys inside strings are invisible
	// to the structure scan.
	masked string
	// strOpen maps a string's opening quote offset to its closing one.
	strOpen map[int]int
	inStr   []bool
}

// loadViteConfig reads the frontend's vite config, nil when it has none.
func loadViteConfig(dir string) *viteConfig {
	for _, name := range viteConfigNames {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return newViteConfig(newSourceFile(name, string(raw)))
		}
	}
	return nil
}

func newViteConfig(f *sourceFile) *viteConfig {
	b := []byte(f.code)
	vc := &viteConfig{f: f, strOpen: map[int]int{}, inStr: make([]bool, len(b))}
	for i := 0; i < len(b); i++ {
		q := b[i]
		if q != '"' && q != '\'' && q != '`' {
			continue
		}
		j := i + 1
		for ; j < len(b); j++ {
			if b[j] == '\\' {
				vc.inStr[j] = true
				if j+1 < len(b) {
					vc.inStr[j+1] = true
				}
				b[j] = ' '
				if j+1 < len(b) && b[j+1] != '\n' {
					b[j+1] = ' '
				}
				j++
				continue
			}
			if b[j] == q || (q != '`' && b[j] == '\n') {
				break
			}
			vc.inStr[j] = true
			if b[j] != '\n' {
				b[j] = ' '
			}
		}
		vc.strOpen[i] = j
		i = j
	}
	vc.masked = string(b)
	return vc
}

// objClass is what kind of object literal a `{` opens.
type objClass struct {
	// kind: "top" (the config object, or an arm of it), "key" (the value of
	// the property `name`), or "other" (a call argument, an array element, a
	// function body — not the config).
	kind string
	name string
}

var (
	identTailRE = regexp.MustCompile(`[A-Za-z_$][\w$]*$`)
	// topAssignRE: a variable the config is exported or returned through.
	topRefRE = `(?:export\s+default|defineConfig\s*\(|module\.exports\s*=|return)\s*%s\b`
)

// container is the offset of the innermost unclosed bracket before off, and
// the bracket; -1 when there is none.
func (vc *viteConfig) container(off int) (int, byte) {
	depth := 0
	for i := off - 1; i >= 0; i-- {
		switch vc.masked[i] {
		case ')', ']', '}':
			depth++
		case '(', '[', '{':
			if depth == 0 {
				return i, vc.masked[i]
			}
			depth--
		}
	}
	return -1, 0
}

// classify says what the object literal opened at brace is.
func (vc *viteConfig) classify(brace int) objClass {
	before := strings.TrimRight(vc.masked[:brace], " \t\r\n")
	switch {
	case before == "":
		return objClass{kind: "top"}
	case strings.HasSuffix(before, ":"):
		rest := strings.TrimRight(before[:len(before)-1], " \t\r\n")
		name := identTailRE.FindString(rest)
		prev := ""
		switch {
		case name != "":
			prev = strings.TrimRight(rest[:len(rest)-len(name)], " \t\r\n")
		case strings.HasSuffix(rest, `"`) || strings.HasSuffix(rest, `'`):
			for open, end := range vc.strOpen {
				if end == len(rest)-1 {
					name = vc.f.code[open+1 : end]
					prev = strings.TrimRight(rest[:open], " \t\r\n")
				}
			}
		case strings.HasSuffix(rest, "]"):
			return objClass{kind: "key", name: "[computed]"}
		}
		if name != "" && (strings.HasSuffix(prev, "{") || strings.HasSuffix(prev, ",")) {
			return objClass{kind: "key", name: name}
		}
		// The else arm of a ternary: `cond ? a : {`.
		return objClass{kind: "top"}
	case strings.HasSuffix(before, "?"):
		return objClass{kind: "top"}
	case strings.HasSuffix(before, "=>"):
		// An arrow function BODY, not an object.
		return objClass{kind: "other"}
	case strings.HasSuffix(before, "("):
		rest := strings.TrimRight(before[:len(before)-1], " \t\r\n")
		switch {
		case strings.HasSuffix(rest, "=>"), strings.HasSuffix(rest, "="), identTailRE.FindString(rest) == "defineConfig",
			identTailRE.FindString(rest) == "return", identTailRE.FindString(rest) == "default":
			return objClass{kind: "top"}
		}
		return objClass{kind: "other"}
	case identTailRE.FindString(before) == "return", identTailRE.FindString(before) == "default":
		return objClass{kind: "top"}
	case strings.HasSuffix(before, "=") && !strings.HasSuffix(before, "==") && !strings.HasSuffix(before, "!=") && !strings.HasSuffix(before, "<=") && !strings.HasSuffix(before, ">="):
		lhs := strings.TrimRight(before[:len(before)-1], " \t\r\n")
		if strings.HasSuffix(lhs, "module.exports") {
			return objClass{kind: "top"}
		}
		// `const config: UserConfig = {` — the config only when the variable
		// is what the file exports or returns.
		if m := regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?::\s*[\w$.<>\[\]]+\s*)?$`).FindStringSubmatch(lhs); m != nil {
			if regexp.MustCompile(fmt.Sprintf(topRefRE, regexp.QuoteMeta(m[1]))).MatchString(vc.masked) {
				return objClass{kind: "top"}
			}
		}
		return objClass{kind: "other"}
	}
	return objClass{kind: "other"}
}

// occurrence is one place a key is set.
type occurrence struct {
	class   objClass
	line    int
	literal bool
	value   string
	// objValue: the value is an object literal (`build: {`).
	objValue bool
}

// occurrences finds every object property named key: its enclosing object's
// class, and its value when that is a plain string literal.
func (vc *viteConfig) occurrences(key string) []occurrence {
	var out []occurrence
	re := regexp.MustCompile(`["'` + "`" + `]?\b` + regexp.QuoteMeta(key) + `\b["'` + "`" + `]?`)
	for _, loc := range re.FindAllStringIndex(vc.f.code, -1) {
		start, end := loc[0], loc[1]
		tok := vc.f.code[start:end]
		quoted := tok[0] == '"' || tok[0] == '\'' || tok[0] == '`'
		if quoted {
			// A quoted key: the quote must open a string that is exactly it.
			if close, ok := vc.strOpen[start]; !ok || close != end-1 {
				continue
			}
		} else if vc.inStr[start] || strings.ContainsAny(tok[len(tok)-1:], `"'`+"`") {
			continue
		}
		if p := strings.TrimRight(vc.masked[:start], " \t\r\n"); strings.HasSuffix(p, ".") {
			continue // member access: config.build.outDir
		}
		open, br := vc.container(start)
		if open < 0 || br != '{' {
			continue
		}
		after := strings.TrimLeft(vc.masked[end:], " \t\r\n")
		occ := occurrence{class: vc.classify(open), line: vc.f.lineAt(start)}
		switch {
		case strings.HasPrefix(after, ":"):
			valOff := end + (len(vc.masked[end:]) - len(after)) + 1
			occ.literal, occ.value = vc.literalAt(valOff)
			occ.objValue = strings.HasPrefix(strings.TrimLeft(vc.masked[valOff:], " \t\r\n"), "{")
		case strings.HasPrefix(after, ",") || strings.HasPrefix(after, "}"):
			// shorthand `{ outDir }`: a variable.
		default:
			continue // not a property
		}
		out = append(out, occ)
	}
	return out
}

// literalAt reads a plain string literal value starting at off (after the
// colon) that is the WHOLE value: followed only by `,`, `}` or a newline.
func (vc *viteConfig) literalAt(off int) (bool, string) {
	for off < len(vc.masked) && (vc.masked[off] == ' ' || vc.masked[off] == '\t') {
		off++
	}
	close, ok := vc.strOpen[off]
	if !ok || close >= len(vc.f.code) {
		return false, ""
	}
	val := vc.f.code[off+1 : close]
	if vc.f.code[off] == '`' && strings.Contains(val, "${") {
		return false, ""
	}
	rest := strings.TrimLeft(vc.masked[close+1:], " \t\r")
	if rest != "" && rest[0] != ',' && rest[0] != '}' && rest[0] != '\n' {
		return false, ""
	}
	return true, val
}

// resolve folds a key's occurrences into one value.
func (vc *viteConfig) resolve(label string, occs []occurrence, def, defWhere, indirect string) viteValue {
	var lit *occurrence
	for i := range occs {
		o := occs[i]
		if !o.literal {
			return unknownValue(fmt.Sprintf("%s at %s:%d is computed, not a string literal", label, vc.f.rel, o.line))
		}
		if lit != nil && lit.value != o.value {
			return unknownValue(fmt.Sprintf("%s is set to different values (%s:%d %q, %s:%d %q)", label, vc.f.rel, lit.line, lit.value, vc.f.rel, o.line, o.value))
		}
		if lit == nil {
			lit = &occs[i]
		}
	}
	if indirect != "" {
		return unknownValue(indirect)
	}
	if lit != nil {
		return viteValue{known: true, value: lit.value, file: vc.f.rel, line: lit.line, where: fmt.Sprintf("%s at %s:%d", label, vc.f.rel, lit.line)}
	}
	if !vc.hasTopObject() {
		return unknownValue(fmt.Sprintf("%s has no config object literal this check can read", vc.f.rel))
	}
	return viteValue{known: true, value: def, where: defWhere}
}

// topLevel is a top-level config key (base, root).
func (vc *viteConfig) topLevel(key, def string) viteValue {
	var occs []occurrence
	ambiguous := 0
	for _, o := range vc.occurrences(key) {
		switch o.class.kind {
		case "top":
			occs = append(occs, o)
		case "other":
			ambiguous = o.line
		}
	}
	indirect := vc.indirect(func(c objClass) bool { return c.kind == "top" })
	if indirect == "" && len(occs) == 0 && ambiguous > 0 {
		indirect = fmt.Sprintf("%s:%d sets a %q inside an object this check cannot place (a call argument?)", vc.f.rel, ambiguous, key)
	}
	return vc.resolve(key, occs, def, fmt.Sprintf("Vite's default — %s sets no %s", vc.f.rel, key), indirect)
}

// buildOutDir is build.outDir.
func (vc *viteConfig) buildOutDir() viteValue {
	var occs []occurrence
	for _, o := range vc.occurrences("outDir") {
		if o.class.kind == "key" && o.class.name == "build" {
			occs = append(occs, o)
		}
	}
	indirect := vc.indirect(func(c objClass) bool {
		return c.kind == "top" || (c.kind == "key" && c.name == "build")
	})
	if indirect == "" {
		// `build: someOptions` — not an object literal this check can read.
		for _, o := range vc.occurrences("build") {
			if o.class.kind == "top" && !o.objValue {
				indirect = fmt.Sprintf("build at %s:%d is not an object literal", vc.f.rel, o.line)
				break
			}
		}
	}
	return vc.resolve("build.outDir", occs, "dist", fmt.Sprintf("Vite's default — %s sets no build.outDir", vc.f.rel), indirect)
}

// indirect names a construct that could set a key without spelling it: a
// mergeConfig call, or a spread directly inside an object `in` accepts.
func (vc *viteConfig) indirect(in func(objClass) bool) string {
	if loc := regexp.MustCompile(`\bmergeConfig\s*\(`).FindStringIndex(vc.masked); loc != nil {
		return fmt.Sprintf("%s:%d merges configs (mergeConfig), which may set it", vc.f.rel, vc.f.lineAt(loc[0]))
	}
	for _, loc := range regexp.MustCompile(`\.\.\.`).FindAllStringIndex(vc.masked, -1) {
		open, br := vc.container(loc[0])
		if open >= 0 && br == '{' && in(vc.classify(open)) {
			return fmt.Sprintf("the spread at %s:%d may set it", vc.f.rel, vc.f.lineAt(loc[0]))
		}
	}
	return ""
}

// hasTopObject reports whether the file holds a config object literal at all
// — else an absent key proves nothing (the config may come from elsewhere).
func (vc *viteConfig) hasTopObject() bool {
	return vc.objectLine() > 0
}

// objectLine is the line of the first config object literal, 0 when none.
func (vc *viteConfig) objectLine() int {
	for i := 0; i < len(vc.masked); i++ {
		if vc.masked[i] == '{' && vc.classify(i).kind == "top" {
			return vc.f.lineAt(i)
		}
	}
	return 0
}
