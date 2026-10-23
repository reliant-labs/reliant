package scaffolds

// Cross-tier symbol references: the authoring-time guard on forge's own
// template tree.
//
// # The invariant
//
// A file forge REGENERATES may never reference a symbol that only a file
// forge writes ONCE can define. If it does, forge has authored a permanent
// build break: it keeps rewriting the caller while being structurally unable
// to write the callee, and the generated tree stops compiling the moment the
// user adds a component.
//
// The canonical instance: `forge scaffold worker nightly` scaffolds
// cmd/<bin>/cmd/workers/nightly.go calling `c.WorkerNightly()`, but that
// accessor is projected by internal/app/lifecycle.go — written at project
// birth, before the worker existed, and never rewritten. Result:
//
//	cmd/svcdemo/cmd/workers/nightly.go:40:87: c.WorkerNightly undefined
//	  (type *app.Components has no field or method WorkerNightly)
//
// `scaffold operator` has the identical shape via `c.OperatorFleet()`.
//
// # Detection strategy
//
// Full type resolution across Go text/templates is not achievable: a
// template is not parseable Go, the receiver types are spelled in template
// actions, and the same symbol can be emitted by several templates with
// different data shapes. So this lint does not attempt symbol resolution. It
// keys on a much narrower, much more reliable signal that happens to be
// exactly the shape of the defect class:
//
//	A DERIVED SYMBOL FAMILY — a Go identifier whose spelling is
//	part-static, part-template-action (`Worker{{.FieldName}}`,
//	`Operator{{.FieldName}}`, `Mount{{.FieldName}}`) — is a projection of
//	DISCOVERED STATE. One member of the family exists per discovered
//	component, so the set GROWS over the life of the project.
//
// A derived family is further classified as a PROJECTION when its definition
// sits inside a `{{range}}`: forge emits one definition per row of a
// discovered set. That is the decisive property. A projection's definition
// site must be a file forge rewrites every run, because new members appear
// after the file was first written.
//
// So the rule is:
//
//	A derived symbol family that some template DEFINES inside a {{range}},
//	and some OTHER template REFERENCES, must have at least one definition
//	site in a Tier-1 (regenerated-every-run) template.
//
// Symbols with a FIXED name (`NewComponents`, `AllWorkers`) are deliberately
// out of scope: a fixed name comes into existence when the file is first
// written and never grows, so a scaffold-once home for it is correct, not a
// defect. Including them would have flagged every legitimate Tier-1 ->
// scaffold-once call (`cmd/<bin>/cmd/serve.go` calling `app.NewComponents`)
// and trained everyone to ignore the rule.
//
// # The sanctioned escape valve
//
// A scaffold-once file MAY hold a projection if forge additively RECONCILES
// it — i.e. `forge generate` injects the missing accessor into the existing
// user-owned file rather than rewriting it. That is a real, legitimate
// mechanism (it is how internal/app/compose.go gains a component field).
//
// The escape valve is not an allowlist and cannot be asserted by hand: the
// lint looks for the EVIDENCE. It scans forge's own generator sources for an
// inline template literal that emits a definition of the same family. If
// `internal/codegen` carries
//
//	func (c *Components) Worker{{.FieldName}}() serverkit.Worker {
//
// inside an injector template, the projection is reconciled and the finding
// is withdrawn. Delete the reconciler and the lint goes red again — the
// exemption is only ever as alive as the code that earns it.
//
// # What this CANNOT catch (read this before trusting it)
//
//  1. FIXED-NAME symbols. A Tier-1 template calling `c.Foo()` where `Foo` is
//     defined only in a scaffold-once template is not reported. See above for
//     why that is deliberate — but it does mean a SIGNATURE change to a
//     scaffold-once symbol can still break Tier-1 callers silently.
//
//  2. FULLY-INTERPOLATED identifiers. `{{.FieldName}}` with no static text
//     around it carries no matchable signal, so `Components`' own struct
//     fields (compose.go.tmpl emits `{{.FieldName}} {{.FieldType}}`) are
//     invisible. Forge's convention of a static prefix (`Worker`, `Operator`,
//     `Mount`) is what makes the family trackable at all.
//
//  3. STRUCT FIELDS and CONSTANTS generally. Only func/method declarations
//     are treated as definition sites. Reference sites are call expressions
//     (`.Name{{...}}(`) plus METHOD EXPRESSIONS on a parenthesized type
//     (`(*app.Components).Mount{{...}}`, `(pkg.T).Name{{...}}`) — the latter
//     because a method named as a VALUE binds the symbol exactly as hard as
//     calling it, and forge emits precisely that in cmd-svc-group.go.tmpl.
//
//     What is still missed is the bare selector read: `x.Mount{{.Name}}`
//     passed as a func value with no parens on the receiver. That shape is
//     textually identical to reading a struct field, which is far and away
//     the more common spelling, so matching it would report a projected
//     field read as if it were a call. The parenthesized form carries the
//     signal unambiguously (a field cannot be selected off a type), so the
//     narrow rule is the one implemented. Measured against the live tree the
//     broad rule added 41 spurious reference records — `.Deps`, `.Spec`,
//     `.Name`, protoc descriptors — and zero findings.
//
//  4. BARE same-package calls. References must appear in selector form
//     (`x.Name{{...}}(`) or as a parenthesized method expression, on a line
//     that is not a `//` comment. A template calling a package-local derived
//     function without a receiver is not matched, and neither is one whose
//     only call site is inside a block comment.
//
//  5. SYMBOLS FORGE NEVER DEFINES. A template referencing a derived family
//     that NO template defines is ignored on purpose: that is overwhelmingly
//     a call into protoc output or user code (`.Get{{.EntityName}}()`), and
//     flagging it would drown the real signal.
//
//  6. UNREFERENCED FROZEN PROJECTIONS. A projection in a scaffold-once
//     template that NO other template calls is not reported, even though its
//     member set is equally frozen. Only a forge-emitted reference makes it a
//     forge-authored build break; without one it is a completeness question
//     about a scaffold the user owns. This deliberately leaves one hole: when
//     forge tells the USER to hand-write the reference (as `forge scaffold
//     worker` does for AllWorkers()), the lint stays quiet and the user can
//     still walk into the same wall.
//
//  7. RECONCILERS THAT BUILD SOURCE BY CONCATENATION. The escape valve only
//     sees definitions spelled as template text. An injector that assembles
//     `"func (c *Components) Worker" + name + "()"` from string pieces is not
//     recognized, and its template will be reported. That is the safe failure
//     direction (a false positive, not a silent miss) — spell the injected
//     definition as a template literal and it resolves itself.
//
//  8. TIER MISCLASSIFICATION. Tiers come from the template's own banner and,
//     failing that, from classifyTemplate's name list (banners.go). A
//     template whose banner lies, or whose name is not yet in that list, is
//     tiered wrong here too. This lint is downstream of that classifier, not
//     a second opinion on it.
//
// # Where it runs
//
// This is an authoring-time check over forge's OWN templates, so it is
// meaningful only inside the forge repo — like BannerLintRoot, it no-ops when
// `internal/templates/` is absent. It is gated by TestCrossTierSymbols in
// cross_tier_test.go, which runs on every `go test ./...` and FAILS the
// build. That is deliberate: `forge lint`'s scaffold rules are advisory
// output a user may reasonably ignore, but this invariant is not a matter of
// taste — a violation means forge ships a tree that does not compile. It is
// exported with BannerLintRoot's signature so it can also be surfaced as a
// `forge lint` step without any change here.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// wildcard replaces a `{{...}}` action when a derived identifier is
// normalized into a family key: `Worker{{.FieldName}}` and
// `Worker{{.MountFieldName | pascalCase}}` both key to "Worker\x00", so a
// definition and a reference match even when they name different fields of
// different data shapes. NUL cannot occur in template source, so it can never
// collide with real content.
const wildcard = "\x00"

var (
	// tmplActionRE matches one template action. Actions do not nest, so the
	// non-brace body is exact rather than approximate.
	tmplActionRE = regexp.MustCompile(`\{\{[^{}]*\}\}`)

	// identRE is a Go identifier that may be interleaved with template
	// actions: `Worker{{.FieldName}}`, `{{.EntityLower}}ToProto`,
	// `New{{.FieldName}}Cmd`.
	identPat = `(?:[A-Za-z0-9_]+|\{\{[^{}]*\}\})+`

	// defRE matches a func or method declaration whose NAME may be derived.
	// The optional `\([^)]*\)` is the receiver; it only engages when the
	// character after `func ` is `(`, so a plain `func {{.X}}Foo(` still
	// binds its name correctly.
	defRE = regexp.MustCompile(`(?m)^[ \t]*func[ \t]+(?:\([^)]*\)[ \t]*)?(` + identPat + `)[ \t]*\(`)

	// refRE matches a selector call: `c.Worker{{.FieldName}}(`,
	// `workers.New{{.FieldName}}Cmd(`.
	refRE = regexp.MustCompile(`\.(` + identPat + `)[ \t]*\(`)

	// methodExprRE matches a METHOD EXPRESSION — the method named as a
	// value, with no call parens after it:
	//
	//	cmd.ServeSpec{Mount: (*app.Components).Mount{{.MountFieldName}}}
	//
	// This is a genuine cross-tier reference that refRE cannot see, because
	// the whole point of a method expression is that it is passed rather
	// than invoked. It binds the symbol just as hard as a call does: if the
	// frozen definer never grew `Mount<Svc>`, the tree does not compile.
	//
	// The parenthesized receiver is the entire signal, and it is what keeps
	// this precise. A bare `x.Method` used as a value is indistinguishable
	// from an ordinary field read, so it is deliberately NOT matched (see
	// limitation 3). The parenthesized form, by contrast, is only ever legal
	// Go as a method expression on a type: `(*T).M` and `(pkg.T).M` have no
	// other reading, since a field cannot be selected off a type.
	//
	// The receiver must therefore LOOK LIKE A TYPE — pointer-starred, or a
	// qualified `pkg.Type`. A bare `(x).Foo` is excluded on purpose: that
	// spelling is a parenthesized value, not a type.
	methodExprRE = regexp.MustCompile(
		`\((?:` +
			`\*[ \t]*[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*` + // (*T) / (*pkg.T)
			`|[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+` + // (pkg.T)
			`)\)[ \t]*\.(` + identPat + `)`)

	// collapseWildcardRE folds `{{.A}}{{.B}}` down to a single wildcard so
	// adjacent actions do not change a family's identity.
	collapseWildcardRE = regexp.MustCompile(wildcard + `+`)
)

// symbolSite is one definition or reference of a derived symbol family.
type symbolSite struct {
	key     string // normalized family key, e.g. "Worker\x00"
	display string // as written, e.g. "Worker{{.FieldName}}"
	line    int
	inRange bool // definition sits inside a {{range}} — a projection of discovered state
}

// templateFacts is everything the rule needs about one template.
type templateFacts struct {
	rel  string
	tier templateTier
	defs []symbolSite
	refs []symbolSite
}

// CrossTierLintRoot walks the template tree under root and reports derived
// symbol families whose only definition site is a template forge writes once.
// Outside the forge repo (no internal/templates/) it returns no findings.
//
// See the package-level commentary at the top of this file for the detection
// strategy and, more importantly, for what it cannot catch.
func CrossTierLintRoot(root string) (Result, error) {
	var result Result

	troot := filepath.Join(root, "internal", "templates")
	if _, err := os.Stat(troot); os.IsNotExist(err) {
		return result, nil
	}

	var facts []templateFacts
	err := filepath.WalkDir(troot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Go templates only. The invariant is about Go symbol resolution;
		// a .ts/.k/.yml template cannot break a Go build.
		if !strings.HasSuffix(path, ".go.tmpl") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		rel := relPath(path, root)
		src := string(data)
		defs, refs := scanTemplateSymbols(src)
		facts = append(facts, templateFacts{
			rel:  rel,
			tier: resolveTemplateTier(rel, firstLines(data, 60)),
			defs: defs,
			refs: refs,
		})
		return nil
	})
	if err != nil {
		return result, err
	}

	reconciled, err := scanReconciledFamilies(root)
	if err != nil {
		return result, err
	}

	result.Findings = evaluateCrossTier(facts, reconciled)
	return result, nil
}

// siteRow pairs a symbol site with the template it was found in.
type siteRow struct {
	f    templateFacts
	site symbolSite
}

// evaluateCrossTier applies the rule to the collected facts. Kept separate
// from the walk so the decision logic reads without any I/O in the way.
func evaluateCrossTier(facts []templateFacts, reconciled map[string]string) []Finding {
	// Index every definition site by symbol family.
	defsByKey := map[string][]siteRow{}
	for _, f := range facts {
		for _, d := range f.defs {
			defsByKey[d.key] = append(defsByKey[d.key], siteRow{f, d})
		}
	}

	var findings []Finding
	keys := make([]string, 0, len(defsByKey))
	for k := range defsByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		sites := defsByKey[key]

		// A family with any Tier-1 definition site is safe: forge rewrites
		// that file every run, so the family stays in sync with whatever the
		// project discovers.
		tier1Defines := false
		definingTemplates := map[string]bool{}
		for _, s := range sites {
			definingTemplates[s.f.rel] = true
			if s.f.tier == tier1Generated {
				tier1Defines = true
			}
		}
		if tier1Defines {
			continue
		}

		// Only PROJECTIONS are in scope: a definition emitted once per row of
		// a discovered set (inside a {{range}}). A one-off derived definition
		// — `handleWebhook{{.Name}}` in the webhook scaffold — is written at
		// the same moment as everything that calls it, so a write-once home
		// is correct for it.
		var projections []siteRow
		for _, s := range sites {
			if s.site.inRange {
				projections = append(projections, s)
			}
		}
		if len(projections) == 0 {
			continue
		}

		// Sanctioned escape valve: forge additively injects this family into
		// the existing file, so the write-once home is reconciled, not frozen.
		if _, ok := reconciled[key]; ok {
			continue
		}

		// Collect the referencing templates (excluding the definers
		// themselves — a template calling its own projection is fine).
		var refs []siteRow
		for _, f := range facts {
			if definingTemplates[f.rel] {
				continue
			}
			for _, r := range f.refs {
				if r.key == key {
					refs = append(refs, siteRow{f, r})
					break // one finding per (ref template, def template) pair
				}
			}
		}

		// No forge-authored caller means no forge-authored build break. An
		// unreferenced frozen projection is a completeness question about a
		// scaffold the user owns, not a defect — and reporting it produced
		// nothing but false alarms (the per-entity `Test{{.Name}}_Generated`
		// helpers in the one-shot test scaffolds, which are a starting point
		// by design and are invoked by `go test`, not by forge). See the
		// limits section at the top of this file.
		if len(refs) == 0 {
			continue
		}

		display := projections[0].site.display
		sort.Slice(refs, func(i, j int) bool { return refs[i].f.rel < refs[j].f.rel })
		for _, p := range projections {
			for _, r := range refs {
				findings = append(findings, Finding{
					Rule:     "cross-tier-derived-symbol",
					Severity: SeverityError,
					Path:     r.f.rel,
					Message:  crossTierMessage(display, r.f, r.site, p.f, p.site),
				})
			}
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}

// crossTierMessage spells out both files, both tiers, and the two legitimate
// fixes. A lint that only says "violation" makes the next author guess.
func crossTierMessage(display string, refF templateFacts, refSite symbolSite, defF templateFacts, defSite symbolSite) string {
	return fmt.Sprintf(
		"forge emits a reference to %q that it cannot guarantee it has written.\n"+
			"      reference: %s:%d\n"+
			"                 tier: %s\n"+
			"      definition: %s:%d\n"+
			"                 tier: %s\n"+
			"    %s projects one %q per discovered component (inside a {{range}}), and no Tier-1 template defines that family. "+
			"Because the defining file is written once and never rewritten, a component added after project birth leaves a tree that references a symbol nobody wrote "+
			"— the `c.WorkerNightly undefined (type *app.Components has no field or method WorkerNightly)` class of build break.\n"+
			"    Two legitimate fixes: (1) move the definition into a Tier-1 (regenerated-every-run) template — usually the right one, because a projection of discovered state is derivable and needs no user input; "+
			"or (2) move the reference out of %s so forge stops emitting a call it cannot guarantee.\n"+
			"    (A generate-time reconciler that additively injects the definition into the existing file also satisfies this rule. This lint recognizes one by finding the definition spelled as a template literal under internal/codegen — a reconciler that concatenates strings will not be seen.)",
		display,
		refF.rel, refSite.line,
		tierLabel(refF.tier),
		defF.rel, defSite.line,
		tierLabel(defF.tier),
		defF.rel, display,
		refF.rel,
	)
}

func tierLabel(t templateTier) string {
	switch t {
	case tier1Generated:
		return "Tier-1 (regenerated every run, DO NOT EDIT, drift-gated)"
	case tier2Scaffold:
		return "scaffold-once (written once at birth, user-owned, never rewritten)"
	case tier3UserOwned:
		return "user-owned skeleton (written once, hand-maintained thereafter)"
	case tierSkip:
		return "banner-less by nature"
	default:
		return "unclassified (extend banners.go's classifyTemplate)"
	}
}

// resolveTemplateTier answers "which lifecycle bucket does this template emit
// into?".
//
// Content-first, then name-based — the same order lintTemplateBanner uses,
// and for the same reason: a template that declares its own lifecycle in its
// banner is the most authoritative source, and classifyTemplate's name lists
// are a fallback that drifts as templates are renamed or re-tiered.
//
// The one addition over the banner lint's reads is the "forge-scaffold
// (USER-OWNED)" spelling: the cmd-tree scaffolds and internal/app/{compose,
// lifecycle}.go declare their write-once lifecycle in those words. The banner
// lint deliberately still asks them for the canonical wording, so that
// spelling is honoured here for TIER RESOLUTION only and changes no banner
// finding.
func resolveTemplateTier(rel, head string) templateTier {
	switch {
	case declaresGeneratedBanner(head):
		return tier1Generated
	case declaresScaffoldOnceBanner(head), declaresWriteOnceBanner(head):
		return tier2Scaffold
	case declaresUserOwnedMarker(head):
		return tier3UserOwned
	}
	return classifyTemplate(rel)
}

// declaresWriteOnceBanner matches the "forge writes this file ONCE
// (write-if-absent), then NEVER regenerates or overwrites it" wording the
// command-tree and internal/app composition scaffolds carry. It is the same
// promise writeForgeScaffoldOnce implements (internal/codegen/writers.go).
func declaresWriteOnceBanner(head string) bool {
	return strings.Contains(head, "forge-scaffold (USER-OWNED)") ||
		strings.Contains(head, "write-if-absent")
}

// scanTemplateSymbols extracts derived definition and reference sites from
// one template's source.
func scanTemplateSymbols(src string) (defs, refs []symbolSite) {
	depths := rangeDepthPoints(src)

	for _, m := range defRE.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		key, ok := familyKey(name)
		if !ok {
			continue
		}
		defs = append(defs, symbolSite{
			key:     key,
			display: name,
			line:    lineAt(src, m[2]),
			inRange: rangeDepthAt(depths, m[2]) > 0,
		})
	}

	// Two reference shapes bind a symbol equally hard: an ordinary selector
	// CALL, and a METHOD EXPRESSION named as a value. Both are collected
	// into the same ref stream — downstream the rule only cares that some
	// other template pins the family, not how it spells the pinning.
	seen := map[int]bool{}
	for _, re := range []*regexp.Regexp{refRE, methodExprRE} {
		for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
			// A reference spelled inside a `//` comment does not compile, so
			// it is not a reference. Skipping it both avoids inventing
			// findings out of prose and makes the reported line the one that
			// actually breaks — forge's templates document their own wiring
			// in the header, so the first textual hit for
			// `c.Worker{{.FieldName}}()` is usually a doc-comment 30 lines
			// above the real call.
			if inLineComment(src, m[0]) {
				continue
			}
			// The two patterns can both claim one site — `(*T).M(x)` is a
			// method expression that is also immediately called. Key on the
			// name's offset so it is recorded once.
			if seen[m[2]] {
				continue
			}
			name := src[m[2]:m[3]]
			key, ok := familyKey(name)
			if !ok {
				continue
			}
			seen[m[2]] = true
			refs = append(refs, symbolSite{
				key:     key,
				display: name,
				line:    lineAt(src, m[2]),
			})
		}
	}
	// Both passes walk the file front-to-back independently, so the merged
	// stream is ordered by pattern then offset. Sort by line so the reported
	// reference is the earliest real one, matching the single-pass behavior
	// callers relied on before method expressions were tracked.
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].line < refs[j].line })
	return defs, refs
}

// inLineComment reports whether byte offset off sits on a line whose first
// non-whitespace content is `//`. Block comments are not tracked — forge's
// templates do not use them for wiring prose.
func inLineComment(src string, off int) bool {
	start := strings.LastIndexByte(src[:off], '\n') + 1
	return strings.HasPrefix(strings.TrimLeft(src[start:off], " \t"), "//")
}

// familyKey normalizes a possibly-derived identifier into its family key.
// It reports false for names that carry no usable signal: a name with no
// template action at all (a fixed symbol, out of scope by design) and a name
// that is ENTIRELY interpolated (`{{.FieldName}}` — a wildcard that would
// match every other wildcard in the tree).
func familyKey(name string) (string, bool) {
	if !strings.Contains(name, "{{") {
		return "", false
	}
	key := collapseWildcardRE.ReplaceAllString(tmplActionRE.ReplaceAllString(name, wildcard), wildcard)
	static := strings.ReplaceAll(key, wildcard, "")
	// Two static characters is the floor for a name to identify anything.
	// It costs nothing: a family is only ever compared against definitions
	// forge itself emits, so a short static part cannot pull in noise from
	// outside the template tree.
	if len(static) < 2 {
		return "", false
	}
	return key, true
}

// depthPoint records the {{range}} nesting depth in force from byte offset
// `off` onward.
type depthPoint struct {
	off   int
	depth int
}

// rangeDepthPoints walks the template's action stream and records where
// {{range}} nesting opens and closes. Only `range` increments the depth —
// `if`/`with`/`block`/`define` push onto the same stack so that their `{{end}}`
// pops the right frame, but they do not make a definition a projection.
func rangeDepthPoints(src string) []depthPoint {
	var (
		points []depthPoint
		stack  []bool // true == this frame was opened by a range
		depth  int
	)
	for _, m := range tmplActionRE.FindAllStringIndex(src, -1) {
		inner := strings.TrimSpace(strings.Trim(strings.TrimSuffix(strings.TrimPrefix(src[m[0]:m[1]], "{{"), "}}"), "-"))
		kw := inner
		if i := strings.IndexAny(inner, " \t"); i >= 0 {
			kw = inner[:i]
		}
		switch kw {
		case "range":
			stack = append(stack, true)
			depth++
			points = append(points, depthPoint{off: m[1], depth: depth})
		case "if", "with", "block", "define":
			stack = append(stack, false)
		case "end":
			if n := len(stack); n > 0 {
				wasRange := stack[n-1]
				stack = stack[:n-1]
				if wasRange && depth > 0 {
					depth--
					points = append(points, depthPoint{off: m[0], depth: depth})
				}
			}
		}
	}
	return points
}

// rangeDepthAt returns the {{range}} depth in force at byte offset off.
func rangeDepthAt(points []depthPoint, off int) int {
	depth := 0
	for _, p := range points {
		if p.off > off {
			break
		}
		depth = p.depth
	}
	return depth
}

func lineAt(src string, off int) int {
	if off > len(src) {
		off = len(src)
	}
	return strings.Count(src[:off], "\n") + 1
}

// scanReconciledFamilies finds derived symbol families that forge's own
// generator sources emit as INJECTED definitions — the sanctioned escape
// valve described at the top of this file.
//
// The scan is deliberately over source text rather than parsed Go: the
// injector's payload lives in a raw string literal (`template.Must(...Parse(
// `func (c *Components) Worker{{.FieldName}}() ...`))`), so the same
// definition regex that reads templates reads it directly. Test sources are
// excluded — an exemption has to be earned by shipping code, not by a fixture.
func scanReconciledFamilies(root string) (map[string]string, error) {
	found := map[string]string{}
	for _, sub := range []string{
		filepath.Join("internal", "codegen"),
		filepath.Join("internal", "generator"),
	} {
		dir := filepath.Join(root, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return fmt.Errorf("read %s: %w", path, rerr)
			}
			src := string(data)
			for _, m := range defRE.FindAllStringSubmatchIndex(src, -1) {
				key, ok := familyKey(src[m[2]:m[3]])
				if !ok {
					continue
				}
				if _, seen := found[key]; !seen {
					found[key] = relPath(path, root)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}
