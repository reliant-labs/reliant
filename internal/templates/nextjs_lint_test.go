package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNextJSESLintConfig_ExemptsScenariosAndConfigs is the regression test
// for frontend-eslint-warnings-on-scaffold (kalshi-trader friction). A
// fresh `forge scaffold frontend dashboard` followed by `npm run lint` was
// emitting 70+ warnings against forge-scaffolded files:
//
//   - src/mocks/scenarios/<name>.ts and vitest.config.ts hit
//     `import/no-default-export` even though both legitimately use
//     default exports (scenarios by registry contract, vitest by
//     ecosystem convention).
//   - The original override exempted only `*.config.{js,ts,mjs}`. Under
//     ESLint flat-config's minimatch behaviour `*` does not cross `/`
//     boundaries reliably across configs, so the `**/` prefix is the
//     load-bearing portable form.
//
// This test fails if anyone:
//
//   - drops the `src/mocks/scenarios/**` exemption, or
//   - downgrades `**/*.config.{js,ts,mjs}` back to `*.config.{js,ts,mjs}`
//     (which would silently re-introduce the vitest.config.ts warning).
func TestNextJSESLintConfig_ExemptsScenariosAndConfigs(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "eslint.config.mjs"),
		nil,
	)
	if err != nil {
		t.Fatalf("render nextjs/eslint.config.mjs: %v", err)
	}
	s := string(content)

	// Scenarios — defineScenario() is exported as default by template
	// contract; the registry barrel relies on it.
	if !strings.Contains(s, `"src/mocks/scenarios/**/*.{ts,tsx}"`) {
		t.Errorf("eslint.config.mjs must exempt src/mocks/scenarios/** from import/no-default-export so scaffolded scenarios don't warn; got:\n%s", s)
	}

	// vitest / postcss / etc. — match anywhere with **/ prefix.
	if !strings.Contains(s, `"**/*.config.{js,ts,mjs}"`) {
		t.Errorf("eslint.config.mjs must use **/*.config.{js,ts,mjs} (not bare *.config.*) so vitest.config.ts and friends are exempted; got:\n%s", s)
	}

	// Guard against regression to the bare-glob form. The bare form is
	// what produced the original 70-warning storm; surface it loudly if
	// someone reverts. We check for a line that ONLY contains the bare
	// glob — the `**/` form contains the same suffix, so a substring
	// search would false-positive.
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == `"*.config.{js,ts,mjs}",` {
			t.Errorf("eslint.config.mjs still contains the bare-glob form %q which silently mis-matches root configs under flat-config; use **/*.config.{js,ts,mjs} instead", strings.TrimSpace(line))
		}
	}
}

// TestNextJSOtelKeepsEnvReadsInProjectCode pins the one thing the scaffolded
// `lib/otel_gen.ts` must keep now that the SDK wiring itself lives in
// @reliantlabs/forge-web-runtime/telemetry: the `process.env.NEXT_PUBLIC_*`
// reads.
//
// Next.js inlines those literals at BUILD time, and it can only do that where
// they are written out verbatim in application code. A library reading
// `process.env` at runtime finds nothing in a browser, so hoisting these
// upstream would silently disable browser telemetry for every project — with
// no error anywhere, just no data.
func TestNextJSOtelKeepsEnvReadsInProjectCode(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "lib", "otel_gen.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/src/lib/otel_gen.ts.tmpl: %v", err)
	}
	s := string(content)

	for _, envVar := range []string{
		"process.env.NEXT_PUBLIC_OTEL_ENDPOINT",
		"process.env.NEXT_PUBLIC_APP_VERSION",
		"process.env.NEXT_PUBLIC_API_URL",
		"process.env.NEXT_PUBLIC_OTEL_REPLAY",
	} {
		if !strings.Contains(s, envVar) {
			t.Errorf("otel_gen.ts no longer reads %s in project code — Next.js can only inline that literal where it is written verbatim, so telemetry would silently no-op; got:\n%s", envVar, s)
		}
	}

	// The engine comes from the SUBPATH, never the barrel: the barrel is
	// imported by React Native frontends, which do not install
	// @hyperdx/browser and would fail to resolve it.
	if !strings.Contains(s, `"@reliantlabs/forge-web-runtime/telemetry"`) &&
		!strings.Contains(s, `'@reliantlabs/forge-web-runtime/telemetry'`) {
		t.Errorf("otel_gen.ts must import the SDK wiring from the @reliantlabs/forge-web-runtime/telemetry subpath; got:\n%s", s)
	}
	if strings.Contains(s, "forge-web-runtime/otel") {
		t.Errorf("otel_gen.ts still names the retired ./otel subpath; got:\n%s", s)
	}

	// initTelemetry is the name providers.tsx imports. providers.tsx is
	// scaffold-once, so renaming this export breaks every existing project.
	if !strings.Contains(s, "export function initTelemetry(") {
		t.Errorf("otel_gen.ts must export initTelemetry — providers.tsx (scaffold-once, never rewritten) imports it by that name; got:\n%s", s)
	}
}

// TestNextJSSearchSchemasImportGroups guards the `newlines-between: always`
// import/order rule. The scaffolded `lib/search-schemas.ts` mixes a value
// import from `react` (external group) with a type import from `zod`
// (type group); without a blank line between the two groups eslint emits
// an `import/order` warning on first `npm run lint`.
func TestNextJSSearchSchemasImportGroups(t *testing.T) {
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "lib", "search-schemas.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/src/lib/search-schemas.ts.tmpl: %v", err)
	}
	s := string(content)

	// The fix wants:
	//   import { useMemo } from "react";
	//   <blank line>
	//   import type { z } from "zod";
	idxReact := strings.Index(s, `import { useMemo } from "react";`)
	idxZod := strings.Index(s, `import type { z } from "zod";`)
	if idxReact == -1 {
		t.Fatalf("search-schemas.ts.tmpl missing react import; got:\n%s", s)
	}
	if idxZod == -1 {
		t.Fatalf("search-schemas.ts.tmpl missing zod type import; got:\n%s", s)
	}
	if idxZod <= idxReact {
		t.Errorf("zod type import must appear AFTER the react value import to satisfy import/order; got:\n%s", s)
	}
	between := s[idxReact:idxZod]
	// Require a blank line between the react value import and the zod
	// type import — the load-bearing signal for `newlines-between: always`
	// between the external and type groups.
	if !strings.Contains(between, "\n\n") {
		t.Errorf("search-schemas.ts.tmpl must have a blank line between the external and type import groups (import/order: newlines-between=always); got between react and zod:\n%q", between)
	}
}
