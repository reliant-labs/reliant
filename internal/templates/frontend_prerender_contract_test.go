// frontend_prerender_contract_test.go — the two scaffold defects that a
// typecheck, a lint run and even a passing `next build` all fail to catch.
//
// Both were found by diffing a real project against the template it was
// scaffolded from, and both are the same shape of bug: the scaffold looked
// fine because something ELSE happened to cover for it.
//
//   - The sign-in route calls useSearchParams() with no Suspense boundary of
//     its own. `next build` survives that today only because the scaffold also
//     ships src/app/loading.tsx, and Next turns a root loading.tsx into an
//     implicit boundary. Delete loading.tsx — an obvious edit, it is `yours`
//     code — and the build fails outright: "useSearchParams() should be
//     wrapped in a suspense boundary at page /auth/sign-in". Even while it
//     builds, the root boundary catches the bailout for the WHOLE page, so the
//     prerendered HTML ships the loading skeleton instead of the sign-in
//     screen.
//   - layout.tsx set no background on <body>. The palette flips with the OS
//     preference, so with nothing painting the page the dark palette's
//     near-white ink lands on the browser's default white sheet: measured
//     1.12:1, invisible. The scaffolded shell masked it by painting the same
//     tokens on a wrapper div — accidental cover that the first restyle drops.
//
// These are static string guards on purpose: they must hold in CI with no node
// toolchain. The live proof is the acceptance run in the fix's report (fresh
// scaffold, loading.tsx removed, `next build` green; contrast measured in
// Chrome at 17.09:1 dark / 16.81:1 light).
package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// signInPageTemplate is the App Router sign-in route.
const signInPageTemplate = "nextjs/src/app/auth/sign-in/page.tsx.tmpl"

// TestSignInRouteOwnsItsSuspenseBoundary pins the boundary to the sign-in
// route itself, rather than leaving it to whatever ancestor happens to exist.
func TestSignInRouteOwnsItsSuspenseBoundary(t *testing.T) {
	src, err := FrontendTemplates().Render(
		filepath.FromSlash(signInPageTemplate),
		FrontendTemplateData{FrontendName: "web", ProjectName: "demo"},
	)
	if err != nil {
		t.Fatalf("render %s: %v", signInPageTemplate, err)
	}
	s := string(src)

	// The route reads the query string; that is the whole reason it needs a
	// boundary. If this ever stops being true the rest of the test is moot.
	if !strings.Contains(s, "useSearchParams") {
		t.Skip("sign-in no longer calls useSearchParams(); boundary requirement moot")
	}
	// Matched as "Suspense appears in the named-import list from react",
	// not as one exact import line. The previous spelling pinned the whole
	// clause — `import { Suspense, useCallback } from "react"` — so it went
	// red the day the page swapped useCallback for useState, an edit that
	// changed nothing this test is about. A guard that fires on an unrelated
	// refactor is one the next person deletes rather than reads, and this
	// assertion is worth keeping: the real property is that the boundary's
	// import is present at all.
	reactImport := regexp.MustCompile(`import\s*\{([^}]*)\}\s*from\s*"react"`)
	imported := reactImport.FindStringSubmatch(s)
	if imported == nil {
		t.Fatalf("sign-in page has no named import from \"react\"; got:\n%s", s)
	}
	if !regexp.MustCompile(`\bSuspense\b`).MatchString(imported[1]) {
		t.Errorf("sign-in page must import Suspense from react (imports %q) — useSearchParams() "+
			"bails out of prerendering and `next build` fails the route unless the "+
			"bailout is caught by a boundary this file owns", strings.TrimSpace(imported[1]))
	}
	if !strings.Contains(s, "<Suspense") {
		t.Fatalf("sign-in page must wrap its useSearchParams() consumer in <Suspense>; got:\n%s", s)
	}

	// The boundary has to sit ABOVE the component that reads the params. The
	// default export is what Next renders, so that is where it belongs — a
	// <Suspense> nested inside the reader catches nothing.
	def := strings.Index(s, "export default function")
	sus := strings.Index(s, "<Suspense")
	if def < 0 {
		t.Fatalf("sign-in page has no default export; got:\n%s", s)
	}
	if sus < def {
		t.Errorf("<Suspense> must be inside the DEFAULT EXPORT, wrapping the component "+
			"that calls useSearchParams(); found it at offset %d, before the default "+
			"export at %d — a boundary below the reader does not catch its bailout", sus, def)
	}
	if strings.Contains(s[def:], "useSearchParams") {
		t.Errorf("the default export must not call useSearchParams() itself — it has to " +
			"render a CHILD that does, or the bailout escapes the boundary and " +
			"`next build` fails the route")
	}
}

// TestScaffoldedPagePaintsItsOwnBackground guards fix 2 across both web
// flavors: the page must carry the semantic surface/ink pair, and it must do
// so somewhere a shell restyle cannot remove.
func TestScaffoldedPagePaintsItsOwnBackground(t *testing.T) {
	// The stylesheet floor. This is the guarantee that survives an app shell
	// rewrite, which is exactly what forge's own docs tell users to do.
	for kind, cssPath := range map[string]string{
		"nextjs":   filepath.Join("nextjs", "src", "app", "globals.css"),
		"vite-spa": filepath.Join("vite-spa", "src", "index.css"),
	} {
		css, err := FrontendTemplates().Get(cssPath)
		if err != nil {
			t.Fatalf("read %s: %v", cssPath, err)
		}
		got := string(css)
		// Normalize whitespace so the assertion is about the rule, not its
		// formatting.
		flat := strings.Join(strings.Fields(got), " ")
		const wantRule = "body { background-color: var(--color-surface); color: var(--color-ink); }"
		if !strings.Contains(flat, wantRule) {
			t.Errorf("%s: must paint the page itself with %q inside @layer base.\n"+
				"The palette flips with prefers-color-scheme, so a document that paints "+
				"no background leaves the dark palette's near-white ink on the browser's "+
				"white canvas — measured 1.12:1, invisible. An app-shell div painting the "+
				"same tokens is not a substitute: it is `yours` code and never covers the "+
				"overscroll gutter.", kind, wantRule)
		}
		if !strings.Contains(got, "@layer base") {
			t.Errorf("%s: the body paint must live in @layer base so ordinary utility "+
				"classes still override it", kind)
		}
	}

	// Next.js additionally sets the tokens on <body> in layout.tsx. Belt and
	// braces: this is the spelling a reader sees first, and it keeps the
	// scaffold honest about which tokens the page is built on.
	layout, err := FrontendTemplates().Render(
		filepath.FromSlash("nextjs/src/app/layout.tsx.tmpl"),
		FrontendTemplateData{FrontendName: "web", ProjectName: "demo"},
	)
	if err != nil {
		t.Fatalf("render nextjs layout: %v", err)
	}
	body := string(layout)
	open := strings.Index(body, "<body")
	if open < 0 {
		t.Fatalf("nextjs layout has no <body>; got:\n%s", body)
	}
	end := strings.Index(body[open:], ">")
	if end < 0 {
		t.Fatalf("nextjs layout <body> tag is unterminated")
	}
	bodyTag := body[open : open+end]
	for _, tok := range []string{"bg-surface", "text-ink"} {
		if !strings.Contains(bodyTag, tok) {
			t.Errorf("nextjs layout <body> must carry %q (got %q) — without a painted "+
				"page the dark palette renders near-white ink on white", tok, bodyTag)
		}
	}
}
