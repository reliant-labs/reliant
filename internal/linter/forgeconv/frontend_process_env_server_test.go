// File: internal/linter/forgeconv/frontend_process_env_server_test.go
//
// Next.js SERVER-ONLY App Router modules are exempt from
// forgeconv-frontend-process-env, because the rule's central premise is false
// for them.
//
// THE PREMISE. The rule's load-bearing argument is PROMOTABILITY: a bundler
// INLINES a process.env read at BUILD time, freezing the artifact to the
// environment it was built against, so `forge env deploy` cannot move it
// without a rebuild. True for anything that reaches the browser.
//
// WHY IT DOES NOT HOLD HERE. Next inlines process.env when it builds the
// CLIENT bundle — that is what the NEXT_PUBLIC_ prefix opts a variable into.
// A route handler runs on the Node runtime and reads process.env LIVE, at
// request time, from the container's actual environment. That IS runtime
// injection: the property the typed config module exists to buy, delivered by
// the framework's own contract.
//
// So flagging a route handler inverts the rule. The worked example is
// control-plane's api/[...path]/route.ts — the same-origin proxy, with
// `export const runtime = "nodejs"`, reading an ADMIN_API_URL that KCL sets
// per environment and that names an IN-CLUSTER host. Following the
// remediation would ship a cluster-DNS name to a browser that cannot resolve
// it, and would convert a runtime read into a build-time one, LOSING
// promotability rather than gaining it.
//
// The exemption is structural — App Router location AND a server-only
// basename — so an ordinary src/lib/route.ts stays covered.
package forgeconv

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// consoleProxyFixture is control-plane's route handler, reduced to the line
// that was being flagged. This is the exact shape reported at
// frontends/internal-console/src/app/api/[...path]/route.ts:53.
const consoleProxyFixture = `import { NextRequest } from "next/server";

const ADMIN_API_URL = process.env.ADMIN_API_URL ?? "http://localhost:8091";

export const runtime = "nodejs";

export async function GET(req: NextRequest) {
  return fetch(new URL("/x", ADMIN_API_URL));
}
`

// TestLintFrontendProcessEnv_ExemptsNextRouteHandler REPRODUCES THE FALSE
// POSITIVE. Before the fix this file produced one finding telling the author
// to move a server-side, runtime-injected, in-cluster URL into the
// browser-facing typed config module.
func TestLintFrontendProcessEnv_ExemptsNextRouteHandler(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/app/api/[...path]/route.ts", consoleProxyFixture)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("a Next.js route handler must not be flagged — it reads process.env "+
			"LIVE on the server at request time, which is already the runtime injection "+
			"this rule exists to enforce. Got %d finding(s): %+v", len(res.Findings), res.Findings)
	}
}

// TestLintFrontendProcessEnv_ExemptsServerOnlyModules covers the rest of the
// category. Each of these is server-only by framework contract.
func TestLintFrontendProcessEnv_ExemptsServerOnlyModules(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"src/app/api/health/route.ts",
		"src/app/api/[...path]/route.ts",
		"app/api/v1/widgets/route.js",
		"src/middleware.ts",
		"middleware.ts",
		"src/instrumentation.ts",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			root, feDir := newFrontendWithConfig(t)
			writeFE(t, feDir, rel, `const u = process.env.ADMIN_API_URL ?? "";
export const runtime = "nodejs";
`)
			res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
			if len(res.Findings) != 0 {
				t.Errorf("%s must be exempt, got %+v", rel, res.Findings)
			}
		})
	}
}

// TestLintFrontendProcessEnv_StillFlagsNonRouteModules is the guard that keeps
// the exemption honest. It is keyed on the App Router CONVENTION, not on a
// filename, so an ordinary module that happens to be called route.ts — or a
// client component sitting next to a route handler — stays covered.
//
// Without this, the fix for a false positive would have opened a hole in the
// rule, which is the more expensive mistake: these reads ARE inlined and DO
// freeze the artifact.
func TestLintFrontendProcessEnv_StillFlagsNonRouteModules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  string
	}{
		// Named route.ts but not in the App Router tree: an ordinary module.
		{"lib route.ts", "src/lib/route.ts"},
		// In the App Router tree, but a page — may carry "use client" and
		// its code can be sent to the browser.
		{"app page", "src/app/dashboard/page.tsx"},
		{"app layout", "src/app/layout.tsx"},
		// Ordinary app code.
		{"lib connect", "src/lib/connect.ts"},
		{"component", "src/components/nav.tsx"},
		// middleware/instrumentation are exempt only at their conventional
		// root; buried in src/lib they are ordinary modules.
		{"nested middleware", "src/lib/middleware.ts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, feDir := newFrontendWithConfig(t)
			writeFE(t, feDir, tc.rel, `const u = process.env.ADMIN_API_URL ?? "";
export default u;
`)
			res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
			if len(res.Findings) != 1 {
				t.Fatalf("%s must still be flagged (this read IS inlined and freezes the "+
					"artifact), got %d finding(s): %+v", tc.rel, len(res.Findings), res.Findings)
			}
			if !strings.Contains(res.Findings[0].Message, "ADMIN_API_URL") {
				t.Errorf("finding does not name the variable: %q", res.Findings[0].Message)
			}
		})
	}
}

// TestIsNextServerOnlyModule is the unit table behind the predicate, where the
// both-halves rule (App Router location AND server-only basename) is pinned.
func TestIsNextServerOnlyModule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rel  string
		want bool
		why  string
	}{
		{"src/app/api/[...path]/route.ts", true, "the motivating case"},
		{"app/api/health/route.ts", true, "app/ at the frontend root"},
		{"src/app/route.ts", true, "a root route handler"},
		{"app/api/v1/route.js", true, "plain JS route handler"},
		{"middleware.ts", true, "conventional root location"},
		{"src/middleware.ts", true, "conventional src location"},
		{"src/instrumentation.ts", true, "conventional src location"},

		{"src/lib/route.ts", false, "named route.ts but not in the App Router tree"},
		{"src/app/page.tsx", false, "a page may carry \"use client\""},
		{"src/app/layout.tsx", false, "a layout may carry \"use client\""},
		{"src/lib/middleware.ts", false, "middleware is exempt only at its conventional root"},
		{"src/app/api/handler.ts", false, "not a route-handler basename"},
		{"src/routes.ts", false, "not a route handler at all"},
		{"src/app/api/route.css", false, "not a scanned source extension"},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			t.Parallel()
			base := tc.rel
			if i := strings.LastIndex(tc.rel, "/"); i >= 0 {
				base = tc.rel[i+1:]
			}
			if got := isNextServerOnlyModule(tc.rel, base); got != tc.want {
				t.Errorf("isNextServerOnlyModule(%q) = %v, want %v — %s", tc.rel, got, tc.want, tc.why)
			}
		})
	}
}
