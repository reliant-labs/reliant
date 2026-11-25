package opindex

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Why a change reaches an op: [Reason.Via].
const (
	// ViaMethod: the changed function is the method implementing a
	// component op.
	ViaMethod = "method"
	// ViaHandler: the changed function is an rpc's handler method, or a
	// function in the handler's own file that the handler reaches.
	ViaHandler = "handler"
	// ViaHelper: the changed declaration is reached from the op's entry
	// method through the package's static call graph.
	ViaHelper = "helper"
	// ViaPackage: nothing in the package reaches the changed declaration
	// (a constructor, an init, a func only other packages call), so every
	// op of the package is named.
	ViaPackage = "package"
	// ViaProto: the rpc's declaration, or its request or response message,
	// changed in a .proto file.
	ViaProto = "proto"
)

// Unattributed reasons with fixed text. Others name their subject
// (`package internal/util has no ops …`, `message Foo changed; …`).
const (
	ReasonGenerated     = "generated"
	ReasonTestFile      = "test file"
	ReasonNotGo         = "not Go source"
	ReasonNoDeclChange  = "only comments, imports or blank lines changed"
	ReasonRenamedOnly   = "renamed without content changes"
	ReasonProtoNoDecl   = "proto change outside rpc and message declarations (imports, options, enums, comments)"
	noOpsReasonTemplate = "package %s has no ops (no generated component decorator, not a handler package)"
)

// Reason is one changed declaration that reaches an op.
type Reason struct {
	// Via is one of the Via* constants.
	Via string `json:"via"`
	// Function is the changed declaration as Go spells it: "(*service).Send",
	// "helper", "type service", "var maxRetries"; "rpc TodoService.CreateTodo"
	// or "message CreateTodoRequest" for a proto change. Empty when the
	// file did not parse and the whole package is named.
	Function string `json:"function,omitempty"`
	// File and Line locate the declaration, on the head side when it still
	// exists there and on the base side when the change deleted it.
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
}

// Commit is one commit of a mapped range. The working tree's uncommitted
// changes are the pseudo-commit with SHA "" and Subject "uncommitted changes".
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	// PR is the pull request number a squash-merge subject ends with
	// ("… (#123)"); 0 when it has none. See [ParsePR].
	PR int `json:"pr,omitempty"`
}

// UncommittedSubject is the subject of the working tree's pseudo-commit.
const UncommittedSubject = "uncommitted changes"

// MappedOp is an op a diff reaches, why, and which commits did it.
type MappedOp struct {
	Op
	// Removed: the op is not in the index — the diff (or a later change)
	// deleted its rpc from the working tree the index was built from, or
	// its .proto lies outside proto/. Op is then read from the diff's own
	// proto: no handler, Package empty.
	Removed bool `json:"removed,omitempty"`
	// Reasons are sorted by file, line, via and function.
	Reasons []Reason `json:"reasons"`
	// Commits that changed the op, newest first (the uncommitted
	// pseudo-commit first of all). Empty when the mapping was not of a
	// commit range ([Index.MapChanges]).
	Commits []Commit `json:"commits,omitempty"`
}

// Unattributed is a changed file no op is mapped from, and why.
type Unattributed struct {
	File    string   `json:"file"`
	Reason  string   `json:"reason"`
	Commits []Commit `json:"commits,omitempty"`
}

// Mapping is a diff mapped to the ops it reaches. Every changed file
// appears either in some op's Reasons or in Unattributed, so nothing a diff
// touches is silently dropped.
type Mapping struct {
	// Base and Head are the resolved commits mapped; Head is "" for the
	// working tree.
	Base string `json:"base"`
	Head string `json:"head"`
	// Commits are the commits mapped, newest first. Merge commits are left
	// out: each side's commits are mapped on their own.
	Commits []Commit `json:"commits"`
	// Total is how many non-merge commits base..head holds; it exceeds
	// len(Commits) (less the uncommitted pseudo-commit) when Truncated.
	Total int `json:"total"`
	// Truncated says only the newest release.MaxRangeCommits commits were
	// mapped: an op changed only by an older commit is missing.
	Truncated    bool           `json:"truncated,omitempty"`
	Ops          []MappedOp     `json:"ops"`
	Unattributed []Unattributed `json:"unattributed"`
}

// ReadFunc returns a file's content on one side of a diff, by
// project-relative path. A nil slice and nil error mean the file does not
// exist on that side.
type ReadFunc func(path string) ([]byte, error)

// MapChanges maps changes to the ops they reach. head and base read each
// file's content on either side; nothing else is read, so a caller can map
// a synthetic diff. The result has no Base, Head or Commits.
func (ix *Index) MapChanges(changes []FileChange, head, base ReadFunc) (Mapping, error) {
	acc := newAccum()
	if err := ix.mapInto(acc, changes, head, base, nil); err != nil {
		return Mapping{}, err
	}
	return acc.mapping(), nil
}

// mapInto maps changes into acc, crediting commit c (nil: no commit).
func (ix *Index) mapInto(acc *accum, changes []FileChange, head, base ReadFunc, c *Commit) error {
	for _, fc := range changes {
		p := fc.Path
		var err error
		switch {
		case isGeneratedPath(p):
			acc.unattributed(p, ReasonGenerated, c)
		case strings.HasSuffix(p, "_test.go"):
			acc.unattributed(p, ReasonTestFile, c)
		case strings.HasSuffix(p, ".go"):
			err = ix.mapGo(acc, fc, head, base, c)
		case strings.HasSuffix(p, ".proto"):
			err = ix.mapProto(acc, fc, head, base, c)
		default:
			acc.unattributed(p, ReasonNotGo, c)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// newSide returns fc's head side to read: the path, the ranges that changed
// it, whether the whole file is the change (an addition with no hunks, as
// git reports an empty file), and false when there is no head side.
func (fc FileChange) newSide() (string, []LineRange, bool, bool) {
	if fc.Status == "D" {
		return "", nil, false, false
	}
	return fc.Path, fc.NewRanges, fc.Status == "A" && len(fc.NewRanges) == 0, true
}

// oldSide is newSide for the base side.
func (fc FileChange) oldSide() (string, []LineRange, bool, bool) {
	if fc.Status == "A" {
		return "", nil, false, false
	}
	old := fc.Path
	if fc.OldPath != "" {
		old = fc.OldPath
	}
	// A base-side point is an insertion on the head side, where the head
	// declaration containing it is what changed.
	var ranges []LineRange
	for _, r := range fc.OldRanges {
		if r.Count > 0 {
			ranges = append(ranges, r)
		}
	}
	return old, ranges, fc.Status == "D" && len(fc.OldRanges) == 0, true
}

func touchesAny(ranges []LineRange, start, end int) bool {
	for _, r := range ranges {
		if r.touches(start, end) {
			return true
		}
	}
	return false
}

func pkgDirOf(file string) string { return path.Dir(file) }

// mapGo maps one changed Go file: its changed declarations on both sides,
// each to the ops of its package that reach it.
func (ix *Index) mapGo(acc *accum, fc FileChange, head, base ReadFunc, c *Commit) error {
	type changedDecl struct {
		pkg, file string
		d         goDecl
	}
	var (
		changed []changedDecl
		seen    = map[string]bool{}
		broken  []string // files that did not parse
	)
	collect := func(file string, ranges []LineRange, whole bool, read ReadFunc) error {
		src, err := read(file)
		if err != nil {
			return fmt.Errorf("opindex: read %s: %w", file, err)
		}
		if src == nil {
			return nil
		}
		_, _, decls, perr := parseGoDecls(file, src)
		if perr != nil {
			broken = append(broken, file)
			return nil
		}
		pkg := pkgDirOf(file)
		for _, d := range decls {
			if !whole && !touchesAny(ranges, d.Start, d.End) {
				continue
			}
			// The head side is read first, so a declaration changed on
			// both sides is located where it lives now.
			if k := pkg + "\x00" + d.Key; !seen[k] {
				seen[k] = true
				changed = append(changed, changedDecl{pkg, file, d})
			}
		}
		return nil
	}
	if file, ranges, whole, ok := fc.newSide(); ok {
		if err := collect(file, ranges, whole, head); err != nil {
			return err
		}
	}
	if file, ranges, whole, ok := fc.oldSide(); ok {
		if err := collect(file, ranges, whole, base); err != nil {
			return err
		}
	}

	for _, file := range broken {
		// A file that does not parse has no declarations to intersect: name
		// every op of its package rather than none.
		p := ix.pkgs[pkgDirOf(file)]
		if p == nil {
			acc.unattributed(file, fmt.Sprintf(noOpsReasonTemplate, pkgDirOf(file)), c)
			continue
		}
		for _, name := range p.ops {
			acc.attribute(ix.ops[name], false, Reason{Via: ViaPackage, File: file}, c)
		}
	}
	if len(changed) == 0 && len(broken) == 0 {
		reason := ReasonNoDeclChange
		if (fc.Status == "R" || fc.Status == "C") && len(fc.NewRanges) == 0 && len(fc.OldRanges) == 0 {
			reason = ReasonRenamedOnly
		}
		acc.unattributed(fc.Path, reason, c)
		return nil
	}
	for _, cd := range changed {
		p := ix.pkgs[cd.pkg]
		if p == nil {
			acc.unattributed(cd.file, fmt.Sprintf(noOpsReasonTemplate, cd.pkg), c)
			continue
		}
		ix.attributeDecl(acc, p, cd.file, cd.d, c)
	}
	return nil
}

// attributeDecl names the ops of p that a change to d reaches.
func (ix *Index) attributeDecl(acc *accum, p *pkgIndex, file string, d goDecl, c *Commit) {
	entry := p.entry[d.Key]
	for _, name := range entry {
		op := ix.ops[name]
		via := ViaMethod
		if op.Kind == KindRPC {
			via = ViaHandler
		}
		acc.attribute(op, false, Reason{Via: via, Function: d.Display, File: file, Line: d.Start}, c)
	}
	helpers := 0
	for _, name := range p.reach[d.Key] {
		if contains(entry, name) {
			continue
		}
		op := ix.ops[name]
		via := ViaHelper
		if op.Kind == KindRPC && op.File == file {
			via = ViaHandler // rpc_<name>.go: the handler's own file
		}
		acc.attribute(op, false, Reason{Via: via, Function: d.Display, File: file, Line: d.Start}, c)
		helpers++
	}
	if len(entry) > 0 || helpers > 0 {
		return
	}
	for _, name := range p.ops {
		acc.attribute(ix.ops[name], false, Reason{Via: ViaPackage, Function: d.Display, File: file, Line: d.Start}, c)
	}
}

// mapProto maps one changed .proto file: a changed rpc declaration is its
// op; a changed message reaches every rpc taking or returning it, in this
// file and (by fully qualified name) across the project's protos.
func (ix *Index) mapProto(acc *accum, fc FileChange, head, base ReadFunc, c *Commit) error {
	touched := false
	side := func(file string, ranges []LineRange, whole bool, read ReadFunc) error {
		src, err := read(file)
		if err != nil {
			return fmt.Errorf("opindex: read %s: %w", file, err)
		}
		if src == nil {
			return nil
		}
		pf := parseProto(src)
		for _, r := range pf.RPCs {
			if whole || touchesAny(ranges, r.Line, r.End) {
				touched = true
				ix.attributeRPC(acc, pf, file, r, Reason{Via: ViaProto, Function: "rpc " + r.Service + "." + r.Name, File: file, Line: r.Line}, c)
			}
		}
		for _, m := range pf.Messages {
			if !whole && !touchesAny(ranges, m.Line, m.End) {
				continue
			}
			touched = true
			fq := qualify(pf.Package, m.Name)
			reason := Reason{Via: ViaProto, Function: "message " + m.Name, File: file, Line: m.Line}
			used := false
			for _, r := range pf.RPCs {
				if qualify(pf.Package, r.In) == fq || qualify(pf.Package, r.Out) == fq {
					ix.attributeRPC(acc, pf, file, r, reason, c)
					used = true
				}
			}
			for _, name := range ix.rpcsByMessage[fq] {
				acc.attribute(ix.ops[name], false, reason, c)
				used = true
			}
			if !used {
				acc.unattributed(file, fmt.Sprintf("message %s changed; no rpc takes or returns it directly (nested use is not followed)", m.Name), c)
			}
		}
		return nil
	}
	if file, ranges, whole, ok := fc.newSide(); ok {
		if err := side(file, ranges, whole, head); err != nil {
			return err
		}
	}
	if file, ranges, whole, ok := fc.oldSide(); ok {
		if err := side(file, ranges, whole, base); err != nil {
			return err
		}
	}
	if !touched {
		acc.unattributed(fc.Path, ReasonProtoNoDecl, c)
	}
	return nil
}

// attributeRPC credits the op of rpc r declared in pf, which the working
// tree may no longer declare (Removed).
func (ix *Index) attributeRPC(acc *accum, pf protoFile, file string, r protoRPC, reason Reason, c *Commit) {
	name := pf.opName(r)
	op, ok := ix.ops[name]
	if !ok {
		op = Op{
			Name: name, Kind: KindRPC, File: file, Line: r.Line,
			RPCService: pf.service(r), RPCMethod: r.Name,
			ProtoFile: file, ProtoLine: r.Line,
		}
	}
	acc.attribute(op, !ok, reason, c)
}

// qualify resolves a proto type reference written in package pkg to a
// fully qualified name. A dotted reference is taken as already qualified,
// which is how forge protos spell cross-package types.
func qualify(pkg, t string) string {
	t = strings.TrimPrefix(t, ".")
	if t == "" || strings.Contains(t, ".") || pkg == "" {
		return t
	}
	return pkg + "." + t
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// accum collects attributions across files and commits, deduplicated.
type accum struct {
	ops      map[string]*MappedOp
	reasons  map[string]map[Reason]bool
	commits  map[string]map[string]bool
	unattr   map[[2]string]*Unattributed
	uCommits map[[2]string]map[string]bool
	mapped   []Commit
}

func newAccum() *accum {
	return &accum{
		ops:      map[string]*MappedOp{},
		reasons:  map[string]map[Reason]bool{},
		commits:  map[string]map[string]bool{},
		unattr:   map[[2]string]*Unattributed{},
		uCommits: map[[2]string]map[string]bool{},
	}
}

func (a *accum) attribute(op Op, removed bool, r Reason, c *Commit) {
	m := a.ops[op.Name]
	if m == nil {
		m = &MappedOp{Op: op, Removed: removed, Reasons: []Reason{}}
		a.ops[op.Name] = m
		a.reasons[op.Name] = map[Reason]bool{}
		a.commits[op.Name] = map[string]bool{}
	}
	if !a.reasons[op.Name][r] {
		a.reasons[op.Name][r] = true
		m.Reasons = append(m.Reasons, r)
	}
	if c != nil && !a.commits[op.Name][c.SHA] {
		a.commits[op.Name][c.SHA] = true
		m.Commits = append(m.Commits, *c)
	}
}

func (a *accum) unattributed(file, reason string, c *Commit) {
	k := [2]string{file, reason}
	u := a.unattr[k]
	if u == nil {
		u = &Unattributed{File: file, Reason: reason}
		a.unattr[k] = u
		a.uCommits[k] = map[string]bool{}
	}
	if c != nil && !a.uCommits[k][c.SHA] {
		a.uCommits[k][c.SHA] = true
		u.Commits = append(u.Commits, *c)
	}
}

// mapping renders the accumulated result in its deterministic order: ops by
// name, reasons by location, unattributed files by path then reason. Commit
// lists keep the order commits were mapped in, newest first.
func (a *accum) mapping() Mapping {
	m := Mapping{Commits: a.mapped, Ops: []MappedOp{}, Unattributed: []Unattributed{}}
	if m.Commits == nil {
		m.Commits = []Commit{}
	}
	for _, op := range a.ops {
		sort.Slice(op.Reasons, func(i, j int) bool {
			x, y := op.Reasons[i], op.Reasons[j]
			if x.File != y.File {
				return x.File < y.File
			}
			if x.Line != y.Line {
				return x.Line < y.Line
			}
			if x.Via != y.Via {
				return x.Via < y.Via
			}
			return x.Function < y.Function
		})
		m.Ops = append(m.Ops, *op)
	}
	sort.Slice(m.Ops, func(i, j int) bool { return m.Ops[i].Name < m.Ops[j].Name })
	for _, u := range a.unattr {
		m.Unattributed = append(m.Unattributed, *u)
	}
	sort.Slice(m.Unattributed, func(i, j int) bool {
		x, y := m.Unattributed[i], m.Unattributed[j]
		if x.File != y.File {
			return x.File < y.File
		}
		return x.Reason < y.Reason
	})
	return m
}
