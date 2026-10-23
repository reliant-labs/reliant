// File: internal/codegen/package_directives.go
//
// Package-level forge directives — comment markers a package declares in
// its own source (contract.go / doc.go / any non-test .go) to opt into or
// out of generator behaviour, as an alternative to a central forge.yaml
// list. They are the package-scoped siblings of the field-level
// `//forge:optional-dep` marker (see deps_parser.go) and follow the same
// recognition rules: both the spaced (`// forge:foo`) and unspaced
// (`//forge:foo`) forms are accepted, and the marker must be the WHOLE
// comment line (after stripping comment syntax + whitespace) so prose that
// merely mentions the directive is never mistaken for it.
//
// Three directives live here, deliberately separate because they mean
// DIFFERENT things (FORGE_SHAPE_REDESIGN §1/§4, friction fr-b158e37541):
//
//   - `//forge:exclude-contract` — opt this package OUT of contract
//     codegen (mock/middleware/tracing/metrics scaffold). The per-package
//     equivalent of listing the package in forge.yaml `contracts.exclude`.
//     The package is NOT contract-shaped (or doesn't want a mock).
//
//   - `//forge:external-component` (alias `//forge:provided`) — this
//     component is HAND-CONSTRUCTED in providers.go / OpenInfra, NOT by the
//     type-topological Build injector. The injector skips it as a Build
//     node, but the package STILL gets its contract/mock codegen. This is
//     for a package that IS contract-shaped and WANTS its mock, but whose
//     construction is bespoke (adapter wrapping, two-phase setters, a
//     dialer nil'd on unset env) and so cannot be a plain New(Deps) node.
//     The server HAS it: a consumer's Deps field resolves to the Infra field
//     the owner put it on.
//
//   - `//forge:not-a-component: <why>` — the server does NOT have this
//     package at all. It keeps its contract, its mock and (if it opts in)
//     its decorator, but the server composition never constructs it, never
//     resolves its Deps and gives it no Infra field: something else builds
//     it (the CLI, a daemon, another binary). A server component that
//     requires it by type is a generate error, because the marker would
//     then be false. Unlike external-component, it makes a claim about the
//     server rather than about how the server builds it.
//
// All are recognized as either a package doc comment (above the `package`
// clause) or a free-standing comment anywhere in the package's .go files —
// the latter so a package can carry the marker without disturbing its
// existing package doc. Like the package clause, a directive is a property of
// the PACKAGE, whichever file spells it: the conventional home is doc.go, and
// a package that repeats one must repeat it identically (reason and
// allowances), which `forge lint` checks.

package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Package directive spellings. exclude-contract has a single spelling;
// external-component accepts `provided` as a shorter synonym (same intent,
// "forge does not build this").
const (
	directiveExcludeContract    = "forge:exclude-contract"
	directiveExternalComponent  = "forge:external-component"
	directiveExternalComponent2 = "forge:provided"
	directiveNotAComponent      = "forge:not-a-component"

	// directiveServiceMarker / directiveContractMarker sit on an INTERFACE's
	// doc (or inline) comment and declare it the package's Service contract
	// under a NON-canonical name. They free the friction the strict
	// contract-name rule imposed: an author who names an adapter interface
	// role-orientedly (Gateway / Provider / Dispatcher — which the adapter
	// skill itself encourages) no longer hits a hard lint + a multi-file
	// rename. Both spellings mean the same thing; `contract` reads better on
	// a non-adapter package, `service` on a service-shaped one.
	directiveServiceMarker  = "forge:service"
	directiveContractMarker = "forge:contract"

	// directiveConstructorMarker sits on the package constructor's (`func New`)
	// doc comment. It is the Phase-2 opt-in signal for the generated
	// observability decorator (middleware_gen.go) — the marker anchors both
	// the decorator generation and, later, the constructor-unexport lint.
	// Scaffolds stamp it by default so a generated package is born instrumented.
	directiveConstructorMarker = "forge:constructor"

	// directiveNoObserve is the observability opt-out. It has TWO scopes:
	//
	//   - PACKAGE-level — on the constructor's doc, the package doc, or the
	//     Service/marked contract interface's doc (i.e. on contract.go / New).
	//     Skips the decorator entirely (compose falls back to the unwrapped
	//     construction; the stale _gen decorator is removed).
	//   - METHOD-level — on a single interface method's doc comment. The
	//     decorator still wraps the type, but THAT method delegates directly
	//     without routing through the chain (MethodDef.SkipObserve; handled in
	//     the contract decorator generator, NOT here).
	//
	// observePackageDecision consults only the PACKAGE-level slots — it never
	// reads an interface method's doc, so a method-level opt-out is never
	// mistaken for a package-level one.
	directiveNoObserve = "forge:no-observe"
)

// observePackageDecision scans the package rooted at dir for the two
// PACKAGE-level observability markers, returning them in one AST walk:
//
//   - hasConstructorMarker: `// forge:constructor` on the constructor's doc
//     (any name — see IsComponentConstructor).
//   - hasNoObserve: `// forge:no-observe` on the constructor's doc, the package
//     doc comment, or the contract interface's doc — the package-level opt-out.
//
// METHOD-level `// forge:no-observe` (on an interface method field) is
// deliberately NOT consulted here: those slots are the method fields' own doc
// comments, which this walk never inspects, so the two scopes can never be
// conflated. Non-test, non-generated files only; both spaced (`// forge:x`)
// and unspaced (`//forge:x`) forms are recognized, and the marker must be the
// whole comment line.
func observePackageDecision(dir string) (hasConstructorMarker, hasNoObserve bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		// Package doc comment — a package-level opt-out may live on contract.go.
		if anyCommentGroupHasDirective(directiveNoObserve, file.Doc) {
			hasNoObserve = true
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				// The package constructor's doc anchors both markers.
				if !IsComponentConstructor(d) {
					continue
				}
				if anyCommentGroupHasDirective(directiveConstructorMarker, d.Doc) {
					hasConstructorMarker = true
				}
				if anyCommentGroupHasDirective(directiveNoObserve, d.Doc) {
					hasNoObserve = true
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					// Only the INTERFACE type-decl doc slots — never a method
					// field's doc, so a method-level opt-out is not seen here.
					if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
						continue
					}
					if anyCommentGroupHasDirective(directiveNoObserve, d.Doc, ts.Doc, ts.Comment) {
						hasNoObserve = true
					}
				}
			}
		}
	}
	return hasConstructorMarker, hasNoObserve
}

// HasConstructorMarker reports whether the package rooted at dir carries the
// `// forge:constructor` marker on its constructor's doc comment.
func HasConstructorMarker(dir string) bool {
	m, _ := observePackageDecision(dir)
	return m
}

// HasPackageNoObserveDirective reports whether the package rooted at dir opts
// OUT of observability at the PACKAGE level via `// forge:no-observe` (on the
// constructor, package doc, or contract interface). Method-level opt-outs are
// not reported here — see MethodDef.SkipObserve.
func HasPackageNoObserveDirective(dir string) bool {
	_, n := observePackageDecision(dir)
	return n
}

// anyCommentGroupHasDirective reports whether any of the given comment groups
// carries a comment whose whole text (after stripping comment markers +
// whitespace) equals needle. Mirrors HasServiceContractMarker's rule so prose
// that merely mentions a directive is never a false match; recognizes both the
// spaced and unspaced forms.
func anyCommentGroupHasDirective(needle string, groups ...*ast.CommentGroup) bool {
	for _, cg := range groups {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			if commentEquals(c, needle) {
				return true
			}
		}
	}
	return false
}

// HasServiceContractMarker reports whether any of the given comment groups
// carries the `//forge:service` or `//forge:contract` marker. The marker
// must be the WHOLE comment line (after stripping comment syntax +
// whitespace), matching every other forge directive, so prose that merely
// mentions it is never a false match. Both spaced (`// forge:service`) and
// unspaced (`//forge:service`) forms are recognized. Shared by
// DetectServiceInterfaceName (codegen) and the contract-name lint
// (contractcheck) so the two never drift on what counts as marked.
func HasServiceContractMarker(groups ...*ast.CommentGroup) bool {
	for _, cg := range groups {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			switch trimCommentMarkers(c.Text) {
			case directiveServiceMarker, directiveContractMarker:
				return true
			}
		}
	}
	return false
}

// DetectServiceInterfaceName returns the name of the package's Service
// contract interface — the type key a consumer's by-type Deps field is
// matched against, and the interface the contract-name lint accepts:
//
//   - "Service"  — a type named `Service` exists (interface OR struct):
//     the zero-annotation canonical path, unchanged and always preferred.
//   - <name>     — no `Service` type, but an interface carries the
//     `//forge:service` / `//forge:contract` marker: the marker frees the
//     interface's name (Gateway / Provider / Dispatcher …).
//   - ""         — neither; the caller defaults to "Service".
//
// The canonical `Service` always wins over a marker (a package with both
// keeps `Service`), so existing projects are byte-for-byte unaffected.
// Among multiple marked interfaces the first in deterministic order (file
// name, then source position) is chosen; marking two interfaces is a user
// error the lint can surface separately. Non-test, non-generated files
// only. Unreadable/unparseable dirs return "".
func DetectServiceInterfaceName(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") ||
			strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	marked := ""
	for _, n := range names {
		file, perr := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// Canonical `Service` (interface OR struct) always wins.
				if ts.Name.Name == "Service" {
					return "Service"
				}
				if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
					continue
				}
				if marked != "" {
					continue
				}
				// The doc slot can live on the grouped GenDecl (`type (…)`)
				// or the spec itself; the inline slot on the spec.
				if HasServiceContractMarker(genDecl.Doc, ts.Doc, ts.Comment) {
					marked = ts.Name.Name
				}
			}
		}
	}
	return marked
}

// HasExcludeContractDirective reports whether the package rooted at dir
// declares `//forge:exclude-contract` in any of its non-test .go files, in
// either the reasoned form (`//forge:exclude-contract: <why>`) or the bare
// one. A package carrying this directive opts OUT of contract codegen — the
// per-package equivalent of forge.yaml `contracts.exclude`.
//
// The bare form still EXCLUDES here, deliberately: codegen must never break a
// project over the spelling of an opt-out. It is `forge lint` that refuses a
// bare marker (see internal/linter/contract/exclude_directive.go), because a
// reason is what turns the cheapest way to silence the contract rules into a
// decision a reviewer can evaluate.
func HasExcludeContractDirective(dir string) bool {
	_, ok := FindExcludeContractMarker(dir)
	return ok
}

// ReasonedMarker is one occurrence of a package directive that takes a
// reason — `//forge:exclude-contract: <why>` or `//forge:not-a-component:
// <why>`.
type ReasonedMarker struct {
	// File is the absolute path of the file carrying the marker.
	File string
	// Line is the marker's 1-indexed line.
	Line int
	// Reason is the text after `forge:<directive>:`; "" for a bare marker.
	Reason string
}

// FindExcludeContractMarker returns the package's `//forge:exclude-contract`
// marker, scanning non-test, non-generated files in name order so the answer
// is deterministic. When a package carries several, the first REASONED one
// wins — a package that already says why is not made to say it twice.
func FindExcludeContractMarker(dir string) (ReasonedMarker, bool) {
	return findReasonedDirective(dir, directiveExcludeContract)
}

// HasNotAComponentDirective reports whether the package rooted at dir
// declares `//forge:not-a-component` in any of its non-test .go files, with
// or without a reason. Such a package keeps its contract codegen (mock, and
// the decorator if it opts in) but is not a component of the server
// composition: compose.go never constructs it and never resolves its Deps.
//
// The bare form counts here for the same reason a bare exclude-contract
// does: codegen must never break a project over the spelling of a marker.
// `forge lint` refuses the bare form (forge-not-a-component-reason).
func HasNotAComponentDirective(dir string) bool {
	_, ok := FindNotAComponentMarker(dir)
	return ok
}

// FindNotAComponentMarker returns the package's `//forge:not-a-component`
// marker, preferring a reasoned one (see FindExcludeContractMarker).
func FindNotAComponentMarker(dir string) (ReasonedMarker, bool) {
	return findReasonedDirective(dir, directiveNotAComponent)
}

// ParseNotAComponentComment reports whether one raw comment IS the
// not-a-component directive, and its reason — the same two accepted
// spellings as ParseExcludeContractComment.
func ParseNotAComponentComment(raw string) (reason string, ok bool) {
	return parseReasonedDirective(raw, directiveNotAComponent)
}

// FindExcludeContractMarkers returns EVERY `//forge:exclude-contract` marker
// in the package, in file-name then line order. A package directive is a
// property of the package, like its package clause: it applies from whichever
// file declares it, and a package that declares it twice must say the same
// thing both times — the lint gate (internal/linter/contract) refuses two
// declarations that disagree.
func FindExcludeContractMarkers(dir string) []ReasonedMarker {
	return findReasonedDirectives(dir, directiveExcludeContract)
}

// FindNotAComponentMarkers returns every `//forge:not-a-component` marker in
// the package (see FindExcludeContractMarkers).
func FindNotAComponentMarkers(dir string) []ReasonedMarker {
	return findReasonedDirectives(dir, directiveNotAComponent)
}

// findReasonedDirective returns the first occurrence of a reasoned package
// directive (`//<directive>: <why>` or the bare `//<directive>`) in dir's
// non-test, non-generated files, scanned in name order. A reasoned
// occurrence anywhere wins over a bare one.
func findReasonedDirective(dir, directive string) (ReasonedMarker, bool) {
	all := findReasonedDirectives(dir, directive)
	for _, m := range all {
		if m.Reason != "" {
			return m, true
		}
	}
	if len(all) > 0 {
		return all[0], true
	}
	return ReasonedMarker{}, false
}

// findReasonedDirectives returns every occurrence of a reasoned package
// directive in dir's non-test, non-generated files, in file-name then line
// order.
func findReasonedDirectives(dir, directive string) []ReasonedMarker {
	var out []ReasonedMarker
	forEachPackageComment(dir, func(path string, line int, raw string) {
		if reason, ok := parseReasonedDirective(raw, directive); ok {
			out = append(out, ReasonedMarker{File: path, Line: line, Reason: reason})
		}
	})
	return out
}

// forEachPackageComment calls fn for every comment in dir's non-test,
// non-generated .go files, in file-name then position order. Unparseable
// files are skipped: a directive is read best-effort.
func forEachPackageComment(dir string, fn func(path string, line int, raw string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") ||
			strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	for _, n := range names {
		path := filepath.Join(dir, n)
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				fn(path, fset.Position(c.Pos()).Line, c.Text)
			}
		}
	}
}

// ParseExcludeContractComment reports whether one raw comment IS the
// exclude-contract directive, and its reason. Accepted, spaced or not:
//
//	//forge:exclude-contract: <why>   → ok, reason "<why>"
//	//forge:exclude-contract          → ok, reason "" (bare — lint refuses it)
//
// Anything else — prose that mentions the directive, `forge:exclude-contract,
// forge:outbound-io, …` lists, a different separator — is not the directive,
// matching every other forge marker's whole-line rule. A different separator
// is not silent, though: `//forge:exclude-contract — <why>` is what
// ClassifyGoMarkerComment calls GoMarkerMalformed, and `forge lint` reports it
// with its file:line and this form (go_markers.go).
func ParseExcludeContractComment(raw string) (reason string, ok bool) {
	return parseReasonedDirective(raw, directiveExcludeContract)
}

// parseReasonedDirective is the shared whole-line rule for a directive that
// takes a reason after a colon.
func parseReasonedDirective(raw, directive string) (reason string, ok bool) {
	text := trimCommentMarkers(raw)
	if text == directive {
		return "", true
	}
	rest, has := strings.CutPrefix(text, directive+":")
	if !has {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// HasOutboundIODirective reports whether the package rooted at dir declares
// `//forge:outbound-io` in any of its non-test .go files — the marker the
// adapter scaffold stamps on contract.go. It asserts one property: this
// package calls OUT to a third-party system and nothing calls in. The
// outbound-io-no-rpc contract check reads it (an inbound handler contradicts
// the claim).
func HasOutboundIODirective(dir string) bool {
	return packageHasDirective(dir, "forge:outbound-io")
}

// HasExternalComponentDirective reports whether the package rooted at dir
// declares `//forge:external-component` (or `//forge:provided`) in any of
// its non-test .go files. A package carrying this directive is skipped by
// the Build injector (it is hand-wired in providers.go / OpenInfra) but
// STILL gets its contract/mock codegen.
func HasExternalComponentDirective(dir string) bool {
	return packageHasDirective(dir, directiveExternalComponent) ||
		packageHasDirective(dir, directiveExternalComponent2)
}

// packageHasDirective scans every non-test .go file in dir for a comment
// whose whole text (after stripping comment markers + whitespace) equals
// needle. It recognizes both `// needle` and `//needle` (the directive
// form Go's CommentGroup.Text() would otherwise drop). Comments are scanned
// raw via the FileSet's comment list so package-doc, free-standing, and
// inline comments are all seen. Unparseable or unreadable files are
// skipped best-effort — a directive is an opt-in signal; failing to read
// it just means the default behaviour applies.
func packageHasDirective(dir, needle string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Generated files never carry hand-authored directives; skip them
		// both for speed and to avoid a regenerated file echoing a marker.
		if strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, cg := range file.Comments {
			if cg == nil {
				continue
			}
			for _, c := range cg.List {
				if commentEquals(c, needle) {
					return true
				}
			}
		}
	}
	return false
}

// commentEquals reports whether a single *ast.Comment's inner text equals
// needle. Mirrors HasOptionalDepMarkerCommentGroup's per-comment rule:
// strip `//`, `/* */`, and surrounding whitespace, then exact-match — so a
// comment that merely references the directive inside surrounding prose is
// not a match.
func commentEquals(c *ast.Comment, needle string) bool {
	return trimCommentMarkers(c.Text) == needle
}
