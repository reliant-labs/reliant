package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// renderNextConfig renders next.config.ts.tmpl for the given output
// branch and base path. Helper for the basePath tests below.
func renderNextConfig(t *testing.T, output, basePath string) string {
	t.Helper()
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "admin",
			ProjectName:  "testproject",
			Output:       output,
			BasePath:     basePath,
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl (output=%q basePath=%q): %v", output, basePath, err)
	}
	return string(content)
}

// TestNextJSConfig_BasePath_AllBranches guards the basePath contract in
// every output branch:
//
//   - the effective value reads the single canonical env var
//     NEXT_PUBLIC_BASE_PATH, falling back to forge.yaml's base_path
//     literal — exactly one variable name, so the cp-forge-style
//     "config reads X, .env sets Y, silently ignored" class can't
//     reappear;
//   - basePath AND assetPrefix are emitted with the same value —
//     assetPrefix is what keeps RSC/chunk URLs under the prefix so
//     React hydrates behind the proxy.
func TestNextJSConfig_BasePath_AllBranches(t *testing.T) {
	for _, output := range []string{"static", "standalone", "server"} {
		t.Run(output, func(t *testing.T) {
			s := renderNextConfig(t, output, "/admin")

			wantConst := `const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "/admin";`
			if !strings.Contains(s, wantConst) {
				t.Errorf("next.config.ts (output=%s) must source basePath from NEXT_PUBLIC_BASE_PATH with the forge.yaml literal as default; expected %q, got:\n%s", output, wantConst, s)
			}
			wantSpread := `...(basePath ? { basePath, assetPrefix: basePath } : {}),`
			if !strings.Contains(s, wantSpread) {
				t.Errorf("next.config.ts (output=%s) must emit basePath AND assetPrefix (same value); expected %q, got:\n%s", output, wantSpread, s)
			}
		})
	}
}

// TestNextJSConfig_BasePath_Unset_KeepsEnvOverridePath asserts the
// no-base_path render still wires the env-var path (empty literal
// default) so any frontend can be mounted under a prefix later by
// setting NEXT_PUBLIC_BASE_PATH at build time — without re-scaffolding.
// It must NOT emit the static-branch fail-loud guard: with no declared
// base_path, an empty effective value is the normal root mount.
func TestNextJSConfig_BasePath_Unset_KeepsEnvOverridePath(t *testing.T) {
	for _, output := range []string{"static", "standalone", "server"} {
		t.Run(output, func(t *testing.T) {
			s := renderNextConfig(t, output, "")

			wantConst := `const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";`
			if !strings.Contains(s, wantConst) {
				t.Errorf("next.config.ts (output=%s, no base_path) must still read NEXT_PUBLIC_BASE_PATH with empty default; expected %q, got:\n%s", output, wantConst, s)
			}
			if strings.Contains(s, "throw new Error") {
				t.Errorf("next.config.ts (output=%s, no base_path) must not emit the empty-basePath build guard; got:\n%s", output, s)
			}
		})
	}
}

// TestNextJSConfig_BasePath_StaticGuard pins the fail-loud contract for
// static exports: when forge.yaml declares a base_path but the env
// override empties it, the production build must throw instead of
// silently baking root-mounted URLs that 404 behind the proxy. Only the
// static branch carries the guard — standalone/server builds have a
// runtime and the guard text would be misleading there.
func TestNextJSConfig_BasePath_StaticGuard(t *testing.T) {
	s := renderNextConfig(t, "static", "/admin")
	for _, want := range []string{
		`if (process.env.NODE_ENV === "production" && basePath === "") {`,
		`throw new Error(`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("next.config.ts (output=static, base_path=/admin) must fail the production build when the effective basePath is empty; expected %q, got:\n%s", want, s)
		}
	}

	for _, output := range []string{"standalone", "server"} {
		s := renderNextConfig(t, output, "/admin")
		if strings.Contains(s, "throw new Error") {
			t.Errorf("next.config.ts (output=%s) must not carry the static-export guard; got:\n%s", output, s)
		}
	}
}

// TestNextJSBasePathGen_Render pins what the generated helper still OWNS.
//
// The BEHAVIOUR — normalisation, the idempotency guard, the absolute-URL
// passthrough — moved into @reliantlabs/forge-web-runtime, where it is tested
// directly (web-runtime/src/basepath.test.ts) instead of being re-emitted
// into every project and asserted here by substring. What is left in the
// generated file is the part that is genuinely this project's, and it is
// exactly what this test guards:
//
//   - the env read, spelled VERBATIM. Next.js inlines NEXT_PUBLIC_* by
//     substituting the literal into the sources it compiles, so this
//     expression appearing in project code is load-bearing: a library
//     reading process.env at runtime finds nothing in a browser bundle.
//   - forge.yaml's base_path baked as the fallback.
//   - the public contract the rest of the app imports (BASE_PATH,
//     joinBasePath), which must keep its names across the move.
func TestNextJSBasePathGen_Render(t *testing.T) {
	render := func(basePath string) string {
		t.Helper()
		content, err := FrontendTemplates().Render(
			filepath.Join("nextjs", "src", "lib", "basepath_gen.ts.tmpl"),
			FrontendTemplateData{
				FrontendName: "admin",
				ProjectName:  "testproject",
				BasePath:     basePath,
			},
		)
		if err != nil {
			t.Fatalf("render basepath_gen.ts.tmpl (basePath=%q): %v", basePath, err)
		}
		return string(content)
	}

	s := render("/admin")
	for _, want := range []string{
		// Tier-1 banner — the checksums guard keys off generated-file shape.
		"// Code generated by forge. DO NOT EDIT.",
		// Canonical env var with the forge.yaml literal baked as fallback.
		// Verbatim: this is the literal Next.js substitutes at build time.
		`process.env.NEXT_PUBLIC_BASE_PATH ?? "/admin"`,
		// Public contract — both symbols keep their names, so call sites
		// (and src/lib/admin-url.ts) are unaffected by where they come from.
		"export const { BASE_PATH, joinBasePath } = createBasePath(",
		// The behaviour is the library's, and is named as such.
		`import { createBasePath } from "@reliantlabs/forge-web-runtime";`,
		// Header documents the division of labour with <Link>.
		"<Link",
		"window.location",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("basepath_gen.ts (base_path=/admin) missing %q; got:\n%s", want, s)
		}
	}

	// The behaviour must NOT come back. Re-inlining the normalise/join
	// implementation here is the regression this refactor is vulnerable to:
	// the file would still compile and still pass every assertion above,
	// while quietly forking from the library copy that is actually tested.
	for _, forbidden := range []string{
		"function normalizeBasePath",
		"function joinBasePath",
		"startsWith(`${BASE_PATH}/`)",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("basepath_gen.ts re-inlines %q — that behaviour belongs to "+
				"@reliantlabs/forge-web-runtime; the generated file carries the VALUE only. Got:\n%s",
				forbidden, s)
		}
	}

	// Without a declared base_path the baked fallback is the empty string;
	// the env override remains the only way to set a prefix.
	s = render("")
	if !strings.Contains(s, `process.env.NEXT_PUBLIC_BASE_PATH ?? ""`) {
		t.Errorf("basepath_gen.ts (no base_path) must bake an empty fallback; got:\n%s", s)
	}
}
