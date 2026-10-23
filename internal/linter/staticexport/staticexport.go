// Package staticexport reports what a Next.js App Router frontend contains
// that a static export (`output: "export"`) cannot serve.
//
// WHY. Every static runtime forge binds a frontend to — forge.OnHosted (the
// control plane's static hosting), forge.OnBucket, forge.OnFirebase —
// publishes the files the build wrote to public_dir and runs nothing behind
// them. A Next.js app is a static export only if it avoids every feature that
// needs a server at request time, and `next dev` serves all of those features
// happily. So an app grows a `[id]` route, a server action or a rewrite in
// development, and the first anyone hears of it is the export failing in CI —
// or, for the constructs the export tolerates, a production site that quietly
// lacks what dev had.
//
// WHAT IS AUTHORITATIVE. The real check is `next build` with output: export,
// which CI's `NODE_ENV=production npm run build` and forge's own static-site
// build (`forge build`, `forge env deploy`) run for every static frontend.
// This package does not replace it. It is an offline, sub-second
// approximation that reports the same failures EARLIER, each with a file, a
// line and the fix, plus the two things the build cannot report: a frontend
// whose build is not an export at all, and constructs the export accepts and
// then silently drops.
//
// SEVERITY follows exactly that line:
//
//   - error   — `next build` refuses the export (it would fail in CI anyway),
//     or the build is not an export, so there is nothing to publish.
//   - warning — the export builds, but the construct does nothing on a static
//     host: dev and production diverge with no error anywhere.
//
// LIMITS, stated so nobody mistakes this for a compiler. It reads source
// text, comment-aware but with no TypeScript AST: a generateStaticParams
// re-exported through a barrel, an import aliased through a helper module,
// or a next.config assembled by a function it cannot see into are not
// followed. Where it cannot tell, it stays SILENT rather than guess — a rule
// that invents findings trains people to suppress it. The App Router is
// covered; the legacy pages/ router is not (forge scaffolds the App Router).
//
// Findings honour the shared suppression directives (internal/linter/suppress)
// in the caller, e.g. `// forge:lint-disable-next-line
// static-export-route-handler: dev-only endpoint`.
package staticexport

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs, one per construct.
const (
	RuleNotExported    = "static-export-not-exported"
	RuleDynamicSegment = "static-export-dynamic-segment"
	RuleRouteHandler   = "static-export-route-handler"
	RuleMiddleware     = "static-export-middleware"
	RuleServerAction   = "static-export-server-action"
	RuleNextHeaders    = "static-export-next-headers"
	RuleDynamicConfig  = "static-export-dynamic-config"
	RuleNextImage      = "static-export-next-image"
	RuleConfigRoutes   = "static-export-config-routes"
	RuleTrailingSlash  = "static-export-trailing-slash"
)

// RuleIDs lists every rule this package emits.
func RuleIDs() []string {
	return []string{
		RuleNotExported, RuleDynamicSegment, RuleRouteHandler, RuleMiddleware,
		RuleServerAction, RuleNextHeaders, RuleDynamicConfig, RuleNextImage,
		RuleConfigRoutes, RuleTrailingSlash,
	}
}

// Binding is one env binding the frontend to a static runtime.
type Binding struct {
	Env string
	// Runtime is the rendered runtime type: "hosted", "bucket" or "firebase".
	Runtime string
}

// Frontend is one Next.js frontend whose production build must be a static
// export, and why.
type Frontend struct {
	Name string
	// Dir is the frontend's directory on disk.
	Dir string
	// RelDir is Dir relative to the project root, slash-separated. Every
	// finding's File is under it, which is what `forge lint --scope` matches.
	RelDir string
	// Bindings are the envs that bind it to a static runtime. A frontend
	// whose next.config is a static export with no such binding is still
	// checked.
	Bindings []Binding
}

// Check analyses one frontend. A frontend directory that does not exist yields
// nothing — there is no code to judge, and the commands that build it say so.
func Check(fe Frontend) ([]finding.Finding, error) {
	if fi, err := os.Stat(fe.Dir); err != nil || !fi.IsDir() {
		return nil, nil //nolint:nilerr // absent frontend dir: nothing to judge
	}
	files, err := collectSources(fe.Dir)
	if err != nil {
		return nil, err
	}
	c := &checker{fe: fe, files: files}
	c.nextConfig = loadNextConfig(fe.Dir)
	c.checkExported()
	c.checkRoutes()
	c.checkMiddleware()
	c.checkSourceConstructs()
	c.checkNextImage()
	c.checkConfigRoutes()
	c.checkTrailingSlash()
	sort.SliceStable(c.out, func(i, j int) bool {
		if c.out[i].File != c.out[j].File {
			return c.out[i].File < c.out[j].File
		}
		return c.out[i].Line < c.out[j].Line
	})
	return c.out, nil
}

// checker accumulates one frontend's findings.
type checker struct {
	fe         Frontend
	files      []*sourceFile
	nextConfig *sourceFile
	out        []finding.Finding
}

func (c *checker) add(rule string, sev finding.Severity, file string, line int, msg, fix string) {
	c.out = append(c.out, finding.Finding{
		Rule:        rule,
		Severity:    sev,
		File:        file,
		Line:        line,
		Message:     msg,
		Remediation: fix,
	})
}

// rel is the project-relative path of a file under the frontend dir.
func (c *checker) rel(frontendRel string) string {
	if c.fe.RelDir == "" {
		return frontendRel
	}
	return path.Join(c.fe.RelDir, frontendRel)
}

// why is the clause naming what makes this frontend static, for messages.
func (c *checker) why() string {
	if len(c.fe.Bindings) == 0 {
		return fmt.Sprintf("frontend %q is a static export", c.fe.Name)
	}
	return fmt.Sprintf("frontend %q is bound to %s", c.fe.Name, describeBindings(c.fe.Bindings))
}

// describeBindings renders "forge.OnHosted in env prod, forge.OnBucket in env
// staging".
func describeBindings(bs []Binding) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		parts = append(parts, fmt.Sprintf("%s in env %s", RuntimeLabel(b.Runtime), b.Env))
	}
	return strings.Join(parts, ", ")
}

// RuntimeLabel is the KCL spelling of a rendered static runtime type.
func RuntimeLabel(runtime string) string {
	switch runtime {
	case "hosted":
		return "forge.OnHosted"
	case "bucket":
		return "forge.OnBucket"
	case "firebase":
		return "forge.OnFirebase"
	}
	return runtime
}

// IsStaticRuntime reports whether a rendered frontend runtime type publishes
// files and runs nothing — the runtimes that require a static export.
func IsStaticRuntime(runtime string) bool {
	switch runtime {
	case "hosted", "bucket", "firebase":
		return true
	}
	return false
}

// authoritativeNote is appended to every error's remedy: the lint is early
// warning, the build is the verdict.
const authoritativeNote = " (`next build` with output: export — run by CI's `NODE_ENV=production npm run build` and by `forge build` / `forge env deploy` — is the authoritative check; this lint reports the same failure earlier.)"

// dirOf is the directory a frontend-relative file lives in, for messages.
func dirOf(rel string) string {
	d := filepath.ToSlash(filepath.Dir(rel))
	if d == "." {
		return ""
	}
	return d
}
