package vulnpatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

// The Python patcher. A finding for a PyPI package resolves to the
// requirements files, poetry.lock and uv.lock files that pin it, and each is
// bumped its own way:
//
//   - a requirements file kept by hand: its "pkg==X" pin rewritten in place;
//   - a compiled one (a pip-compile or `uv pip compile` header, or a sibling
//     .in): the command its header records rerun with --upgrade-package
//     pkg==X, after raising a "pkg==X" pin in its .in when the .in pins it
//     directly; a transitive dependency moves through that constraint alone;
//   - poetry.lock: `poetry update pkg` when every range pyproject.toml
//     declares for it already admits the fix (a transitive dependency
//     always), else `poetry add pkg@^X`;
//   - uv.lock: `uv lock --upgrade-package pkg==X`.
//
// Ambiguity is skipped with the reason, for the agent: older pins in more
// than one manifest, a range the tool must not widen on its own, a hashed
// file with nothing to regenerate its hashes, a pin that is not a pin.

func wantsAnyPython(o Options) bool {
	return o.wants(EcosystemPip) || o.wants(EcosystemPoetry) || o.wants(EcosystemUV)
}

// pyBump is a Python bump's manifest, and the .in pins it raises first.
type pyBump struct {
	m       *pyManifest
	inEdits []pyInEdit
}

// pyInEdit is a "pkg==from" pin in a compiled file's .in source.
type pyInEdit struct{ file, from string }

// resolvePython ties a PyPI package to the manifests that pin it at the
// scanned version, and only those, as resolveJS does for npm: a manifest
// that pins another version is not evidence that it ships the vulnerable
// copy. When the finding names where it was found (a scan of this tree),
// only the manifests it names are candidates.
//
// Every candidate must belong to one tool: a requirements file beside a
// poetry.lock or uv.lock that pins the same version (an exported
// requirements.txt, say) leaves which one builds the artifact ambiguous, and
// the finding is skipped. Several manifests of one tool that each pin the
// scanned version are all bumped, or, when one of them cannot take the bump,
// none is.
//
// It reports false when no manifest of a wanted ecosystem is involved (the
// finding is left out of the plan).
func (p *Plan) resolvePython(r *Result, f vulnscan.Finding, idx *index, opts Options, at int) bool {
	name, installed := pyName(f.Package.Name), f.Package.Version
	var ms []*pyManifest
	for _, m := range idx.python {
		if opts.wants(m.eco) {
			ms = append(ms, m)
		}
	}
	if len(opts.Ecosystems) > 0 && len(ms) == 0 {
		return false
	}
	ms = hinted(ms, f.Locations, func(m *pyManifest) string { return m.rel })
	var exact, present []*pyManifest
	for _, m := range ms {
		if len(m.pins[name]) == 0 {
			continue
		}
		present = append(present, m)
		if m.pinsAt(name, installed) {
			exact = append(exact, m)
		}
	}
	switch {
	case len(present) == 0:
		if len(opts.Ecosystems) > 0 {
			return false
		}
		r.Reason = pyNowhere(name, ms, idx)
		return true
	case len(exact) == 0:
		var pins []string
		for _, m := range present {
			pins = append(pins, fmt.Sprintf("%s pins %s", m.rel, strings.Join(m.pins[name], ", ")))
		}
		sort.Strings(pins)
		r.Reason = fmt.Sprintf("no Python manifest pins the scanned %s %s (%s): which copy the finding is about is not known", name, installed, strings.Join(pins, "; "))
		return true
	}
	byEco := map[Ecosystem][]string{}
	for _, m := range exact {
		byEco[m.eco] = append(byEco[m.eco], m.rel)
	}
	if len(byEco) > 1 {
		var parts []string
		for _, e := range Ecosystems() {
			if rs := byEco[e]; len(rs) > 0 {
				parts = append(parts, fmt.Sprintf("%s: %s", e, strings.Join(rs, ", ")))
			}
		}
		r.Reason = fmt.Sprintf("%s %s is pinned by the manifests of several package managers (%s): which one builds the scanned artifact is ambiguous", name, installed, strings.Join(parts, "; "))
		return true
	}
	var bumps []*plannedBump
	for _, m := range exact {
		b, why := pythonBump(m, name, installed, r.Target)
		if why != "" {
			r.Manifests, r.Reason = nil, m.rel+": "+why
			return true
		}
		r.Manifests = append(r.Manifests, m.rel)
		bumps = append(bumps, b)
	}
	// Planned only once every manifest can take the bump: a finding is
	// bumped everywhere it is pinned, or nowhere.
	for _, b := range bumps {
		p.plan(b, at)
	}
	r.Ecosystem = exact[0].eco
	r.Status, r.Reason = StatusBumped, ""
	return true
}

// pyNowhere says why a package no manifest pins has nothing to bump.
func pyNowhere(name string, ms []*pyManifest, idx *index) string {
	for _, m := range ms {
		req, ok := m.reqs[name]
		switch {
		case !ok:
		case req.url:
			return fmt.Sprintf("%s names %s by URL: there is no version to raise", m.rel, name)
		case req.spec == "":
			return fmt.Sprintf("%s names %s with no version: nothing to rewrite (a rebuild resolves it afresh; pinning it is a choice)", m.rel, name)
		default:
			return fmt.Sprintf("%s names %s as %q, not a pin: nothing to rewrite (a rebuild resolves it afresh; raising its floor is a choice)", m.rel, name, req.spec)
		}
	}
	if len(idx.pyOther) > 0 {
		return fmt.Sprintf("%s is in no requirements file, poetry.lock or uv.lock in the tree, and %s has no patcher", name, strings.Join(idx.pyOther, ", "))
	}
	return fmt.Sprintf("%s is in no Python manifest in the tree (requirements*.txt, poetry.lock, uv.lock)", name)
}

// pythonBump plans name from -> target in m, or says why m cannot take it.
func pythonBump(m *pyManifest, name, from, target string) (*plannedBump, string) {
	b := &plannedBump{eco: m.eco, manifest: m.rel, dir: m.dir, pkg: name, from: from, to: target, rank: m.rank, py: &pyBump{m: m}}
	switch m.eco {
	case EcosystemPip:
		if m.compile == nil {
			if m.noRegen != "" {
				return nil, "it is compiled, but " + m.noRegen
			}
			if m.hashed {
				return nil, "it pins with --hash, and no pip-compile header or sibling .in says how to regenerate the hashes"
			}
			if why := checkPinEdit(filepath.Join(m.root, filepath.FromSlash(m.rel)), "it", name, from); why != "" {
				return nil, why
			}
			b.direct = true
			return b, ""
		}
		for _, src := range m.compile.sources {
			rel := relTo(m.root, src)
			if filepath.Base(src) == "pyproject.toml" {
				decls, err := readPyProject(src)
				if err != nil {
					continue // the compile reports it
				}
				for _, d := range decls[name] {
					b.direct = true
					if ok, known := admitsPEP440(d.constraint, target); known && !ok {
						return nil, fmt.Sprintf("%s %s constrains %s to %q, which excludes the fix %s: raising the constraint is a choice", rel, d.section, name, d.constraint, target)
					}
				}
				continue
			}
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			reqs, _, _, _ := parseRequirements(data)
			for _, req := range reqs {
				if req.name != name {
					continue
				}
				b.direct = true
				if v, ok := req.pin(); ok {
					if comparePyVersions(v, target) < 0 {
						if why := checkPinEdit(src, rel, name, v); why != "" {
							return nil, why
						}
						b.py.inEdits = append(b.py.inEdits, pyInEdit{file: src, from: v})
					}
					continue
				}
				if ok, known := admitsPEP440(req.spec, target); known && !ok {
					return nil, fmt.Sprintf("%s constrains %s to %q, which excludes the fix %s: raising the constraint is a choice", rel, name, req.spec, target)
				}
			}
		}
		return b, ""
	case EcosystemPoetry, EcosystemUV:
		decls := m.direct[name]
		b.direct = len(decls) > 0
		for _, d := range decls {
			admits, known := admitsDecl(d, target)
			if m.eco == EcosystemUV && known && !admits {
				return nil, fmt.Sprintf("pyproject.toml %s constrains %s to %q, which excludes the fix %s: raising the constraint is a choice (uv add)", d.section, name, d.constraint, target)
			}
			if m.eco == EcosystemPoetry && !(known && admits) {
				if why := poetryAddable(name, decls); why != "" {
					return nil, why
				}
			}
		}
	}
	return b, ""
}

func admitsDecl(d pyDecl, version string) (admits, known bool) {
	if d.poetry {
		return admitsPoetry(d.constraint, version)
	}
	return admitsPEP440(d.constraint, version)
}

// poetryAddable says why `poetry add` must not rewrite name's declaration
// ("" when it may).
func poetryAddable(name string, decls []pyDecl) string {
	if len(decls) > 1 {
		var where []string
		for _, d := range decls {
			where = append(where, d.section)
		}
		return fmt.Sprintf("pyproject.toml declares %s in %d places (%s): which one to raise is ambiguous", name, len(decls), strings.Join(where, ", "))
	}
	if d := decls[0]; d.addable != "" {
		return fmt.Sprintf("pyproject.toml %s constrains %s to %q, which excludes the fix, and poetry add would rewrite the declaration: %s", d.section, name, d.constraint, d.addable)
	}
	return ""
}

// applyPython bumps every package of one Python manifest; bs share it.
func applyPython(ctx context.Context, run vulnscan.Runner, bs []*plannedBump) (cmds [][]string, edits []string, err error) {
	cmds = [][]string{}
	m := bs[0].py.m
	exec := func(dir string, argv ...string) error {
		cmds = append(cmds, argv)
		var out bytes.Buffer
		err := run(ctx, vulnscan.Command{Path: argv[0], Args: argv[1:], Dir: dir, Stdout: &out, Stderr: &out})
		if err != nil {
			return fmt.Errorf("%s: %v\n%s", strings.Join(argv, " "), err, Tail(out.String(), 2000))
		}
		return nil
	}
	rewrite := func(file, name, from, to string) error {
		n, err := rewritePins(file, name, from, to)
		if err != nil {
			return fmt.Errorf("rewrite %s: %w", relTo(m.root, file), err)
		}
		if n == 0 {
			return fmt.Errorf("%s has no %s==%s pin to rewrite", relTo(m.root, file), name, from)
		}
		edits = append(edits, fmt.Sprintf("%s: %s==%s -> %s==%s", relTo(m.root, file), name, from, name, to))
		return nil
	}
	switch m.eco {
	case EcosystemPip:
		if m.compile == nil {
			for _, b := range bs {
				if err := rewrite(filepath.Join(m.root, filepath.FromSlash(m.rel)), b.pkg, b.from, b.to); err != nil {
					return cmds, edits, err
				}
			}
			return cmds, edits, nil
		}
		argv := append([]string{}, m.compile.argv...)
		for _, b := range bs {
			for _, e := range b.py.inEdits {
				if err := rewrite(e.file, b.pkg, e.from, b.to); err != nil {
					return cmds, edits, err
				}
			}
			argv = append(argv, "--upgrade-package", b.pkg+"=="+b.to)
		}
		return cmds, edits, exec(m.compile.runDir, argv...)
	case EcosystemPoetry:
		adds := map[string][]string{} // group -> specs
		var updates []string
		for _, b := range bs {
			decls := m.direct[b.pkg]
			add := false
			for _, d := range decls {
				if ok, known := admitsDecl(d, b.to); !(ok && known) {
					add = true
				}
			}
			if !add {
				updates = append(updates, b.pkg)
				continue
			}
			if why := poetryAddable(b.pkg, decls); why != "" {
				return cmds, edits, fmt.Errorf("%s", why)
			}
			spec := b.pkg
			if x := decls[0].extras; len(x) > 0 {
				spec += "[" + strings.Join(x, ",") + "]"
			}
			adds[decls[0].group] = append(adds[decls[0].group], spec+"@^"+b.to)
		}
		groups := make([]string, 0, len(adds))
		for g := range adds {
			groups = append(groups, g)
		}
		sort.Strings(groups)
		for _, g := range groups {
			argv := []string{"poetry", "add"}
			if g != "" {
				argv = append(argv, "--group", g)
			}
			if err := exec(m.dir, append(argv, adds[g]...)...); err != nil {
				return cmds, edits, err
			}
		}
		if len(updates) > 0 {
			return cmds, edits, exec(m.dir, append([]string{"poetry", "update"}, updates...)...)
		}
		return cmds, edits, nil
	case EcosystemUV:
		argv := []string{"uv", "lock"}
		for _, b := range bs {
			argv = append(argv, "--upgrade-package", b.pkg+"=="+b.to)
		}
		return cmds, edits, exec(m.dir, argv...)
	}
	return cmds, edits, fmt.Errorf("no Python patcher for %s", m.eco)
}

// pinLine is a requirement line that pins one version: indent, name,
// extras, "==" or "===", the version, and the rest (markers, options,
// comment).
var pinLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9][A-Za-z0-9._-]*)(\s*\[[^\]]*\])?(\s*)(===?)(\s*)([^\s;#,\\]+)(.*)$`)

// rewritePins rewrites every "name==from" pin in a requirements-format file
// to "name==to", leaving the rest of each line (markers, hashes, comments)
// and every other line as they are. It reports how many it rewrote.
func rewritePins(file, name, from, to string) (int, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	out, n := editPins(data, name, from, to)
	if n == 0 {
		return 0, nil
	}
	// An existing file keeps its permission bits (atomicfile copies them).
	return n, atomicfile.WriteFile(file, out, 0o644)
}

// editPins is rewritePins on data: the edited bytes, and how many pins it
// rewrote.
func editPins(data []byte, name, from, to string) ([]byte, int) {
	lines := strings.Split(string(data), "\n")
	n := 0
	for i, line := range lines {
		g := pinLine.FindStringSubmatch(line)
		if len(g) < 9 || pyName(g[2]) != name || !pyEqual(g[7], from) || strings.HasPrefix(strings.TrimSpace(g[8]), ",") {
			continue
		}
		lines[i] = g[1] + g[2] + g[3] + g[4] + g[5] + g[6] + to + g[8]
		n++
	}
	return []byte(strings.Join(lines, "\n")), n
}

// checkPinEdit says why forge must not rewrite file's "name==from" pins
// itself ("" when it may): forge rewrites only a file it read in full, and
// only when the line-wise edit finds every pin the parse did. A line it
// could not read as a requirement (pip would read it some other way, or
// refuse the file), or a pin split across continuation lines, refuses it.
func checkPinEdit(file, rel, name, from string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Sprintf("%s cannot be read: %v", rel, err)
	}
	reqs, _, _, unread := parseRequirements(data)
	if len(unread) > 0 {
		return fmt.Sprintf("%s has a line forge does not read as a requirement (%q): it rewrites only a file it fully understands", rel, unread[0])
	}
	want := 0
	for _, r := range reqs {
		if v, ok := r.pin(); ok && r.name == name && pyEqual(v, from) {
			want++
		}
	}
	if _, got := editPins(data, name, from, from); got != want {
		return fmt.Sprintf("%s pins %s==%s %d time(s), and a line-wise rewrite finds %d: it is not rewritten by hand", rel, name, from, want, got)
	}
	return ""
}
