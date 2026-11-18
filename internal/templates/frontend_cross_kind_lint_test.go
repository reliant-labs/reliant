// frontend_cross_kind_lint_test.go — files that ship BYTE-IDENTICAL into
// more than one frontend kind must lint under EVERY kind's config.
//
// Born red on a real scaffold. `forge scaffold frontend admin --kind
// vite-spa` produced a tree that failed `npm run lint` out of the box with
// three hard errors:
//
//	src/components/ui/avatar.tsx:59   Definition for rule
//	  '@next/next/no-img-element' was not found
//	src/components/ui/progress_bar.tsx:1     Definition for rule
//	  'react/forbid-dom-props' was not found
//	src/components/ui/skeleton_loader.tsx:1  Definition for rule
//	  'react/forbid-dom-props' was not found
//
// Cause: the component library's copies carried in-file `eslint-disable`
// directives naming rules from plugins only the NEXT.JS config loads. ESLint
// treats a directive that names an undefined rule as an error, so the exact
// same bytes were a warning under Next.js (where the rules are defined and
// already exempted by directory scope, making the directive redundant) and
// fatal under Vite.
//
// The invariant: an in-file eslint directive is a per-file suppression that
// travels with the bytes, so it may only name rules EVERY browser scaffold
// resolves. Framework-specific suppressions belong in that framework's own
// eslint config, scoped by directory — which is where the Next.js scaffold
// already puts these two.
package templates

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/components"
)

// eslintDirective matches the four in-file suppression spellings —
// eslint-disable, eslint-disable-line, eslint-disable-next-line,
// eslint-enable. The rule list that follows is carved out by directiveRules
// rather than by this pattern, because rule names contain "/" and so cannot
// be delimited by a character class that also has to stop at "*/".
var eslintDirective = regexp.MustCompile(`eslint-(?:disable|enable)(?:-next-line|-line)?\b`)

// crossKindESLintNamespaces is the set of plugin namespaces EVERY browser
// frontend kind's eslint config loads, and therefore the only namespaces a
// shared file's in-file directive may name.
//
// Derived from the Vite SPA config (the narrower of the two) — it is the
// intersection, because the Next.js config is a superset. The empty string
// stands for ESLint core rules ("no-console", "eqeqeq", …), which every
// config resolves via js.configs.recommended.
// TestViteESLintConfigStillLoadsOnlyTheSharedNamespaces below fails if the
// Vite config grows a plugin, so this list cannot silently go stale.
var crossKindESLintNamespaces = map[string]bool{
	"":                   true, // ESLint core
	"@typescript-eslint": true,
	"jsx-a11y":           true,
	"react-hooks":        true,
	"react-refresh":      true,
}

// ruleNamespace returns the plugin namespace of an eslint rule name:
// "react/forbid-dom-props" -> "react", "@next/next/no-img-element" ->
// "@next/next", "no-console" -> "" (core).
func ruleNamespace(rule string) string {
	i := strings.LastIndex(rule, "/")
	if i < 0 {
		return ""
	}
	return rule[:i]
}

// directiveRules extracts every rule name named by an in-file eslint
// directive in src, in source order. A directive's rule list runs from the
// end of the keyword to whichever comes first: the end of the line, the end
// of the block comment, or the ` -- ` description separator. A bare
// `/* eslint-disable */` names no rules and contributes nothing.
func directiveRules(src string) []string {
	var out []string
	for _, loc := range eslintDirective.FindAllStringIndex(src, -1) {
		rest := src[loc[1]:]
		for _, stop := range []string{"\n", "*/", "--"} {
			if i := strings.Index(rest, stop); i >= 0 {
				rest = rest[:i]
			}
		}
		for _, rule := range strings.Split(rest, ",") {
			rule = strings.TrimSpace(rule)
			if rule == "" {
				continue
			}
			out = append(out, rule)
		}
	}
	return out
}

// TestSharedFrontendFilesCarryNoKindSpecificESLintDirectives is the direct
// guard on the defect above: every file that ships byte-identical into both
// the Next.js and the Vite SPA tree — the component library installed at
// src/components/ui/**, plus the shared/ and shared-web/ template roots —
// may only suppress rules both configs resolve.
func TestSharedFrontendFilesCarryNoKindSpecificESLintDirectives(t *testing.T) {
	t.Parallel()

	check := func(t *testing.T, label, src string) {
		t.Helper()
		for _, rule := range directiveRules(src) {
			ns := ruleNamespace(rule)
			if crossKindESLintNamespaces[ns] {
				continue
			}
			t.Errorf("%s: in-file eslint directive names %q, whose plugin namespace %q is not loaded by every browser frontend kind.\n"+
				"This file ships byte-identical into both frontends/<n>/ trees, and eslint FAILS on a directive naming an undefined rule — "+
				"so the suppression that is merely redundant under Next.js is a hard error under Vite.\n"+
				"Fix: drop the in-file directive and scope the exemption in that framework's own eslint.config.mjs by directory.",
				label, rule, ns)
		}
	}

	// The component library — installed verbatim into src/components/ui/ of
	// every browser frontend by generator.installCoreComponents.
	lib := components.NewLibrary()
	for _, entry := range lib.Registry() {
		if entry.Category != components.CategoryUI {
			continue
		}
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get component %s: %v", entry.Name, err)
		}
		check(t, "components/ui/"+entry.Name+".tsx", src)
	}

	// The shared template roots — rendered into every browser frontend by
	// FrontendTemplateRoots("nextjs") and ("vite-spa") alike.
	for _, root := range []string{"shared", "shared-web"} {
		files, err := listTemplates(filepath.Join("frontend", root), true)
		if err != nil {
			t.Fatalf("list %s: %v", root, err)
		}
		for _, rel := range files {
			raw, err := FrontendTemplates().Get(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s/%s: %v", root, rel, err)
			}
			check(t, root+"/"+rel, string(raw))
		}
	}
}

// TestEveryBrowserKindRunsTheSharedDOMTests is the same defect shape one
// layer up: shared-web/ contributes component tests that MOUNT INTO THE DOM
// (status-badge, entity-picker, and the runtime harness in test-utils), and
// both browser kinds ship a `test` script plus a jsdom devDependency to run
// them. Only Next.js declared the jsdom ENVIRONMENT, so `npm test` in a
// freshly-scaffolded Vite SPA failed on arrival with 13 x "document is not
// defined". Byte-identical test files are worth nothing if only one kind has
// the harness to execute them.
func TestEveryBrowserKindRunsTheSharedDOMTests(t *testing.T) {
	t.Parallel()

	// The shared tests that need a DOM, and therefore create the obligation.
	sharedDOMTests := 0
	files, err := listTemplates(filepath.Join("frontend", "shared-web"), true)
	if err != nil {
		t.Fatalf("list shared-web: %v", err)
	}
	for _, rel := range files {
		if strings.Contains(rel, ".test.") {
			sharedDOMTests++
		}
	}
	if sharedDOMTests == 0 {
		t.Skip("shared-web contributes no tests — nothing to run in either kind")
	}

	// The file vitest resolves its config from, per kind. Next.js carries a
	// dedicated vitest.config.ts; a Vite SPA has vitest read vite.config.ts.
	for kind, configRel := range map[string]string{
		"nextjs":   filepath.Join("nextjs", "vitest.config.ts"),
		"vite-spa": filepath.Join("vite-spa", "vite.config.ts.tmpl"),
	} {
		pkgRel := filepath.Join(kind, "package.json.tmpl")
		pkg, err := FrontendTemplates().Get(pkgRel)
		if err != nil {
			t.Fatalf("read %s: %v", pkgRel, err)
		}
		if !strings.Contains(string(pkg), `"test":`) {
			continue // this kind does not claim to run tests at all
		}

		cfg, err := FrontendTemplates().Get(configRel)
		if err != nil {
			t.Errorf("%s ships a `test` script but %s is missing — vitest has nowhere to read the jsdom environment from", kind, configRel)
			continue
		}
		if !strings.Contains(string(cfg), `environment: "jsdom"`) {
			t.Errorf("%s ships a `test` script and inherits %d DOM test(s) from shared-web/, but %s does not declare `environment: \"jsdom\"`.\n"+
				"`npm test` in a fresh %s frontend fails on arrival with \"document is not defined\".",
				kind, sharedDOMTests, configRel, kind)
		}
	}
}

// TestViteESLintConfigStillLoadsOnlyTheSharedNamespaces pins the derivation
// of crossKindESLintNamespaces to the config it was read off. If the Vite
// scaffold grows a plugin, widen the allowlist deliberately rather than
// letting the guard above quietly under-approximate.
func TestViteESLintConfigStillLoadsOnlyTheSharedNamespaces(t *testing.T) {
	t.Parallel()

	rel := filepath.Join("vite-spa", "eslint.config.mjs")
	raw, err := FrontendTemplates().Get(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}

	// The flat config declares its non-core plugins in a `plugins: { ... }`
	// block as quoted namespace keys.
	block := regexp.MustCompile(`(?s)plugins:\s*\{(.*?)\}`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatalf("%s: no `plugins: { ... }` block — the derivation this guard pins has moved", rel)
	}
	got := map[string]bool{"": true, "@typescript-eslint": true} // js.configs.recommended + tseslint.configs.recommended
	for _, m := range regexp.MustCompile(`"([^"]+)"\s*:`).FindAllStringSubmatch(block[1], -1) {
		got[m[1]] = true
	}

	if !equalStringSets(got, crossKindESLintNamespaces) {
		t.Errorf("%s loads plugin namespaces %v, but crossKindESLintNamespaces is %v.\n"+
			"Update crossKindESLintNamespaces to match — it is the intersection every shared frontend file's "+
			"in-file eslint directives are checked against.", rel, sortedKeys(got), sortedKeys(crossKindESLintNamespaces))
	}
}

// a11yRulesBlock carves the `const a11yRules = { … };` literal out of a flat
// eslint config. The terminator is a `};` at column 0, which is the only
// place one can appear in this file: every nested object inside the literal
// is indented.
var a11yRulesBlock = regexp.MustCompile(`(?ms)^const a11yRules = \{.*?^\};`)

// TestBothESLintConfigsShareOneA11yBlock keeps the accessibility base
// IDENTICAL across the two browser scaffolds, and it is the guard the
// extraction seam in both configs names.
//
// Two reasons it has to hold. First, correctness today: the component
// library at src/components/ui/**, and every file under shared-web/, ship
// the SAME BYTES into both trees. A rule enabled in one config and not the
// other makes one file clean in the Next.js frontend and red in the Vite
// one — the same defect shape TestSharedFrontendFilesCarryNoKindSpecificESLintDirectives
// above was born on, one layer up.
//
// Second, the migration: this block is duplicated ONLY because
// eslint.config.mjs is scaffold-once and there is no shared package for it
// to import yet. Two copies that agree can be replaced by one import
// mechanically; two copies that have drifted have to be reconciled first,
// by someone who no longer remembers which difference was deliberate.
func TestBothESLintConfigsShareOneA11yBlock(t *testing.T) {
	t.Parallel()

	blocks := map[string]string{}
	for _, kind := range []string{"nextjs", "vite-spa"} {
		rel := filepath.Join(kind, "eslint.config.mjs")
		raw, err := FrontendTemplates().Get(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		block := a11yRulesBlock.FindString(string(raw))
		if block == "" {
			t.Fatalf("%s: no `const a11yRules = { … };` literal.\n"+
				"The accessibility base must stay a NAMED const spread into the rules object, not rules "+
				"inlined among the rest — that name is what makes swapping it for an import of a shared "+
				"eslint config package a one-line change instead of a rewrite.", rel)
		}
		blocks[kind] = block
	}

	if blocks["nextjs"] != blocks["vite-spa"] {
		t.Errorf("the a11yRules block differs between nextjs/eslint.config.mjs and vite-spa/eslint.config.mjs.\n"+
			"Files under shared-web/ and the src/components/ui/** library ship byte-identical into BOTH trees, "+
			"so a rule in one config and not the other makes the same file clean in one frontend and red in the "+
			"other. Keep the two copies identical until they can be replaced by a single imported base.\n"+
			"--- nextjs ---\n%s\n--- vite-spa ---\n%s", blocks["nextjs"], blocks["vite-spa"])
	}

	// A block that stopped naming any rule would satisfy the equality check
	// while gating nothing.
	if !strings.Contains(blocks["nextjs"], `"jsx-a11y/`) {
		t.Errorf("the a11yRules block names no jsx-a11y rule — the equality assertion above is vacuous:\n%s", blocks["nextjs"])
	}
}

func equalStringSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
