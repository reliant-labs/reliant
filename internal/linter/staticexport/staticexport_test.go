package staticexport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// exportingConfig is the production half of forge's static next.config: an
// export in production, `next dev` unchanged, next/image unoptimized.
const exportingConfig = `import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  ...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),
  images: { unoptimized: true },
};

export default nextConfig;
`

// standaloneConfig is the server build — with the static mode named in a
// comment, which must not count as an export.
const standaloneConfig = `import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // A static export would set output: "export" here instead.
  output: "standalone",
};

export default nextConfig;
`

// frontendTree writes files under a fresh frontend dir and returns a
// A static-export frontend.
func frontendTree(t *testing.T, files map[string]string) Frontend {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Frontend{Name: "web", Dir: dir, RelDir: "frontends/web"}
}

// withConfig adds the exporting next.config unless the case brings its own.
func withConfig(files map[string]string) map[string]string {
	if _, ok := files["next.config.ts"]; !ok {
		files["next.config.ts"] = exportingConfig
	}
	return files
}

func check(t *testing.T, fe Frontend) []finding.Finding {
	t.Helper()
	fs, err := Check(fe)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return fs
}

// summary renders findings as "rule severity file:line" for comparison.
func summary(fs []finding.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s %s %s:%d", f.Rule, f.Severity, f.File, f.Line))
	}
	return out
}

func assertFindings(t *testing.T, fs []finding.Finding, want ...string) {
	t.Helper()
	got := summary(fs)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n got:\n  %s\nwant:\n  %s\n\nfull:\n%s", strings.Join(got, "\n  "), strings.Join(want, "\n  "), dump(fs))
	}
}

func dump(fs []finding.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s %s:%d\n  %s\n  → %s\n", f.Rule, f.File, f.Line, f.Message, f.Remediation)
	}
	return b.String()
}

// ── not-exported ─────────────────────────────────────────────────────────

func TestNotExported(t *testing.T) {
	t.Run("static declared and exported is clean", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{}))))
	})

	t.Run("server build bound to a static runtime", func(t *testing.T) {
		fe := frontendTree(t, map[string]string{"next.config.ts": standaloneConfig})
		fe.Bindings = []Binding{{Env: "prod", Runtime: "hosted"}}
		fs := check(t, fe)
		assertFindings(t, fs, "static-export-not-exported error frontends/web/next.config.ts:5")
		for _, want := range []string{"forge.OnHosted in env prod", "`forge env render` refuses"} {
			if !strings.Contains(fs[0].Message, want) {
				t.Errorf("message does not say %q: %s", want, fs[0].Message)
			}
		}
		for _, want := range []string{"forge project upgrade --force frontends/web/next.config.ts", "forge.DockerBuild {dockerfile = \"frontends/web/Dockerfile\"}"} {
			if !strings.Contains(fs[0].Remediation, want) {
				t.Errorf("fix does not say %q: %s", want, fs[0].Remediation)
			}
		}
	})

	t.Run("next.config does not export", func(t *testing.T) {
		fs := check(t, frontendTree(t, map[string]string{"next.config.ts": standaloneConfig}))
		assertFindings(t, fs, "static-export-not-exported error frontends/web/next.config.ts:5")
		if !strings.Contains(fs[0].Message, "does not set `output: \"export\"`") {
			t.Errorf("message: %s", fs[0].Message)
		}
	})

	t.Run("no next.config is a server build", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, map[string]string{"src/app/page.tsx": "export default function P() { return null }\n"})),
			"static-export-not-exported error frontends/web:0")
	})

	t.Run("absent frontend dir judges nothing", func(t *testing.T) {
		fs, err := Check(Frontend{Name: "web", Dir: filepath.Join(t.TempDir(), "missing")})
		if err != nil || len(fs) != 0 {
			t.Errorf("got %v, %v; want nothing", fs, err)
		}
	})
}

// ── dynamic segments ─────────────────────────────────────────────────────

const clientDetailPage = `"use client";

import { useParams } from "next/navigation";

export default function Detail() {
  const { id } = useParams();
  return <p>{id}</p>;
}
`

func TestDynamicSegment(t *testing.T) {
	t.Run("client page with no generateStaticParams", func(t *testing.T) {
		fs := check(t, frontendTree(t, withConfig(map[string]string{"src/app/jobs/[id]/page.tsx": clientDetailPage})))
		assertFindings(t, fs, "static-export-dynamic-segment error frontends/web/src/app/jobs/[id]/page.tsx:5")
		if !strings.Contains(fs[0].Message, "/jobs/[id] is a dynamic route ([id])") {
			t.Errorf("message: %s", fs[0].Message)
		}
		for _, want := range []string{"server `layout.tsx`", "'use client'", "`/jobs/view?id=…`", "authoritative"} {
			if !strings.Contains(fs[0].Remediation, want) {
				t.Errorf("fix does not say %q: %s", want, fs[0].Remediation)
			}
		}
	})

	t.Run("catch-all and optional catch-all", func(t *testing.T) {
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/docs/[...slug]/page.tsx":    "export default function P() { return null }\n",
			"src/app/shop/[[...slug]]/page.tsx":  "export default function P() { return null }\n",
			"src/app/static/route-name/page.tsx": "export default function P() { return null }\n",
		})))
		assertFindings(t, fs,
			"static-export-dynamic-segment error frontends/web/src/app/docs/[...slug]/page.tsx:1",
			"static-export-dynamic-segment error frontends/web/src/app/shop/[[...slug]]/page.tsx:1")
	})

	t.Run("page-level generateStaticParams covers it", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/blog/[slug]/page.tsx": "export async function generateStaticParams() { return [{ slug: \"a\" }] }\nexport default function P() { return null }\n",
		}))))
	})

	t.Run("a layout's generateStaticParams covers the pages below it", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/blog/[slug]/layout.tsx":    "export const generateStaticParams = async () => [{ slug: \"a\" }];\nexport default function L({ children }) { return children }\n",
			"src/app/blog/[slug]/page.tsx":      clientDetailPage,
			"src/app/blog/[slug]/edit/page.tsx": clientDetailPage,
		}))))
	})

	t.Run("nested segments each need covering", func(t *testing.T) {
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"app/[org]/layout.tsx":          "export function generateStaticParams() { return [] }\n",
			"app/[org]/[repo]/page.tsx":     "export default function P() { return null }\n",
			"app/(marketing)/[id]/page.tsx": "export default function P() { return null }\n",
		})))
		assertFindings(t, fs,
			"static-export-dynamic-segment error frontends/web/app/(marketing)/[id]/page.tsx:1",
			"static-export-dynamic-segment error frontends/web/app/[org]/[repo]/page.tsx:1")
		if !strings.Contains(fs[0].Message, "/[id] is a dynamic route") {
			t.Errorf("a route group is not a URL segment: %s", fs[0].Message)
		}
		if !strings.Contains(fs[1].Message, "([repo])") {
			t.Errorf("only the uncovered segment is named: %s", fs[1].Message)
		}
	})

	t.Run("private folders are not routes", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/_drafts/[id]/page.tsx": clientDetailPage,
		}))))
	})

	t.Run("a scaffold-born page names the CRUD-routes migration", func(t *testing.T) {
		header := "\"use client\";\n\n// yours: scaffolded once, never touched again — forge will not overwrite this file\n// %s page emitted by `forge scaffold page`. See `forge skill load frontend/pages` for canonical patterns.\n"
		body := strings.TrimPrefix(clientDetailPage, "\"use client\";\n")
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/jobs/[id]/page.tsx":      fmt.Sprintf(header, "Detail") + body,
			"src/app/jobs/[id]/edit/page.tsx": fmt.Sprintf(header, "Edit") + body,
		})))
		if len(fs) != 2 {
			t.Fatalf("want the detail and edit pages:\n%s", dump(fs))
		}
		// Sorted by file: [id]/edit/page.tsx, then [id]/page.tsx.
		assertScaffoldRemedy(t, fs[0], "frontends/web/src/app/jobs/edit/page.tsx")
		assertScaffoldRemedy(t, fs[1], "frontends/web/src/app/jobs/view/page.tsx")
	})

	t.Run("a scaffold-born page since rewritten still names the migration", func(t *testing.T) {
		// The roofers shape: the banner survives, the template header does not.
		page := "\"use client\";\n\n// yours: scaffolded once, never touched again — forge will not overwrite this file\n//\n// A crew and its calendar.\n" + strings.TrimPrefix(clientDetailPage, "\"use client\";\n")
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/jobs/[id]/page.tsx":        page,
			"src/app/jobs/[id]/photos/page.tsx": page, // not a template path
		})))
		if len(fs) != 2 {
			t.Fatalf("want both pages:\n%s", dump(fs))
		}
		assertScaffoldRemedy(t, fs[0], "frontends/web/src/app/jobs/view/page.tsx")
		if strings.Contains(fs[1].Remediation, "migrations/v0.1.44") {
			t.Errorf("a page at no template path is not a scaffold page: %s", fs[1].Remediation)
		}
	})
}

func assertScaffoldRemedy(t *testing.T, f finding.Finding, newRoute string) {
	t.Helper()
	for _, s := range []string{
		newRoute,
		"forge skill load migrations/v0.1.44",
		"rm -rf 'frontends/web/src/app/jobs/[id]' frontends/web/src/app/jobs/page.tsx frontends/web/src/app/jobs/new/page.tsx && forge project rescaffold frontends/web/src/app/jobs/page.tsx frontends/web/src/app/jobs/new/page.tsx",
		"useEntityIdParam()",
	} {
		if !strings.Contains(f.Remediation, s) {
			t.Errorf("%s: remedy does not say %q:\n%s", f.File, s, f.Remediation)
		}
	}
}

// ── route handlers ───────────────────────────────────────────────────────

// devLogRoute is the shape of forge's dev log receiver: POST only, and a
// production guard as the first statement.
const devLogRoute = `export async function POST(request: Request): Promise<Response> {
  if (process.env.NODE_ENV === "production") {
    return new Response(null, { status: 404 });
  }
  return new Response(null, { status: 204 });
}
`

func TestRouteHandler(t *testing.T) {
	t.Run("GET not declared static", func(t *testing.T) {
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/api/feed/route.ts": "export async function GET() {\n  return Response.json([]);\n}\n",
		})))
		assertFindings(t, fs, "static-export-route-handler error frontends/web/src/app/api/feed/route.ts:1")
		if !strings.Contains(fs[0].Remediation, "force-static") {
			t.Errorf("fix: %s", fs[0].Remediation)
		}
	})

	t.Run("GET declared force-static ships as a file", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/robots.txt/route.ts": "export const dynamic = \"force-static\";\nexport function GET() { return new Response(\"\") }\n",
		}))))
	})

	t.Run("POST needs a server", func(t *testing.T) {
		fs := check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/api/submit/route.ts": "import x from \"y\";\n\nexport async function POST() { return new Response(null) }\nexport const PUT = POST;\n",
		})))
		assertFindings(t, fs, "static-export-route-handler warning frontends/web/src/app/api/submit/route.ts:3")
		if !strings.Contains(fs[0].Message, "POST/PUT /api/submit") {
			t.Errorf("message: %s", fs[0].Message)
		}
	})

	t.Run("export list form", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/api/x/route.ts": "async function handler() { return new Response(null) }\nexport { handler as DELETE };\n",
		}))), "static-export-route-handler warning frontends/web/src/app/api/x/route.ts:2")
	})

	t.Run("a dev-only handler is clean", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{
			"src/app/%5F_forge/log/route.ts": devLogRoute,
		}))))
	})
}

// ── middleware ───────────────────────────────────────────────────────────

func TestMiddleware(t *testing.T) {
	fs := check(t, frontendTree(t, withConfig(map[string]string{
		"src/middleware.ts":           "import { NextResponse } from \"next/server\";\n\nexport function middleware() { return NextResponse.next() }\n",
		"proxy.ts":                    "export function proxy() {}\n",
		"src/app/middleware/page.tsx": "export default function P() { return null }\n",
		"src/lib/middleware.ts":       "export const x = 1;\n",
	})))
	assertFindings(t, fs,
		"static-export-middleware warning frontends/web/proxy.ts:1",
		"static-export-middleware warning frontends/web/src/middleware.ts:3")
}

// ── server actions ───────────────────────────────────────────────────────

func TestServerAction(t *testing.T) {
	fs := check(t, frontendTree(t, withConfig(map[string]string{
		"src/app/actions.ts":      "'use server'\n\nexport async function save() {}\n",
		"src/components/form.tsx": "export function Form() {\n  async function submit() {\n    \"use server\";\n  }\n  return null;\n}\n",
		"src/lib/notes.ts":        "// \"use server\" would make this an action\nexport const label = \"use server\";\n/*\n'use server'\n*/\n",
	})))
	assertFindings(t, fs,
		"static-export-server-action error frontends/web/src/app/actions.ts:1",
		"static-export-server-action error frontends/web/src/components/form.tsx:3")
}

// ── next/headers ─────────────────────────────────────────────────────────

func TestNextHeaders(t *testing.T) {
	fs := check(t, frontendTree(t, withConfig(map[string]string{
		"src/app/page.tsx": "import Link from \"next/link\";\nimport {\n  cookies,\n  headers,\n} from \"next/headers\";\n\nexport default function P() { cookies(); return null }\n",
		"src/lib/dm.ts":    "export async function dm() {\n  const { draftMode } = await import(\"next/headers\");\n}\n",
		"src/lib/ok.ts":    "// import { cookies } from \"next/headers\";\nexport const x = \"next/headers\";\n",
	})))
	assertFindings(t, fs,
		"static-export-next-headers error frontends/web/src/app/page.tsx:2",
		"static-export-next-headers error frontends/web/src/lib/dm.ts:2")
	if !strings.HasPrefix(fs[0].Message, "{ cookies, headers, } from next/headers") {
		t.Errorf("message should name the imports: %s", fs[0].Message)
	}
}

// ── segment config ───────────────────────────────────────────────────────

func TestDynamicConfig(t *testing.T) {
	fs := check(t, frontendTree(t, withConfig(map[string]string{
		"src/app/live/page.tsx":     "export const dynamic = \"force-dynamic\";\nexport default function P() { return null }\n",
		"src/app/zero/page.tsx":     "export const revalidate = 0;\nexport default function P() { return null }\n",
		"src/app/isr/page.tsx":      "export const revalidate = 3_600;\nexport default function P() { return null }\n",
		"src/app/forever/page.tsx":  "export const revalidate = false;\nexport default function P() { return null }\n",
		"src/app/static/layout.tsx": "export const dynamic = \"force-static\";\nexport default function L({ children }) { return children }\n",
		"src/lib/not-a-route.ts":    "export const dynamic = \"force-dynamic\";\n",
	})))
	assertFindings(t, fs,
		"static-export-dynamic-config warning frontends/web/src/app/isr/page.tsx:1",
		"static-export-dynamic-config error frontends/web/src/app/live/page.tsx:1",
		"static-export-dynamic-config error frontends/web/src/app/zero/page.tsx:1")
}

// ── next/image ───────────────────────────────────────────────────────────

func TestNextImage(t *testing.T) {
	const avatar = "import Image from \"next/image\";\nexport function A() { return <Image src=\"/a.png\" alt=\"\" width={1} height={1} /> }\n"
	t.Run("default loader", func(t *testing.T) {
		fs := check(t, frontendTree(t, map[string]string{
			"next.config.ts":            "const nextConfig = {\n  output: \"export\",\n};\nexport default nextConfig;\n",
			"src/components/avatar.tsx": avatar,
		}))
		assertFindings(t, fs, "static-export-next-image error frontends/web/next.config.ts:1")
		if !strings.Contains(fs[0].Message, "frontends/web/src/components/avatar.tsx:1") {
			t.Errorf("message should name the import site: %s", fs[0].Message)
		}
	})
	t.Run("unoptimized", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, withConfig(map[string]string{"src/components/avatar.tsx": avatar}))))
	})
	t.Run("custom loader", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, map[string]string{
			"next.config.ts":            "export default { output: \"export\", images: { loader: \"custom\", loaderFile: \"./img.ts\" } };\n",
			"src/components/avatar.tsx": avatar,
		})))
	})
}

// ── rewrites / redirects / headers ───────────────────────────────────────

func TestConfigRoutes(t *testing.T) {
	t.Run("ungated", func(t *testing.T) {
		fs := check(t, frontendTree(t, map[string]string{"next.config.ts": `const nextConfig = {
  output: "export",
  async rewrites() {
    return [{ source: "/api/:p*", destination: "http://localhost:8080/:p*" }];
  },
  redirects: async () => [],
};
export default nextConfig;
`}))
		assertFindings(t, fs,
			"static-export-config-routes warning frontends/web/next.config.ts:3",
			"static-export-config-routes warning frontends/web/next.config.ts:6")
	})
	t.Run("gated to development", func(t *testing.T) {
		assertFindings(t, check(t, frontendTree(t, map[string]string{"next.config.ts": `// Development: full Next.js dev server (HMR, rewrites).
const nextConfig = {
  ...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),
  ...(process.env.NODE_ENV === "development"
    ? {
        async rewrites() {
          return [];
        },
      }
    : {}),
  async headers() {
    if (process.env.NODE_ENV === "production") return [];
    return [{ source: "/(.*)", headers: [] }];
  },
};
export default nextConfig;
`})))
	})
}

// ── trailing slash on a bucket ───────────────────────────────────────────

func TestTrailingSlash(t *testing.T) {
	pages := map[string]string{
		"src/app/page.tsx":            "export default function P() { return null }\n",
		"src/app/books/view/page.tsx": "export default function P() { return null }\n",
	}
	bucket := []Binding{{Env: "prod", Runtime: "bucket"}, {Env: "staging", Runtime: "hosted"}}

	t.Run("bucket without trailingSlash 404s every non-root route", func(t *testing.T) {
		fe := frontendTree(t, withConfig(copyFiles(pages)))
		fe.Bindings = bucket
		fs := check(t, fe)
		assertFindings(t, fs, "static-export-trailing-slash warning frontends/web/next.config.ts:3")
		for _, want := range []string{"forge.OnBucket in env prod", "books/view.html for /books/view"} {
			if !strings.Contains(fs[0].Message, want) {
				t.Errorf("message does not say %q: %s", want, fs[0].Message)
			}
		}
		if strings.Contains(fs[0].Message, "OnHosted in env staging") {
			t.Errorf("the hosted binding resolves .html itself and must not be named: %s", fs[0].Message)
		}
		if !strings.Contains(fs[0].Remediation, "`trailingSlash: true`") {
			t.Errorf("fix: %s", fs[0].Remediation)
		}
	})

	t.Run("trailingSlash true is clean", func(t *testing.T) {
		files := copyFiles(pages)
		files["next.config.ts"] = strings.Replace(exportingConfig, "images:", "trailingSlash: true,\n  images:", 1)
		fe := frontendTree(t, files)
		fe.Bindings = bucket
		assertFindings(t, check(t, fe))
	})

	t.Run("hosted only is clean", func(t *testing.T) {
		fe := frontendTree(t, withConfig(copyFiles(pages)))
		fe.Bindings = []Binding{{Env: "prod", Runtime: "hosted"}}
		assertFindings(t, check(t, fe))
	})

	t.Run("a single-page site has nothing to 404", func(t *testing.T) {
		fe := frontendTree(t, withConfig(map[string]string{"src/app/page.tsx": pages["src/app/page.tsx"]}))
		fe.Bindings = bucket
		assertFindings(t, check(t, fe))
	})
}

func copyFiles(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ── comment stripping ────────────────────────────────────────────────────

func TestStripCommentsKeepsLinesAndStrings(t *testing.T) {
	in := "const u = \"http://x\"; // trailing\n/* block\nspans */ const t = `a // b`;\n'it''s'\n"
	got := stripComments(in)
	if len(got) != len(in) || strings.Count(got, "\n") != strings.Count(in, "\n") {
		t.Fatalf("shape changed:\n%q\n%q", in, got)
	}
	for _, keep := range []string{`"http://x"`, "`a // b`", "const t"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q: %q", keep, got)
		}
	}
	for _, drop := range []string{"trailing", "block", "spans"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept comment text %q: %q", drop, got)
		}
	}
}
