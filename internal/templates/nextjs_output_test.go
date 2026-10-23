package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNextJSConfig_StandaloneShape guards the standalone branch, which
// renders for `output: standalone` AND for an EMPTY Output.
//
// Empty is not the scaffold default — new frontends are scaffolded static,
// and the scaffolder resolves "" before it renders (see
// generator.TestGenerateFrontendFiles_DefaultsToStatic). Empty is what a
// forge.yaml entry written before that default looks like, and every such
// frontend's next.config.ts is the standalone render. The upgrade advisory
// re-renders this template for those entries; if empty rendered the
// static branch, every pre-existing project would see a permanent diff, and
// `upgrade --force` would quietly convert its build.
//
// This test fails if anyone:
//   - drops `output: "standalone"` / outputFileTracingRoot from the
//     standalone branch, or
//   - points the empty-Output render at the static-export shape.
func TestNextJSConfig_StandaloneShape(t *testing.T) {
	for _, output := range []string{"standalone", ""} {
		content, err := FrontendTemplates().Render(
			filepath.Join("nextjs", "next.config.ts.tmpl"),
			FrontendTemplateData{
				FrontendName: "dashboard",
				ProjectName:  "testproject",
				Output:       output,
			},
		)
		if err != nil {
			t.Fatalf("render nextjs/next.config.ts.tmpl (output=%q): %v", output, err)
		}
		s := string(content)

		if !strings.Contains(s, `output: "standalone"`) {
			t.Errorf("next.config.ts (output=%q) must contain `output: \"standalone\"` so the build emits .next-prod/standalone/server.js for the Dockerfile; got:\n%s", output, s)
		}
		if !strings.Contains(s, `outputFileTracingRoot:`) {
			t.Errorf("next.config.ts (output=%q) must set outputFileTracingRoot explicitly — Next's own root detection keys off a lockfile, and forge's dev-bridge workspace root is gitignored, so the output path would depend on which forge built the project; got:\n%s", output, s)
		}
		// The tracing root must NOT be this frontend's own directory.
		// Traced files are written to <distDir>/standalone/<path relative
		// to the root>, and the hoisted workspace install of `next` lives
		// ABOVE the frontend — so a frontend-pinned root makes that path
		// `../../node_modules/next/...` and the write escapes back into
		// frontends/<name>/node_modules/next as a pruned, package.json-less
		// copy. Node resolves that stub first on the NEXT build and dies
		// inside Next's own package ("Can't resolve '../shared/lib/utils'"),
		// so consecutive builds alternated pass/fail forever.
		if strings.Contains(s, `outputFileTracingRoot: path.join(__dirname)`) {
			t.Errorf("next.config.ts (output=%q) pins outputFileTracingRoot to the frontend directory — "+
				"file tracing then writes a partial `next` into frontends/<name>/node_modules and poisons the "+
				"following build; the root must be an ancestor of every traced file; got:\n%s", output, s)
		}
		// Standalone must NOT also emit the static-export conditional —
		// the two shapes are contradictory, and a legacy (empty-output)
		// frontend's next.config.ts is the standalone render.
		if strings.Contains(s, `{ output: "export" }`) {
			t.Errorf("next.config.ts (output=%q) must NOT contain the static-export conditional; got:\n%s", output, s)
		}
		// The distDir fence keeps a production build from clobbering the
		// dev server's .next directory (journey fr-cb84c64912).
		fence := `distDir: process.env.NODE_ENV === "production" ? ".next-prod" : ".next",`
		if !strings.Contains(s, fence) {
			t.Errorf("next.config.ts (output=%q) must carry the distDir fence %q so `npm run build` can't clobber a live dev server's .next; got:\n%s", output, fence, s)
		}
	}
}

// TestNextJSConfig_Static guards the static-export shape — what every new
// Next.js frontend is scaffolded with (`output: static` in forge.yaml,
// written by `forge project new --frontend` and `forge scaffold frontend`).
// The rendered next.config.ts must emit the NODE_ENV-gated static-export
// spread — production builds emit `out/` for the hosted runtime's static
// hosting, a bucket or a CDN while `next dev` stays unchanged — and turn
// next/image's server-side optimizer off, which an export does not have.
func TestNextJSConfig_Static(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
			Output:       "static",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl: %v", err)
	}
	s := string(content)

	// The load-bearing shape — a NODE_ENV-gated spread that pins
	// `output: "export"` in production only. Dev stays as `next dev`.
	want := `...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),`
	if !strings.Contains(s, want) {
		t.Errorf("next.config.ts (output=static) must contain %q so production builds emit a static export and dev stays unchanged; got:\n%s", want, s)
	}

	// next/image's default loader is a server route; `next build` refuses
	// an <Image> in an export without this.
	if !strings.Contains(s, "images: { unoptimized: true },") {
		t.Errorf("next.config.ts (output=static) must set images.unoptimized — an export has no image optimizer; got:\n%s", s)
	}

	// The constraint the export puts on hand-written routes — no dynamic
	// segments, the id in the query string — must be documented where a
	// user adding a route will read it.
	for _, want := range []string{"generateStaticParams", "/<slug>/view?id=", "src/lib/entity-routes.ts"} {
		if !strings.Contains(s, want) {
			t.Errorf("next.config.ts (output=static) must document the static-route shape (%q) at the point of use; got:\n%s", want, s)
		}
	}

	// Static mode must NOT carry the distDir fence: in export mode
	// Next.js treats a custom distDir as the export destination — the
	// site would land in .next-prod instead of the documented out/ —
	// while build intermediates go to .next regardless. Verified
	// empirically against Next 15 (the basepath corpus fixture asserts
	// out/ exists after a static build).
	if strings.Contains(s, "distDir:") {
		t.Errorf("next.config.ts (output=static) must not set distDir — export mode would emit the site there instead of out/; got:\n%s", s)
	}

	// Static mode must not ALSO emit standalone output — contradictory.
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, `output: "standalone"`) {
			t.Errorf("next.config.ts (output=static) emitted active `output: \"standalone\"` line outside comments; got:\n%s\nfull file:\n%s", trimmed, s)
		}
	}
}

// TestNextJSConfig_StaticOptIn_BasePathGuard verifies that the static
// branch keeps its base-path build guard: when forge.yaml declares a
// base_path but NEXT_PUBLIC_BASE_PATH empties it at build time, the
// production build must fail loudly instead of baking root-mounted
// URLs into a static export.
func TestNextJSConfig_StaticOptIn_BasePathGuard(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "admin",
			ProjectName:  "testproject",
			Output:       "static",
			BasePath:     "/admin",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `refusing to bake a `) || !strings.Contains(s, `throw new Error(`) {
		t.Errorf("next.config.ts (output=static, base_path=/admin) must keep the fail-loud base-path guard; got:\n%s", s)
	}
}

// TestNextJSConfig_StandaloneExplicit verifies the explicit standalone
// opt-in renders identically in shape to the default: `output:
// "standalone"` plus a workspace-rooted outputFileTracingRoot, so the
// scaffold-shipped Dockerfile finds `.next-prod/standalone/server.js`
// (in the container the frontend is the whole context, so the walk finds
// no workspace above it and roots at /app).
func TestNextJSConfig_StandaloneExplicit(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
			Output:       "standalone",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, `output: "standalone"`) {
		t.Errorf("next.config.ts (output=standalone) must contain `output: \"standalone\"`; got:\n%s", s)
	}
	if strings.Contains(s, `{ output: "export" }`) {
		t.Errorf("next.config.ts (output=standalone) must NOT contain the static-export conditional; got:\n%s", s)
	}
}

// TestNextJSConfig_ServerMode verifies the third opt-in: no `output:`
// field at all. Used when the project wants full Next.js for both dev
// and prod (server components, ISR, custom server, managed host).
func TestNextJSConfig_ServerMode(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
			Output:       "server",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl: %v", err)
	}
	s := string(content)

	// Server mode = no output: key at all (neither "standalone" nor
	// the export conditional). The only `output` mentions allowed are
	// inside comments.
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, `output: "standalone"`) ||
			strings.Contains(trimmed, `{ output: "export" }`) {
			t.Errorf("next.config.ts (output=server) must not emit an `output:` field outside comments; offending line:\n%s\nfull file:\n%s", trimmed, s)
		}
	}
}
