// Copyright (c) 2025 Reliant Labs
package components

import (
	"regexp"
	"strings"
	"testing"
)

func TestLibraryEmbedding(t *testing.T) {
	lib := NewLibrary()
	for _, entry := range lib.Registry() {
		content, err := componentsFS.ReadFile(entry.FilePath)
		if err != nil {
			t.Errorf("component %q (path %q): embed read failed: %v", entry.Name, entry.FilePath, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("component %q: embedded content is empty", entry.Name)
		}
	}
}

func TestLibraryRegistry(t *testing.T) {
	lib := NewLibrary()
	reg := lib.Registry()

	if len(reg) == 0 {
		t.Fatal("registry is empty")
	}
	if len(lib.ByName()) == 0 {
		t.Fatal("byName is empty")
	}

	for _, entry := range reg {
		if entry.Name == "" {
			t.Error("component with empty name")
		}
		if entry.Category == "" {
			t.Errorf("component %q has empty category", entry.Name)
		}
		if entry.Description == "" {
			t.Errorf("component %q has empty description", entry.Name)
		}
		if len(entry.Tags) == 0 {
			t.Errorf("component %q has no tags", entry.Name)
		}
		if entry.FilePath == "" {
			t.Errorf("component %q has empty file path", entry.Name)
		}
	}

	// No duplicate names
	seen := make(map[string]bool)
	for _, entry := range reg {
		if seen[entry.Name] {
			t.Errorf("duplicate component name: %q", entry.Name)
		}
		seen[entry.Name] = true
	}
}

func TestLibraryCategories(t *testing.T) {
	lib := NewLibrary()
	counts := make(map[Category]int)
	for _, entry := range lib.Registry() {
		counts[entry.Category]++
	}

	expected := map[Category]int{
		CategoryLayouts:  15,
		CategoryCharts:   3,
		CategoryDiagrams: 6,
		CategoryDeck:     7,
		CategoryUI:       52,
	}

	for cat, want := range expected {
		got := counts[cat]
		if got != want {
			t.Errorf("category %q: expected %d components, got %d", cat, want, got)
		}
	}
}

func TestLibraryGet(t *testing.T) {
	lib := NewLibrary()

	content, err := lib.Get("quadrant_chart")
	if err != nil {
		t.Fatalf("get quadrant_chart: %v", err)
	}
	if !strings.Contains(content, "QuadrantChart") {
		t.Error("quadrant_chart content should contain 'QuadrantChart'")
	}

	_, err = lib.Get("nonexistent")
	if err == nil {
		t.Error("get nonexistent should return error")
	}
}

// TestLibraryDependencies pins the import closure `component install` writes
// beside a component: every `./x` import that is a library component, followed
// transitively, and nothing that is not.
func TestLibraryDependencies(t *testing.T) {
	lib := NewLibrary()
	for name, want := range map[string][]string{
		"input":         {"form_field_context"},
		"textarea":      {"form_field_context"},
		"toggle_switch": {"form_field_context"},
		"page_header":   {"link"},
		"dropdown_menu": {"link", "popover"},
		"context_menu":  {"dropdown_menu", "link", "popover"},
		"combobox":      {"form_field_context", "popover"},
		"badge":         nil,
	} {
		got, err := lib.Dependencies(name)
		if err != nil {
			t.Fatalf("Dependencies(%q): %v", name, err)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Dependencies(%q) = %v, want %v", name, got, want)
		}
	}

	// Every dependency of every component resolves: an import of a sibling
	// the library does not ship would compile nowhere but the project that
	// happens to have it.
	for _, entry := range lib.Registry() {
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		for _, m := range siblingImport.FindAllStringSubmatch(src, -1) {
			if _, ok := lib.GetEntry(m[1]); !ok {
				t.Errorf("%s imports ./%s, which is not a library component", entry.Name, m[1])
			}
		}
	}

	if _, err := lib.Dependencies("nonexistent"); err == nil {
		t.Error("Dependencies(nonexistent) should return an error")
	}
}

func TestLibraryGetEntry(t *testing.T) {
	lib := NewLibrary()

	entry, ok := lib.GetEntry("sidebar_left")
	if !ok {
		t.Fatal("sidebar_left should exist")
	}
	if entry.Category != CategoryLayouts {
		t.Errorf("sidebar_left category = %q, want layouts", entry.Category)
	}

	_, ok = lib.GetEntry("nonexistent")
	if ok {
		t.Error("nonexistent should not exist")
	}
}

func TestLibrarySearch(t *testing.T) {
	lib := NewLibrary()

	// Search by tag keyword
	results := lib.Search("deck")
	found := false
	for _, r := range results {
		if r.Name == "slide_title" {
			found = true
			break
		}
	}
	if !found {
		t.Error("search 'deck' should find slide_title")
	}

	// Search by category keyword
	results = lib.Search("charts")
	found = false
	for _, r := range results {
		if r.Name == "quadrant_chart" {
			found = true
			break
		}
	}
	if !found {
		t.Error("search 'charts' should find quadrant_chart")
	}

	// Search by name keyword
	results = lib.Search("funnel")
	found = false
	for _, r := range results {
		if r.Name == "funnel_chart" {
			found = true
			break
		}
	}
	if !found {
		t.Error("search 'funnel' should find funnel_chart")
	}

	// Multi-word search where EVERY word is matched by something: identical
	// to the old bag-of-words AND behaviour.
	results = lib.Search("crud admin")
	if len(results) == 0 {
		t.Error("search 'crud admin' should find components")
	}
	for _, r := range results {
		nameLower := strings.ToLower(r.Name)
		descLower := strings.ToLower(r.Description)
		tagStr := strings.ToLower(strings.Join(r.Tags, " "))
		catStr := string(r.Category)
		combined := nameLower + " " + descLower + " " + tagStr + " " + catStr
		if !strings.Contains(combined, "crud") || !strings.Contains(combined, "admin") {
			t.Errorf("search 'crud admin' returned %q which doesn't match both words", r.Name)
		}
	}

	// Search with no results
	results = lib.Search("xyznonexistent123")
	if len(results) != 0 {
		t.Errorf("search with no results should return empty, got %d", len(results))
	}

	// Empty search returns all
	results = lib.Search("")
	if len(results) != len(lib.Registry()) {
		t.Errorf("empty search should return all, got %d want %d", len(results), len(lib.Registry()))
	}
}

// TestLibrarySearchDoesNotShrinkAsTheQueryGetsMorePrecise pins the defect the
// dogfood run hit: `forge component search "dashboard metric stat tiles"`
// returned 0 results, and DROPPING a word ("tiles") returned metric_card and
// stat_grid. Under bag-of-words AND, describing your need more precisely
// empties the library — so the one unit that did search learned the library
// was empty and never searched again. Zero of 74 components were installed
// that run.
func TestLibrarySearchDoesNotShrinkAsTheQueryGetsMorePrecise(t *testing.T) {
	lib := NewLibrary()

	const query = "dashboard metric stat tiles"
	entries, matched, total := lib.SearchDetailed(query)
	if len(entries) == 0 {
		t.Fatalf("search %q returned nothing; a word the library doesn't carry must not empty the result set", query)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4 (the query's word count)", total)
	}
	if matched != 3 {
		t.Errorf("matched = %d, want 3 — %q is the only word no component carries", matched, "tiles")
	}
	if entries[0].Name != "metric_card" {
		t.Errorf("best match = %q, want metric_card (name hit on \"metric\" outranks a description hit)", entries[0].Name)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"metric_card", "stat_grid"} {
		if !names[want] {
			t.Errorf("search %q must surface %q; got %v", query, want, sortedNames(entries))
		}
	}

	// Dropping the unmatched word must not CHANGE the answer either — the
	// whole point is that the extra word only re-ranks, never destroys.
	narrower, _, _ := lib.SearchDetailed("dashboard metric stat")
	if len(narrower) != len(entries) {
		t.Errorf("dropping the unmatched word changed the result set: %v vs %v",
			sortedNames(narrower), sortedNames(entries))
	}

	// A query nothing matches at all is still empty: "no such component" has
	// to stay distinguishable from "here is the closest thing".
	empty, matched, total := lib.SearchDetailed("xyznonexistent123 qqzzwww")
	if len(empty) != 0 || matched != 0 || total != 2 {
		t.Errorf("SearchDetailed(no-match) = %d entries, matched %d, total %d; want 0, 0, 2",
			len(empty), matched, total)
	}
}

// TestLibrarySearchRanksNameHitsFirst pins the ordering half: within one hit
// count, a word in the component's NAME beats the same word in a description.
func TestLibrarySearchRanksNameHitsFirst(t *testing.T) {
	lib := NewLibrary()

	entries := lib.Search("pagination")
	if len(entries) == 0 {
		t.Fatal("search 'pagination' returned nothing")
	}
	if entries[0].Name != "pagination" {
		t.Errorf("search 'pagination' ranked %q first, want the component NAMED pagination; got %v",
			entries[0].Name, sortedNames(entries))
	}
}

func sortedNames(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

func TestLibraryList(t *testing.T) {
	lib := NewLibrary()

	// List all
	all := lib.List("", "")
	if len(all) != 83 {
		t.Errorf("list all should return 83 components, got %d", len(all))
	}

	// List filtered by category
	deck := lib.List("", "deck")
	if len(deck) != 7 {
		t.Errorf("list category=deck should return 7, got %d", len(deck))
	}
}

func TestLibraryFindSimilar(t *testing.T) {
	lib := NewLibrary()

	suggestions := lib.FindSimilar("slide")
	if len(suggestions) == 0 {
		t.Error("FindSimilar('slide') should return suggestions")
	}
}

// TestFormFieldAutoBinding pins the contract that the form/label/input/
// select primitives all participate in the FormFieldContext shape so a
// page-author can write `<FormField><Label/><Input/></FormField>` and
// get a correctly-bound htmlFor/id pair without writing either prop.
//
// The check is intentionally string-level on the embedded sources —
// these .tsx files only ever ship to user projects as text, never
// compile here — so a regression in the wiring (e.g. removing the
// `useContext(FormFieldContext)` line from Input) trips this test
// before users discover the broken auto-binding.
func TestFormFieldAutoBinding(t *testing.T) {
	lib := NewLibrary()

	ctxSrc, err := lib.Get("form_field_context")
	if err != nil {
		t.Fatalf("get form_field_context: %v", err)
	}
	if !strings.Contains(ctxSrc, "export const FormFieldContext") {
		t.Error("form_field_context.tsx must export FormFieldContext for auto-binding")
	}

	form, err := lib.Get("form")
	if err != nil {
		t.Fatalf("get form: %v", err)
	}
	// The context lives in its own module so form.tsx exports only
	// components (react-refresh/only-export-components).
	if strings.Contains(form, "createContext") {
		t.Error("form.tsx must import FormFieldContext from ./form_field_context, not create it")
	}
	if !strings.Contains(form, "React.useId()") {
		t.Error("form.tsx FormField must mint id via React.useId()")
	}
	if !strings.Contains(form, "FormFieldContext.Provider") {
		t.Error("form.tsx FormField must provide FormFieldContext")
	}

	label, err := lib.Get("label")
	if err != nil {
		t.Fatalf("get label: %v", err)
	}
	if !strings.Contains(label, "FormFieldContext") {
		t.Error("label.tsx must read FormFieldContext for auto-binding")
	}
	if !strings.Contains(label, "htmlFor ?? ctx?.id") {
		t.Error("label.tsx must fall back to ctx?.id when htmlFor is unset")
	}

	input, err := lib.Get("input")
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if !strings.Contains(input, "FormFieldContext") {
		t.Error("input.tsx must read FormFieldContext for auto-binding")
	}
	if !strings.Contains(input, "id ?? ctx?.id") {
		t.Error("input.tsx must fall back to ctx?.id when id is unset")
	}

	sel, err := lib.Get("select")
	if err != nil {
		t.Fatalf("get select: %v", err)
	}
	if !strings.Contains(sel, "FormFieldContext") {
		t.Error("select.tsx must read FormFieldContext for auto-binding")
	}
	if !strings.Contains(sel, "id ?? ctx?.id") {
		t.Error("select.tsx must fall back to ctx?.id when id is unset")
	}
}

func TestFormatComponentList(t *testing.T) {
	entries := []Entry{
		{Name: "test_chart", Category: CategoryCharts, Description: "A test chart", Tags: []string{"chart"}},
	}
	result := FormatComponentList(entries)
	if !strings.Contains(result, "Found 1 components") {
		t.Errorf("format should show count, got: %s", result)
	}
	if !strings.Contains(result, "test_chart") {
		t.Error("format should include component name")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Vapor references
// ─────────────────────────────────────────────────────────────────────────────

// externalComponentRefs names every `<Thing>` a library doc comment points at
// that is deliberately NOT part of this library, mapped to where it does live.
//
// This is a ledger, not an escape hatch. Adding an entry to silence a NEW
// finding is the exact failure TestComponentDocsNameOnlyRealComponents exists
// to catch — card.tsx advertised `<StatCards>` beside the real `<MetricCard>`,
// an agent went looking for it, found nothing, and hand-wrote its own metric
// tile rather than installing the metric_card that was sitting right there.
// A doc that names a component nobody can install costs more than no doc.
//
// TestExternalComponentRefsAreTight makes it a ratchet: an entry nothing
// references must be deleted.
var externalComponentRefs = map[string]string{
	"Resource": "@reliantlabs/forge-web-runtime — the wired list view over a Connect list RPC",
	"Field":    "headlessui — cited in label.tsx as prior art for the FormField context pattern",
}

// TestComponentDocsNameOnlyRealComponents fails when a component's doc comment
// advertises a `<Component>` the library does not export.
//
// Scope is deliberately the JSX-reference spelling inside COMMENTS. That is a
// promise to the reader — "install this, it exists" — and it is mechanically
// checkable, unlike prose. Refs outside comments are generics and DOM types
// (`useState<Rect>`, `React.FC<HTMLDivElement>`), which TypeScript already
// resolves.
func TestComponentDocsNameOnlyRealComponents(t *testing.T) {
	lib := NewLibrary()
	exported := libraryExports(t, lib)
	if len(exported) < 50 {
		t.Fatalf("only %d exported symbols found across the library — the export scan broke, "+
			"and a scan that sees nothing approves everything", len(exported))
	}

	refs := 0
	for _, entry := range lib.Registry() {
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		for _, name := range jsxRefsInComments(src) {
			refs++
			if exported[name] {
				continue
			}
			if where, external := externalComponentRefs[name]; external {
				if !strings.Contains(src, where[:strings.IndexByte(where, ' ')]) {
					t.Errorf("%s.tsx cites <%s> as external (%s) but never names its source in the file",
						entry.Name, name, where)
				}
				continue
			}
			t.Errorf("%s.tsx documents <%s>, which this library does not export.\n"+
				"  Either ship it, or point the reader at the component that DOES exist.\n"+
				"  A reader who goes looking and finds nothing hand-writes a replacement for\n"+
				"  something already on the shelf — that is how the library gets bypassed.",
				entry.Name, name)
		}
	}
	if refs == 0 {
		t.Fatal("no <Component> references found in any doc comment — the comment scan broke, " +
			"so this guard is approving everything")
	}
}

// TestExternalComponentRefsAreTight deletes an externalComponentRefs entry's
// right to exist once nothing cites it, so the ledger can only shrink.
func TestExternalComponentRefsAreTight(t *testing.T) {
	lib := NewLibrary()
	cited := map[string]bool{}
	for _, entry := range lib.Registry() {
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		for _, name := range jsxRefsInComments(src) {
			cited[name] = true
		}
	}
	for name, where := range externalComponentRefs {
		if !cited[name] {
			t.Errorf("externalComponentRefs lists %q (%s) but no doc comment cites <%s> any more — delete the entry", name, where, name)
		}
	}
}

// libraryExports collects every identifier the library's .tsx files export.
func libraryExports(t *testing.T, lib *Library) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, entry := range lib.Registry() {
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		for _, re := range []*regexp.Regexp{exportDecl, exportDefaultIdent} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				out[m[len(m)-1]] = true
			}
		}
	}
	return out
}

var (
	exportDecl = regexp.MustCompile(`(?m)^export\s+(?:default\s+)?(?:async\s+)?(?:function|const|let|var|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`)
	// `const Input = forwardRef(…)` … `export default Input;` — the shape every
	// ref-forwarding primitive in this library uses.
	exportDefaultIdent = regexp.MustCompile(`(?m)^export\s+default\s+([A-Za-z_$][\w$]*)\s*;`)
	jsxRef             = regexp.MustCompile(`<([A-Z][A-Za-z0-9]*)>`)
)

// jsxRefsInComments returns the `<Component>` names a file's comments mention.
// Comment lines are recognised structurally (a line whose first non-space runs
// are `//`, `/*` or a JSDoc continuation `*`), which is every comment form this
// library uses and cannot mistake a URL inside a string for one.
func jsxRefsInComments(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "/*") && !strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, m := range jsxRef.FindAllStringSubmatch(trimmed, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Seams the scaffold itself needs
//
// Both tests below pin a PUBLIC PROP SIGNATURE, which is the component's own
// contract and the only thing a caller can rely on. They exist because the
// same failure happened twice in one scaffold: forge shipped a component, the
// component could not express what a generated page needed, and the page
// hand-rolled a replacement — so the library's install count stayed at zero
// while its markup was reimplemented three times.
// ─────────────────────────────────────────────────────────────────────────────

// TestPageChromeTakesComposedContent: a header or banner whose text prop is
// `string` cannot carry a badge, a `<code>` id, a count, or a link inside the
// sentence — and the caller's only way out is to rebuild the typography by
// hand, which three pages in one run did. ReactNode is this library's idiom
// for a caller-composed slot (CardHeader.title, Chip.label, ProgressBar.label,
// Status.label) and a plain string still satisfies it, so widening costs
// no call site.
func TestPageChromeTakesComposedContent(t *testing.T) {
	lib := NewLibrary()
	want := map[string][]string{
		"page_header":  {"title", "subtitle"},
		"alert_banner": {"title", "message"},
	}
	for name, props := range want {
		src, err := lib.Get(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		for _, prop := range props {
			if !propTypeIs(src, prop, "React.ReactNode") {
				t.Errorf("%s.tsx: prop %q must be React.ReactNode, not a bare string.\n"+
					"  A string prop makes the component unusable the moment the title needs\n"+
					"  anything but text, and the caller reimplements the whole block.", name, prop)
			}
		}
	}
}

// TestAlertBannerRootIsAddressable: a banner whose root takes no attributes
// cannot be named, focused or found. An error summary that must take focus
// when a submit fails (and be announced by its title) had to wrap AlertBanner
// in a second role="alert" box — announced twice — or rebuild it. The root
// takes every <div> attribute and a ref, as Card's and Input's do.
func TestAlertBannerRootIsAddressable(t *testing.T) {
	src, err := NewLibrary().Get("alert_banner")
	if err != nil {
		t.Fatalf("get alert_banner: %v", err)
	}
	for _, want := range []string{
		`extends Omit<React.HTMLAttributes<HTMLDivElement>, "title">`,
		`React.forwardRef<HTMLDivElement, AlertBannerProps>`,
		`ref={ref}`,
		`{...rest}`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("alert_banner.tsx: missing %q — the banner's root must take a ref and every <div> attribute", want)
		}
	}
}

// TestListChromeCanBeControlled: forge's generated list pages read their
// filters from the URL (useTypedSearchParams), so a component that OWNS its
// selection cannot participate in the convention forge ships — the URL and the
// control disagree on the first deep link, refresh, or Back. Tabs and FilterBar
// were uncontrolled-only, and a page that needed URL-as-truth hand-rolled its
// own tab bar. Shipping a convention and a component that cannot follow it is
// the defect; the controlled prop is the seam.
func TestListChromeCanBeControlled(t *testing.T) {
	lib := NewLibrary()
	for name, prop := range map[string]string{
		"tabs":       "activeTab",
		"filter_bar": "values",
	} {
		src, err := lib.Get(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if !strings.Contains(src, prop+"?:") {
			t.Errorf("%s.tsx declares no optional %q prop, so a parent cannot own the state.\n"+
				"  Generated pages hold this in the URL; a component that self-updates cannot follow.",
				name, prop)
		}
		// Controlled means the component does NOT self-update when the parent
		// supplies the value. A `setX` unconditional on every change is the
		// uncontrolled-only shape wearing a controlled prop's name.
		if !strings.Contains(src, "controlled") {
			t.Errorf("%s.tsx accepts %q but never branches on whether it is controlled — "+
				"an internal setState that always fires makes the parent's value advisory.", name, prop)
		}
	}
}

// propTypeIs reports whether src declares `prop: <typ>` or `prop?: <typ>` in an
// interface body.
func propTypeIs(src, prop, typ string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, decl := range []string{prop + ": ", prop + "?: "} {
			if strings.HasPrefix(trimmed, decl) {
				return strings.TrimSuffix(strings.TrimPrefix(trimmed, decl), ";") == typ
			}
		}
	}
	return false
}

// TestComponentsUseThemeTokensNotPaletteLiterals pins the library to the
// SEMANTIC palette every forge frontend scaffolds (--color-ink, --color-surface,
// --color-accent, --color-danger/success/warning and their -surface/-border/-ink
// variants), rather than raw Tailwind palette classes.
//
// Why this is a hard rule and not a preference: a component that ships
// `bg-blue-600` cannot be installed into a project whose accent is orange
// without rewriting every className on it. Measured in a real build — three
// separate agents fetched `empty_state`, and all three hand-wrote their own
// instead, because adapting the palette cost more than writing eight lines. A
// library nobody can install is a library nobody uses.
//
// Two deliberate exemptions, both because the colour IS DATA rather than chrome:
//   - categories diagrams/ and deck/ — chart series and node fills
//   - the identity palettes (avatar initials, badge rotation), which encode a
//     value by hue; theming them collapses the rotation to one colour
func TestComponentsUseThemeTokensNotPaletteLiterals(t *testing.T) {
	paletteRE := regexp.MustCompile(
		`\b(bg|text|border|ring|divide|placeholder|from|to|via|fill|stroke|outline|shadow|accent|caret|decoration)` +
			`-(gray|blue|red|green|slate|zinc|neutral|stone|emerald|amber|yellow|indigo|purple|orange|sky|rose|violet|teal|cyan|lime|fuchsia|pink)` +
			`-[0-9]{2,3}\b`)

	exemptCategory := map[string]bool{"diagrams": true, "deck": true}
	exemptFile := map[string]bool{"avatar": true, "detail_view": true}

	lib := NewLibrary()
	checked := 0
	for _, entry := range lib.Registry() {
		if exemptCategory[string(entry.Category)] || exemptFile[entry.Name] {
			continue
		}
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		checked++
		if found := paletteRE.FindAllString(src, -1); len(found) > 0 {
			seen := map[string]bool{}
			var uniq []string
			for _, f := range found {
				if !seen[f] {
					seen[f] = true
					uniq = append(uniq, f)
				}
			}
			t.Errorf("%s (%s) ships hardcoded palette classes %v — use the scaffolded theme tokens "+
				"(ink / ink-muted / ink-subtle, surface / surface-muted, border / border-strong, "+
				"accent, danger, success, warning and their -surface/-border/-ink variants) so the "+
				"component installs into ANY project's palette without a rewrite",
				entry.Name, entry.Category, uniq)
		}
	}
	if checked == 0 {
		t.Fatal("checked zero components — the library or its category filter is broken, and an empty set cannot fail")
	}
	t.Logf("verified %d components carry no palette literals", checked)
}
