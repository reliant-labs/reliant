package staticexport

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// appRoots are where the App Router lives, relative to the frontend dir.
var appRoots = []string{"src/app", "app"}

var (
	dynamicSegmentRE = regexp.MustCompile(`^\[(\[?\.\.\.)?[^\[\]/]+\]?\]$`)
	gspExportRE      = regexp.MustCompile(`\bexport\s+(?:async\s+)?function\s+generateStaticParams\b|\bexport\s+(?:const|let|var)\s+generateStaticParams\b|\bexport\s*\{[^}]*\bgenerateStaticParams\b`)
	useClientRE      = regexp.MustCompile(`^\s*["']use client["']`)
	useServerLineRE  = regexp.MustCompile(`(?m)^[ \t]*["']use server["'][ \t]*;?[ \t]*$`)
	methodFuncRE     = regexp.MustCompile(`\bexport\s+(?:async\s+)?function\s+(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\b|\bexport\s+(?:const|let|var)\s+(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\b`)
	exportListRE     = regexp.MustCompile(`\bexport\s*\{([^}]*)\}`)
	forceStaticRE    = regexp.MustCompile(`\bexport\s+const\s+dynamic\s*=\s*["']force-static["']`)
	forceDynamicRE   = regexp.MustCompile(`\bexport\s+const\s+dynamic\s*=\s*["']force-dynamic["']`)
	revalidateRE     = regexp.MustCompile(`\bexport\s+const\s+revalidate\s*=\s*([^;\n]+)`)
	devGuardRE       = regexp.MustCompile(`if\s*\(\s*process\.env\.NODE_ENV\s*(===?\s*["']production["']|!==?\s*["']development["'])\s*\)\s*\{?\s*return\b`)
	exportDefaultRE  = regexp.MustCompile(`\bexport\s+default\b`)
	// scaffoldPageRE is the header forge's CRUD page templates write
	// (internal/templates/frontend/pages): "Detail page emitted by `forge
	// scaffold page`".
	scaffoldPageRE = regexp.MustCompile("(List|Detail|Create|Edit) page emitted by `forge scaffold page`")
)

// routeFile is one App Router special file.
type routeFile struct {
	src *sourceFile
	// segments are the directories between the app root and the file.
	segments []string
	// kind is the special-file name without extension: page, route, layout…
	kind string
	// root is the app directory it lives under ("src/app" or "app").
	root string
}

// appRouteFiles indexes the frontend's App Router files.
func (c *checker) appRouteFiles() []routeFile {
	var out []routeFile
	for _, f := range c.files {
		for _, root := range appRoots {
			if !strings.HasPrefix(f.rel, root+"/") {
				continue
			}
			inner := strings.TrimPrefix(f.rel, root+"/")
			dir, base := path.Split(inner)
			kind := strings.TrimSuffix(base, path.Ext(base))
			var segs []string
			if d := strings.Trim(dir, "/"); d != "" {
				segs = strings.Split(d, "/")
			}
			if private(segs) {
				break
			}
			out = append(out, routeFile{src: f, segments: segs, kind: kind, root: root})
			break
		}
	}
	return out
}

// private reports a path under an `_folder`, which the App Router excludes
// from routing entirely. `%5Ffolder` is the escape for a URL segment that
// really starts with an underscore, and IS routed.
func private(segs []string) bool {
	for _, s := range segs {
		if strings.HasPrefix(s, "_") {
			return true
		}
	}
	return false
}

// urlOf renders a route's URL pattern: route groups `(g)` and parallel slots
// `@s` are not URL segments, and `%5F` is an escaped underscore.
func urlOf(segs []string) string {
	var parts []string
	for _, s := range segs {
		if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") || strings.HasPrefix(s, "@") {
			continue
		}
		parts = append(parts, strings.ReplaceAll(s, "%5F", "_"))
	}
	return "/" + strings.Join(parts, "/")
}

// checkRoutes covers everything the App Router's special files declare:
// dynamic segments, route handlers, and segment config.
func (c *checker) checkRoutes() {
	files := c.appRouteFiles()
	// gspDirs: the directories (as joined segment paths) holding a layout
	// that exports generateStaticParams.
	gspLayouts := map[string]bool{}
	for _, rf := range files {
		if rf.kind == "layout" && gspExportRE.MatchString(rf.src.code) {
			gspLayouts[strings.Join(rf.segments, "/")] = true
		}
	}
	for _, rf := range files {
		switch rf.kind {
		case "page", "route":
			c.checkDynamicSegments(rf, gspLayouts)
		}
		if rf.kind == "route" {
			c.checkRouteHandler(rf)
		}
		switch rf.kind {
		case "page", "route", "layout", "template", "default":
			c.checkSegmentConfig(rf)
		}
	}
}

// checkDynamicSegments: every dynamic segment on a page's path needs its
// values at BUILD time. generateStaticParams supplies them — from the page
// itself, or from a layout at or below the segment.
func (c *checker) checkDynamicSegments(rf routeFile, gspLayouts map[string]bool) {
	if gspExportRE.MatchString(rf.src.code) {
		return
	}
	var uncovered []string
	for i, seg := range rf.segments {
		if !dynamicSegmentRE.MatchString(seg) {
			continue
		}
		covered := false
		for j := i; j < len(rf.segments); j++ {
			if gspLayouts[strings.Join(rf.segments[:j+1], "/")] {
				covered = true
				break
			}
		}
		if !covered {
			uncovered = append(uncovered, seg)
		}
	}
	if len(uncovered) == 0 {
		return
	}
	line := 1
	if loc := exportDefaultRE.FindStringIndex(rf.src.code); loc != nil {
		line = rf.src.lineAt(loc[0])
	}
	url := urlOf(rf.segments)
	file := c.rel(rf.src.rel)
	msg := fmt.Sprintf("%s is a dynamic route (%s) with no generateStaticParams: an export renders every page at build time, so `next build` refuses it (\"Page %q is missing generateStaticParams()\")",
		url, strings.Join(uncovered, ", "), url)
	c.add(RuleDynamicSegment, finding.SeverityError, file, line, msg, c.dynamicSegmentFix(rf, uncovered)+authoritativeNote)
}

// dynamicSegmentFix names the remedy that fits the page in front of us.
func (c *checker) dynamicSegmentFix(rf routeFile, uncovered []string) string {
	queryForm := fmt.Sprintf("move the id from the path into the query string (`%s?%s=…`, read with useSearchParams) — a static host serves ONE html file for every id, so a value that only exists at runtime (a record in your API) belongs there",
		urlOf(queryRoute(rf.segments)), paramName(uncovered[len(uncovered)-1]))
	if kind := scaffoldCRUDKind(rf); kind != "" {
		// The entity's route dir: src/app/books for
		// src/app/books/[id]/edit/page.tsx.
		slugDir := c.rel(path.Join(rf.root, rf.segments[0]))
		return scaffoldPageRemedy(slugDir, kind)
	}
	if useClientRE.MatchString(rf.src.code) {
		return "if the values are known at build time, export generateStaticParams from a server `layout.tsx` in the segment's directory — this page is 'use client' and cannot export it; otherwise " + queryForm
	}
	return "if the values are known at build time, `export async function generateStaticParams()` returning every one; otherwise " + queryForm
}

// scaffoldCRUDKind reports "detail" or "edit" for a page born from forge's
// CRUD templates, "" otherwise. Two signals, either sufficient:
//
//   - the template's own header ("Detail page emitted by `forge scaffold
//     page`") — the page is probably untouched;
//   - the scaffold-once banner on a page at exactly the template's path,
//     `<slug>/[id]/page.tsx` or `<slug>/[id]/edit/page.tsx` — born there and
//     since rewritten (the roofers pages), which the migration's "pages you
//     edited" steps cover.
func scaffoldCRUDKind(rf routeFile) string {
	if rf.kind != "page" {
		return ""
	}
	if m := scaffoldPageRE.FindStringSubmatch(rf.src.rawHeader); m != nil {
		if k := strings.ToLower(m[1]); k == "detail" || k == "edit" {
			if len(rf.segments) >= 2 && rf.segments[1] == "[id]" {
				return k
			}
		}
	}
	if !strings.Contains(rf.src.rawHeader, scaffoldOnceBanner) || len(rf.segments) < 2 || rf.segments[1] != "[id]" {
		return ""
	}
	switch {
	case len(rf.segments) == 2:
		return "detail"
	case len(rf.segments) == 3 && rf.segments[2] == "edit":
		return "edit"
	}
	return ""
}

// scaffoldOnceBanner is the first line forge writes into every scaffold-once
// frontend file.
const scaffoldOnceBanner = "yours: scaffolded once"

// queryRoute is the static form of a dynamic route: the dynamic segments
// dropped, and a route that ENDED in one gets a `view` leaf so it does not
// collide with its parent (`/books/[id]` → `/books/view`, the scaffold's own
// convention; `/books/[id]/edit` → `/books/edit`).
func queryRoute(segs []string) []string {
	var out []string
	for _, s := range segs {
		if !dynamicSegmentRE.MatchString(s) {
			out = append(out, s)
		}
	}
	if len(segs) > 0 && dynamicSegmentRE.MatchString(segs[len(segs)-1]) {
		out = append(out, "view")
	}
	return out
}

// paramName is `id` for `[id]`, `slug` for `[...slug]`.
func paramName(seg string) string {
	return strings.Trim(seg, "[].")
}

// checkRouteHandler: a route handler is server code. The export emits a GET
// handler as a static FILE when it is declared static, refuses one that is
// not, and silently drops every other method.
func (c *checker) checkRouteHandler(rf routeFile) {
	code := rf.src.code
	methods := map[string]int{}
	for _, m := range methodFuncRE.FindAllStringSubmatchIndex(code, -1) {
		name := ""
		switch {
		case m[2] >= 0:
			name = code[m[2]:m[3]]
		case m[4] >= 0:
			name = code[m[4]:m[5]]
		}
		if _, seen := methods[name]; !seen {
			methods[name] = rf.src.lineAt(m[0])
		}
	}
	for _, m := range exportListRE.FindAllStringSubmatchIndex(code, -1) {
		for _, item := range strings.Split(code[m[2]:m[3]], ",") {
			name := strings.TrimSpace(item)
			if i := strings.LastIndex(name, " as "); i >= 0 {
				name = strings.TrimSpace(name[i+4:])
			}
			if methodFuncRE.MatchString("export function " + name) {
				if _, seen := methods[name]; !seen {
					methods[name] = rf.src.lineAt(m[0])
				}
			}
		}
	}
	if len(methods) == 0 {
		return
	}
	url := urlOf(rf.segments)
	file := c.rel(rf.src.rel)
	if line, ok := methods["GET"]; ok && !forceStaticRE.MatchString(code) && !revalidateRE.MatchString(code) {
		c.add(RuleRouteHandler, finding.SeverityError, file, line,
			fmt.Sprintf("route handler GET %s is not declared static: the export renders it ONCE, at build time, and `next build` refuses a GET that might read the request (\"export const dynamic = \\\"force-static\\\"/export const revalidate not configured on route %q\")", url, url),
			"if the response is the same for every request, add `export const dynamic = \"force-static\"` and it ships as a file; if it reads the request, it is an API endpoint — serve it from the backend (a Connect RPC) instead"+authoritativeNote)
	}
	if devGuardRE.MatchString(code) {
		// Declared dev-only: the handler answers nothing in production by
		// its own first statement (forge's dev log route does exactly
		// this). The export drops it and nothing was lost.
		return
	}
	var others []string
	first := 0
	for name, line := range methods {
		if name == "GET" || name == "HEAD" {
			continue
		}
		others = append(others, name)
		if first == 0 || line < first {
			first = line
		}
	}
	if len(others) == 0 {
		return
	}
	sort.Strings(others)
	c.add(RuleRouteHandler, finding.SeverityWarning, file, first,
		fmt.Sprintf("route handler %s %s needs a server: the export builds, drops it, and a static host answers it with 404 — it works only under `next dev`", strings.Join(others, "/"), url),
		"serve the endpoint from the backend (a Connect RPC the frontend calls through its generated hook); if it exists only for development, say so as the handler's first statement — `if (process.env.NODE_ENV === \"production\") return new Response(null, { status: 404 });` — as forge's dev log route does")
}

// checkSegmentConfig: route segment config that asks for request-time
// rendering. A static export renders once.
func (c *checker) checkSegmentConfig(rf routeFile) {
	code := rf.src.code
	file := c.rel(rf.src.rel)
	if loc := forceDynamicRE.FindStringIndex(code); loc != nil {
		c.add(RuleDynamicConfig, finding.SeverityError, file, rf.src.lineAt(loc[0]),
			fmt.Sprintf("`export const dynamic = \"force-dynamic\"` on %s asks for rendering on every request, and a static export renders once — `next build` refuses it", urlOf(rf.segments)),
			"remove it (or use \"force-static\"), and fetch per-request data in the browser through the generated hooks"+authoritativeNote)
	}
	if m := revalidateRE.FindStringSubmatchIndex(code); m != nil {
		value := strings.TrimSpace(code[m[2]:m[3]])
		seconds, err := strconv.ParseFloat(strings.ReplaceAll(value, "_", ""), 64)
		switch {
		case value == "false" || err != nil:
			// false is "static forever"; an expression cannot be judged.
		case seconds == 0:
			c.add(RuleDynamicConfig, finding.SeverityError, file, rf.src.lineAt(m[0]),
				fmt.Sprintf("`export const revalidate = 0` on %s makes it dynamic (rendered per request), and a static export renders once — `next build` refuses it", urlOf(rf.segments)),
				"remove it, and fetch per-request data in the browser through the generated hooks"+authoritativeNote)
		default:
			c.add(RuleDynamicConfig, finding.SeverityWarning, file, rf.src.lineAt(m[0]),
				fmt.Sprintf("`export const revalidate = %s` on %s asks for incremental regeneration, which needs a server: the export renders the page once and it never revalidates — frozen at build time", value, urlOf(rf.segments)),
				"drop it and fetch data that changes in the browser (the generated hooks refetch); or redeploy to refresh the build")
		}
	}
}

// checkMiddleware: middleware (Next 16: proxy) runs on the server before a
// request is served. A static host runs nothing, so whatever it guards,
// rewrites or redirects simply does not happen in production.
func (c *checker) checkMiddleware() {
	for _, f := range c.files {
		dir, base := dirOf(f.rel), path.Base(f.rel)
		if dir != "" && dir != "src" {
			continue
		}
		name := strings.TrimSuffix(base, path.Ext(base))
		if name != "middleware" && name != "proxy" {
			continue
		}
		line := 1
		if loc := exportKeywordRE.FindStringIndex(f.code); loc != nil {
			line = f.lineAt(loc[0])
		}
		c.add(RuleMiddleware, finding.SeverityWarning, c.rel(f.rel), line,
			fmt.Sprintf("%s runs on the Next.js server before each request, and a static host has no server: it runs under `next dev` and NEVER in production, so whatever it guards, rewrites or redirects is unguarded there — the export does not fail", base),
			"move the logic where a static site can run it: an auth check belongs on the backend API (and a route guard in the client), a redirect or header in your CDN's rules")
	}
}

// checkSourceConstructs: Server Actions and request APIs, in any shipping
// file.
func (c *checker) checkSourceConstructs() {
	for _, f := range c.files {
		file := c.rel(f.rel)
		for _, loc := range useServerLineRE.FindAllStringIndex(f.code, -1) {
			c.add(RuleServerAction, finding.SeverityError, file, f.lineAt(loc[0]),
				"'use server' declares a Server Action — a function the browser calls on the Next.js server — and a static export has no server: `next build` refuses it (\"Server Actions are not supported with static export\")",
				"call the backend instead: a Connect RPC through the generated hook, which is what every forge page does"+authoritativeNote)
		}
		for _, imp := range importsOf(f, "next/headers") {
			what := "next/headers"
			if imp.names != "" {
				what = imp.names + " from next/headers"
			}
			c.add(RuleNextHeaders, finding.SeverityError, file, imp.line,
				fmt.Sprintf("%s reads the incoming REQUEST (cookies(), headers(), draftMode()), and a static export has none — every page renders once at build time, so `next build` fails on the dynamic usage", what),
				"read it where a request exists: in the browser (the auth session hook, document.cookie) or on the backend"+authoritativeNote)
		}
	}
}

// importSite is one import of a module.
type importSite struct {
	line int
	// names is the import clause (`{ cookies }`, `Image`), "" for a bare or
	// dynamic import.
	names string
}

// importsOf finds every static import, re-export, require() and dynamic
// import() of any of the given module specifiers.
func importsOf(f *sourceFile, specs ...string) []importSite {
	var out []importSite
	for _, spec := range specs {
		q := regexp.QuoteMeta(spec)
		re := regexp.MustCompile(`\bfrom\s*["']` + q + `["']|\bimport\s*["']` + q + `["']|\b(?:require|import)\s*\(\s*["']` + q + `["']\s*\)`)
		for _, loc := range re.FindAllStringIndex(f.code, -1) {
			site := importSite{line: f.lineAt(loc[0])}
			if strings.HasPrefix(f.code[loc[0]:], "from") {
				// `import { a,\n b } from "x"`: the statement starts at the
				// nearest import/export keyword before `from`, and the
				// clause is everything between the two.
				lo := loc[0] - 600
				if lo < 0 {
					lo = 0
				}
				window := f.code[lo:loc[0]]
				if k := statementKeywordRE.FindAllStringIndex(window, -1); len(k) > 0 {
					last := k[len(k)-1]
					clause := strings.TrimSpace(window[last[1]:])
					clause = strings.TrimSpace(strings.TrimPrefix(clause, "type "))
					site.names = strings.Join(strings.Fields(clause), " ")
					site.line = f.lineAt(lo + last[0])
				}
			}
			out = append(out, site)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

var (
	// statementKeywordRE is the keyword opening an import or re-export.
	statementKeywordRE = regexp.MustCompile(`\b(?:import|export)\b`)
	exportKeywordRE    = regexp.MustCompile(`\bexport\b`)
)
