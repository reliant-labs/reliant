package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/components"
	nativecomponents "github.com/reliant-labs/forge/pkg/components/native"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Frontend-workspaces emitters.
//
// When `frontend.workspaces: true` in forge.yaml, forge reshapes the
// frontend layout into a pnpm-workspace:
//
//   <root>/
//     pnpm-workspace.yaml
//     packages/
//       api/        — buf-generated Connect TS clients + proto types
//       hooks/      — React Query wrappers + per-service generated hooks
//     frontends/
//       <name>/     — Next.js / Expo / Vite SPA app, imports @<scope>/api etc.
//
// This file owns the workspace-layout files that don't fit cleanly under
// frontend_gen.go (which emits one frontend's files) — pnpm-workspace.yaml
// at the project root, packages/api/, and packages/hooks/. The per-frontend
// emitters in frontend_gen.go consult the workspaces flag to know whether
// to skip emitting per-frontend gen/hooks files (the workspace versions
// supersede them).

// FrontendWorkspaceLayout describes the workspace package names + paths
// derived from a project. It is the single source of truth that hooks
// generation, buf TS generation, and frontend templates consult.
type FrontendWorkspaceLayout struct {
	// Scope is the npm scope without the leading `@`. Derived from
	// the project name; sanitized to a valid npm scope segment.
	Scope string
	// APIPackage is the fully-qualified npm package name for the API
	// workspace (e.g. "@myapp/api").
	APIPackage string
	// HooksPackage is the fully-qualified npm package name for the
	// hooks workspace (e.g. "@myapp/hooks").
	HooksPackage string
	// UIWebPackage is the fully-qualified npm package name for the
	// shared web-UI component workspace (e.g. "@myapp/ui-web"). It
	// holds the React/Tailwind component library that all browser-
	// targeted frontends import from instead of duplicating per-
	// frontend copies under src/components/.
	UIWebPackage string
	// UINativePackage is the fully-qualified npm package name for the
	// React Native primitives workspace (e.g. "@myapp/ui-native").
	// Only emitted when the project has at least one RN frontend AND
	// workspaces are enabled — empty otherwise.
	UINativePackage string
}

// NewFrontendWorkspaceLayout builds the canonical layout from a raw
// project name. Falls back to "@app/api" / "@app/hooks" / "@app/ui-web"
// when the project name doesn't produce a valid npm scope segment.
func NewFrontendWorkspaceLayout(projectName string) FrontendWorkspaceLayout {
	scope := sanitizeNpmScopeSegment(projectName)
	if scope == "" {
		scope = "app"
	}
	return FrontendWorkspaceLayout{
		Scope:           scope,
		APIPackage:      "@" + scope + "/api",
		HooksPackage:    "@" + scope + "/hooks",
		UIWebPackage:    "@" + scope + "/ui-web",
		UINativePackage: "@" + scope + "/ui-native",
	}
}

// sanitizeNpmScopeSegment produces a string valid as an npm scope
// component (no @, lowercase, hyphens/underscores allowed, no leading
// digit/dot). npm requires:
//   - all lowercase
//   - URL-safe (no spaces, no uppercase, no @)
//   - cannot start with . or _
//
// Returns "" when no valid characters remain.
func sanitizeNpmScopeSegment(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			// Drop other chars (spaces, dots, @, /).
		}
	}
	s := b.String()
	s = strings.TrimLeft(s, "._-0123456789")
	return s
}

// WriteFrontendWorkspaceFiles emits the workspace-layout scaffolding:
//
//   - pnpm-workspace.yaml at the project root
//   - packages/api/package.json + tsconfig.json + .gitignore
//   - packages/hooks/package.json + tsconfig.json + use-api-{query,mutation}.ts
//   - packages/ui-web/package.json + tsconfig.json + src/components/*.tsx
//
// Idempotent: writes files only if they do not already exist, so re-
// running `forge generate` after the user customizes (e.g.) the
// packages/api/package.json does not clobber their edits.
//
// Returns nil immediately when workspaces is disabled — callers can
// invoke unconditionally and let this function decide whether work is
// needed.
func WriteFrontendWorkspaceFiles(projectDir, projectName string, workspaces bool) error {
	if !workspaces {
		return nil
	}
	layout := NewFrontendWorkspaceLayout(projectName)

	if err := writePnpmWorkspaceYaml(projectDir); err != nil {
		return err
	}
	if err := writeAPIPackage(projectDir, layout); err != nil {
		return err
	}
	if err := writeHooksPackage(projectDir, layout); err != nil {
		return err
	}
	// packages/hooks holds the generated hooks, which import
	// @reliantlabs/forge-web-runtime for the ConnectClientError type. Reconciling
	// the specifier here is what bridges it to a dev forge's checkout, and
	// what adds the entry to a workspace project scaffolded before the hooks
	// declared the type.
	EnsureWorkspaceHooksWebRuntimeDependency(projectDir)
	if err := writeUIWebPackage(projectDir, layout); err != nil {
		return err
	}
	return nil
}

// WriteUIWebPackageFiles emits packages/ui-web/ — the shared web-UI
// component library workspace. Idempotent (write-if-missing); safe to
// call from `forge generate` cycles after the user customizes the
// scaffolded files.
//
// No-op when workspaces is false so existing single-frontend projects
// regenerate byte-identically.
//
// Split from WriteFrontendWorkspaceFiles so callers that want to refresh
// just the ui-web scaffold (e.g. ensure-step in `forge generate`) don't
// have to touch the api/hooks packages.
func WriteUIWebPackageFiles(projectDir, projectName string, workspaces bool) error {
	if !workspaces {
		return nil
	}
	layout := NewFrontendWorkspaceLayout(projectName)
	return writeUIWebPackage(projectDir, layout)
}

// writePnpmWorkspaceYaml writes the root pnpm-workspace.yaml so pnpm
// recognizes frontends/* and packages/* as workspace members.
//
// We write `packages/*` first so a future `pnpm add` resolves intra-
// workspace deps before reaching for the frontends/* members (pnpm
// honors order for ambiguous installs).
func writePnpmWorkspaceYaml(projectDir string) error {
	path := filepath.Join(projectDir, "pnpm-workspace.yaml")
	body := `# pnpm workspace manifest — generated by ` + "`forge generate`" + `.
#
# Forge emits two workspace tiers:
#   - packages/* — shared TypeScript packages (api, hooks).
#     Adding a new shared package: ` + "`mkdir packages/<name> && pnpm init`" + `.
#   - frontends/* — per-app workspaces (Next.js, Expo, Vite SPA).
#     Created by ` + "`forge scaffold frontend <name>`" + `.
#
# Workspace members reference each other via "workspace:*" in their
# package.json, e.g. "@<scope>/api": "workspace:*".
packages:
  - "packages/*"
  - "frontends/*"
`
	return writeIfMissing(path, body)
}

// writeAPIPackage emits packages/api/{package.json, tsconfig.json,
// .gitignore, README.md}. The api package surfaces the buf-generated
// Connect TS clients to the rest of the workspace — its `main` and
// `types` point at src/gen/index.ts (re-export barrel) so consumers
// say `import { UserService } from "@<scope>/api"`.
func writeAPIPackage(projectDir string, layout FrontendWorkspaceLayout) error {
	apiDir := filepath.Join(projectDir, "packages", "api")
	if err := os.MkdirAll(filepath.Join(apiDir, "src"), 0o755); err != nil {
		return fmt.Errorf("create packages/api: %w", err)
	}

	pkg := fmt.Sprintf(`{
  "name": "%s",
  "version": "0.0.0",
  "private": true,
  "main": "./src/index.ts",
  "types": "./src/index.ts",
  "exports": {
    ".": "./src/index.ts",
    "./*": "./src/gen/*.ts"
  },
  "scripts": {
    "typecheck": "tsc --noEmit"
  },
  "dependencies": {
    "@bufbuild/protobuf": "^2.5.0",
    "@connectrpc/connect": "^2.0.0"
  },
  "devDependencies": {
    "@bufbuild/protoc-gen-es": "^2.5.0",
    "typescript": "^5.8.0"
  }
}
`, layout.APIPackage)
	if err := writeIfMissing(filepath.Join(apiDir, "package.json"), pkg); err != nil {
		return err
	}

	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022", "DOM"],
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true,
    "verbatimModuleSyntax": false,
    "allowSyntheticDefaultImports": true
  },
  "include": ["src/**/*.ts"]
}
`
	if err := writeIfMissing(filepath.Join(apiDir, "tsconfig.json"), tsconfig); err != nil {
		return err
	}

	// src/index.ts is a stub barrel that gets supplemented as buf
	// generates _pb files into src/gen/. We seed it so the package
	// compiles on a fresh scaffold (before any proto exists).
	indexTS := `// Re-export buf-generated Connect TS clients + proto types.
//
// The contents of src/gen/ are produced by ` + "`buf generate`" + ` (driven by
// the project's root ` + "`buf.gen.yaml`" + `). Add a re-export line below for
// each generated file you want to surface from the package, or import
// directly from "` + layout.APIPackage + `/<path>" using the subpath export.
//
// e.g. once you have proto/services/users/v1/users.proto:
//   export * from "./gen/services/users/v1/users_pb";

export {};
`
	if err := writeIfMissing(filepath.Join(apiDir, "src", "index.ts"), indexTS); err != nil {
		return err
	}

	// No .gitignore for src/gen/: buf's stubs are committed, like every
	// other generated file forge emits, so a checkout builds as-cloned and
	// `forge ci verify-generated` can see drift in them. This package used
	// to ship `src/gen/` ignored "so repositories stay clean", which left
	// every fresh clone's frontends unable to typecheck until someone ran
	// codegen — the failure the scaffold .gitignore now documents.

	readme := fmt.Sprintf("# %s\n\n"+
		"Shared Connect TS clients + proto types for the project's frontends.\n\n"+
		"Generated by `forge generate` (via `buf generate`). Consumed by\n"+
		"`frontends/*` workspaces via `\"%s\": \"workspace:*\"`.\n\n"+
		"## Layout\n\n"+
		"- `src/gen/` — buf-emitted `*_pb.ts` files (one per proto file).\n"+
		"- `src/index.ts` — re-export barrel; add `export * from \"./gen/...\"`\n"+
		"  for each generated file you want to expose by the top-level package name.\n\n"+
		"Subpath imports also work: `import { UserService } from \"%s/services/users/v1/users_pb\"`.\n",
		layout.APIPackage, layout.APIPackage, layout.APIPackage)
	if err := writeIfMissing(filepath.Join(apiDir, "README.md"), readme); err != nil {
		return err
	}
	return nil
}

// writeHooksPackage emits packages/hooks/{package.json, tsconfig.json,
// src/use-api-query.ts, src/use-api-mutation.ts, src/index.ts}.
//
// The package is DOM-free: it depends on @tanstack/react-query, the
// project's @<scope>/api workspace and @reliantlabs/forge-web-runtime (for the
// ConnectClientError type the generated hooks declare), and uses no
// document/window APIs. That keeps it consumable from both Next.js
// (DOM-aware) and React Native (DOM-free Hermes/Node runtime).
//
// The runtime specifier written here is the published range; a dev forge
// build rewrites it to a `file:` bridge in
// EnsureWorkspaceHooksWebRuntimeDependency, the same reconcile every
// frontend manifest gets.
func writeHooksPackage(projectDir string, layout FrontendWorkspaceLayout) error { //nolint:funlen // length is embedded TypeScript source literals, not control flow — the branching is one writeIfMissing per emitted file.
	hooksDir := filepath.Join(projectDir, "packages", "hooks")
	if err := os.MkdirAll(filepath.Join(hooksDir, "src", "generated"), 0o755); err != nil {
		return fmt.Errorf("create packages/hooks: %w", err)
	}

	pkg := fmt.Sprintf(`{
  "name": "%s",
  "version": "0.0.0",
  "private": true,
  "main": "./src/index.ts",
  "types": "./src/index.ts",
  "exports": {
    ".": "./src/index.ts",
    "./generated": "./src/generated/index.ts",
    "./*": "./src/*.ts"
  },
  "scripts": {
    "typecheck": "tsc --noEmit"
  },
  "dependencies": {
    "%s": "workspace:*",
    "@bufbuild/protobuf": "^2.5.0",
    "@connectrpc/connect": "^2.0.0",
    "%s": "%s",
    "@tanstack/react-query": "^5.59.0"
  },
  "peerDependencies": {
    "react": "*"
  },
  "devDependencies": {
    "typescript": "^5.8.0"
  }
}
`, layout.HooksPackage, layout.APIPackage, WebRuntimePackage, webRuntimePublishedRange)
	if err := writeIfMissing(filepath.Join(hooksDir, "package.json"), pkg); err != nil {
		return err
	}

	// Important: this tsconfig deliberately omits "DOM" from `lib`. The
	// hooks package is DOM-free — it must be consumable from React
	// Native (no document/window). If a contributor adds a DOM-only API,
	// tsc will fail here, which is the intended guardrail.
	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022"],
    "jsx": "preserve",
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true
  },
  "include": ["src/**/*.ts", "src/**/*.tsx"]
}
`
	if err := writeIfMissing(filepath.Join(hooksDir, "tsconfig.json"), tsconfig); err != nil {
		return err
	}

	// transport.ts holds the module-level Connect Transport that
	// frontends bootstrap once at startup. The hooks package itself is
	// DOM-free; the actual Transport (fetch-backed on web, custom-fetch
	// on React Native) is constructed in the frontend's own connect.ts
	// and handed to setApiTransport(transport) before any hook runs.
	transportTS := `import { createClient, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";

/**
 * Module-level transport. Each frontend constructs its own Transport
 * (browser fetch in Next.js, custom fetch in React Native) and registers
 * it once at startup via setApiTransport(). Hooks generated under
 * src/generated/ resolve their connect clients lazily against whichever
 * transport is registered when the hook fires.
 *
 * Keeping the transport in a module-level singleton (rather than a
 * React Context) lets the hooks package stay framework-agnostic — no
 * <Provider> wiring required from React Native, Next.js, Expo, etc.
 */
let _transport: Transport | null = null;

export function setApiTransport(t: Transport) {
  _transport = t;
}

export function getApiTransport(): Transport {
  if (!_transport) {
    throw new Error(
      "ApiTransport not initialized — call setApiTransport(transport) " +
        "from your frontend's connect.ts before rendering hooks.",
    );
  }
  return _transport;
}

/**
 * connectClient creates a typed Connect client against the currently-
 * registered Transport. Mirrors the per-frontend connectClient() helper
 * so generated hooks can call ` + "`connectClient(UserService)`" + ` exactly
 * like they do in the non-workspace layout.
 */
export function connectClient<S extends DescService>(service: S) {
  return createClient(service, getApiTransport());
}
`
	if err := writeIfMissing(filepath.Join(hooksDir, "src", "transport.ts"), transportTS); err != nil {
		return err
	}

	useAPIQuery := `import { useQuery, type UseQueryOptions } from "@tanstack/react-query";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";

/**
 * useApiQuery wraps a Connect client promise-returning call in a
 * @tanstack/react-query useQuery. The hooks package is DOM-free so this
 * helper works in both Next.js and React Native.
 *
 * Generated per-service hooks live alongside this file under
 * src/generated/ — see ./generated/index.ts. Use this base wrapper for
 * one-off or composite operations that don't map to a generated hook.
 *
 * TError stays a parameter here — unlike the generated hooks, this wrapper
 * takes an ARBITRARY promise, so it cannot know the transport that produced
 * it. It DEFAULTS to ConnectClientError because the documented use is a
 * Connect client call, and that is the shape the transport's error-normalize
 * interceptor throws. React Query's ` + "`Error`" + ` hid reason/code/status/
 * retryable and left message prose as the only thing a call site could match.
 */
export function useApiQuery<TData, TError = ConnectClientError>(
  key: readonly unknown[],
  fetcher: () => Promise<TData>,
  options?: Omit<
    UseQueryOptions<TData, TError, TData, readonly unknown[]>,
    "queryKey" | "queryFn"
  >,
) {
  return useQuery<TData, TError, TData, readonly unknown[]>({
    queryKey: key,
    queryFn: fetcher,
    ...options,
  });
}
`
	if err := writeIfMissing(filepath.Join(hooksDir, "src", "use-api-query.ts"), useAPIQuery); err != nil {
		return err
	}

	useAPIMutation := `import { useMutation, type UseMutationOptions } from "@tanstack/react-query";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";

/**
 * useApiMutation wraps a Connect client promise-returning call in a
 * @tanstack/react-query useMutation. The hooks package is DOM-free so
 * this helper works in both Next.js and React Native.
 *
 * TError stays a parameter here — unlike the generated hooks, this wrapper
 * takes an ARBITRARY promise, so it cannot know the transport that produced
 * it. It DEFAULTS to ConnectClientError because the documented use is a
 * Connect client call, and that is the shape the transport's error-normalize
 * interceptor throws. React Query's ` + "`Error`" + ` hid reason/code/status/
 * retryable and left message prose as the only thing a call site could match.
 */
export function useApiMutation<TData, TVariables, TError = ConnectClientError>(
  mutationFn: (variables: TVariables) => Promise<TData>,
  options?: Omit<
    UseMutationOptions<TData, TError, TVariables>,
    "mutationFn"
  >,
) {
  return useMutation<TData, TError, TVariables>({
    mutationFn,
    ...options,
  });
}
`
	if err := writeIfMissing(filepath.Join(hooksDir, "src", "use-api-mutation.ts"), useAPIMutation); err != nil {
		return err
	}

	// src/index.ts is the top-level barrel for the package. Generated
	// per-service files re-exported via src/generated/index.ts (written
	// at frontend-hooks-generation time, alongside the *.ts files).
	indexTS := `// @<scope>/hooks barrel — exports the base wrappers, the
// transport bootstrap helpers, and the generated per-service hooks.
// The generated/ subpath is also exposed directly so consumers can do
// ` + "`import { useGetUser } from \"@<scope>/hooks/generated\"`" + ` when they
// want to skip the barrel.

export * from "./transport";
export * from "./use-api-query";
export * from "./use-api-mutation";
export * from "./generated";
`
	if err := writeIfMissing(filepath.Join(hooksDir, "src", "index.ts"), indexTS); err != nil {
		return err
	}

	// Seed an empty generated barrel so the package compiles on a fresh
	// scaffold before any service hooks have been emitted. The frontend-
	// hooks step rewrites this file every run.
	generatedIndex := `// Code generated by forge. DO NOT EDIT.
// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)
// Re-exports per-service hooks. Re-rendered every ` + "`forge generate`" + `.
export {};
`
	if err := writeIfMissing(filepath.Join(hooksDir, "src", "generated", "index.ts"), generatedIndex); err != nil {
		return err
	}

	readme := fmt.Sprintf("# %s\n\n"+
		"Shared React Query hooks for the project's frontends. DOM-free —\n"+
		"consumable from both Next.js and React Native (Expo).\n\n"+
		"## Layout\n\n"+
		"- `src/use-api-query.ts` / `src/use-api-mutation.ts` — base wrappers\n"+
		"  for one-off Connect calls.\n"+
		"- `src/generated/<service>-hooks.ts` — per-service hooks generated\n"+
		"  by `forge generate` from the project's proto/services/ files.\n\n"+
		"Frontends consume the package via `\"%s\": \"workspace:*\"` and\n"+
		"`import { useGetUser } from \"%s\"`.\n",
		layout.HooksPackage, layout.HooksPackage, layout.HooksPackage)
	if err := writeIfMissing(filepath.Join(hooksDir, "README.md"), readme); err != nil {
		return err
	}
	return nil
}

// writeUIWebPackage emits packages/ui-web/{package.json, tsconfig.json,
// src/components/<all core components>.tsx, src/index.ts, README.md}.
//
// The ui-web package holds the shared React/Tailwind component library
// that all browser-targeted frontends (Next.js, Vite SPA) import from.
// In the non-workspaces layout the same components are copied per-
// frontend; centralising them here eliminates the divergence problem
// when a project has 2+ web frontends.
//
// Write-if-missing semantics across the board: once a file lands on
// disk the user owns it. `forge generate` never overwrites edits to
// these scaffolded components — this matches the philosophy laid out
// in the frontend-workspaces skill.
//
// React Native is OUT of scope here. RN uses platform-specific
// primitives and does not consume web components; mobile frontends
// don't get the workspace dep.
func writeUIWebPackage(projectDir string, layout FrontendWorkspaceLayout) error {
	uiDir := filepath.Join(projectDir, "packages", "ui-web")
	// Components live under src/components/ui/<name>.tsx — mirroring the
	// per-frontend layout (frontends/<n>/src/components/ui/<name>.tsx).
	// This lets the tsconfig path mapping target `@/components/ui/*`
	// specifically, leaving `@/components/<other>/*` (e.g. the owned
	// `@/components/nav`) resolving against the per-frontend src tree.
	componentsDir := filepath.Join(uiDir, "src", "components", "ui")
	if err := os.MkdirAll(componentsDir, 0o755); err != nil {
		return fmt.Errorf("create packages/ui-web: %w", err)
	}

	// Deps mirror what the shadcn-style component library typically
	// reaches for. Today's embedded components only import "react", but
	// the package surface anticipates future components (clsx /
	// tailwind-merge for class merging, lucide-react for icons, cva for
	// variant styling). Frontend package.json templates already declare
	// lucide-react etc., so listing them here doesn't expand the install.
	pkg := fmt.Sprintf(`{
  "name": "%s",
  "version": "0.0.0",
  "private": true,
  "main": "./src/index.ts",
  "types": "./src/index.ts",
  "exports": {
    ".": "./src/index.ts",
    "./components/ui/*": "./src/components/ui/*.tsx"
  },
  "scripts": {
    "typecheck": "tsc --noEmit"
  },
  "peerDependencies": {
    "react": "*",
    "react-dom": "*"
  },
  "dependencies": {
    "class-variance-authority": "^0.7.0",
    "clsx": "^2.1.0",
    "lucide-react": "^1.17.0",
    "tailwind-merge": "^2.5.0"
  },
  "devDependencies": {
    "@types/react": "^19.1.0",
    "@types/react-dom": "^19.1.0",
    "typescript": "^5.8.0"
  }
}
`, layout.UIWebPackage)
	if err := writeIfMissing(filepath.Join(uiDir, "package.json"), pkg); err != nil {
		return err
	}

	// tsconfig — DOM is on (these ARE the DOM-targeted components) and
	// JSX is preserved so consuming Next.js / Vite SPA owns the JSX
	// transform pass. Keeping declaration: true means `pnpm -r
	// typecheck` covers the package even if it's never built.
	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "jsx": "preserve",
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true,
    "allowSyntheticDefaultImports": true
  },
  "include": ["src/**/*.ts", "src/**/*.tsx"]
}
`
	if err := writeIfMissing(filepath.Join(uiDir, "tsconfig.json"), tsconfig); err != nil {
		return err
	}

	// Copy the same core components that installCoreComponents() lays
	// down per-frontend in the non-workspaces case. Same names, same
	// content — the workspaces flag is purely about WHERE the files
	// land, not WHICH files.
	lib := components.NewLibrary()
	for _, name := range coreComponents {
		content, err := lib.Get(name)
		if err != nil {
			return fmt.Errorf("get ui-web component %s: %w", name, err)
		}
		dest := filepath.Join(componentsDir, name+".tsx")
		if err := writeIfMissing(dest, content); err != nil {
			return fmt.Errorf("write ui-web component %s: %w", name, err)
		}
	}

	// src/index.ts re-export barrel. Built deterministically from the
	// coreComponents list (sorted) so the barrel is stable across runs
	// — important for diff hygiene and snapshot tests. Each export uses
	// `export { default as <Pascal> } from "./components/<name>";` so
	// consumers can write `import { Button, DataTable } from "@my/ui-web"`.
	if err := writeUIWebBarrel(uiDir); err != nil {
		return err
	}

	readme := fmt.Sprintf("# %s\n\n"+
		"Shared React + Tailwind UI components for the project's browser\n"+
		"frontends (Next.js, Vite SPA). Replaces the per-frontend\n"+
		"`src/components/ui/` copies that single-frontend projects get.\n\n"+
		"## Ownership\n\n"+
		"`forge generate` writes these files **once**, on first scaffold.\n"+
		"After that the user owns them — re-running `forge generate` is a\n"+
		"no-op for every file under this package. Edit, delete, or rename\n"+
		"freely; nothing here will be clobbered.\n\n"+
		"## Adding a component\n\n"+
		"Drop a new `.tsx` file under `src/components/ui/`, then re-export\n"+
		"it from `src/index.ts`. Frontends can then\n"+
		"`import { MyThing } from \"%s\"`, or use the path-mapped form\n"+
		"`import MyThing from \"@/components/ui/my_thing\"` if you prefer.\n\n"+
		"## Why this isn't on npm\n\n"+
		"This package is project-specific scaffolding, not a shared\n"+
		"library. forge seeds a starting set and gets out of the way —\n"+
		"the components are yours to fork, restyle, or rewrite to match\n"+
		"your product. Publishing to npm would defeat the point.\n",
		layout.UIWebPackage, layout.UIWebPackage)
	if err := writeIfMissing(filepath.Join(uiDir, "README.md"), readme); err != nil {
		return err
	}

	return nil
}

// writeUIWebBarrel seeds packages/ui-web/src/index.ts as a re-export
// barrel over every .tsx file currently in src/components/ui/. Built
// from the on-disk file list (not the coreComponents constant) so the
// barrel reflects whatever the user has accumulated.
//
// Write-if-missing semantics, like every other ui-web file: the
// barrel is seeded on first scaffold and then owned by the user. If
// you add a new component and want it re-exported, edit the barrel —
// `forge generate` won't fight you. Stable sorted on the first emit
// so initial diffs are clean.
func writeUIWebBarrel(uiDir string) error {
	indexPath := filepath.Join(uiDir, "src", "index.ts")
	if _, err := os.Stat(indexPath); err == nil {
		return nil
	}

	componentsDir := filepath.Join(uiDir, "src", "components", "ui")
	entries, err := os.ReadDir(componentsDir)
	if err != nil {
		return fmt.Errorf("read ui-web components dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".tsx") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".tsx"))
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("// Re-export barrel for @<scope>/ui-web — seeded by forge on first\n")
	b.WriteString("// scaffold from the contents of src/components/ui/, then owned by you.\n")
	b.WriteString("// Add/remove exports below as you add/remove components; subsequent\n")
	b.WriteString("// `forge generate` runs will NOT clobber edits to this file.\n")
	b.WriteString("//\n")
	b.WriteString("// Each component file exports its component as default; the barrel\n")
	b.WriteString("// re-names it to PascalCase for ergonomic named imports:\n")
	b.WriteString("//   import { Button, DataTable } from \"@<scope>/ui-web\";\n\n")
	for _, name := range names {
		pascal := toPascalCase(name)
		fmt.Fprintf(&b, "export { default as %s } from \"./components/ui/%s\";\n", pascal, name)
	}
	if len(names) == 0 {
		// Keep the file a valid module even when there are no components.
		b.WriteString("export {};\n")
	}
	return atomicfile.WriteFile(indexPath, []byte(b.String()), 0o644)
}

// toPascalCase converts snake_case component names (the convention in
// components/library.go) to PascalCase for named exports. "data_table"
// -> "DataTable", "login_form" -> "LoginForm".
func toPascalCase(name string) string {
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// writeIfMissing writes content to path only if the file does not exist.
// Treats the file as user-owned once it's on disk — `forge generate` is
// not supposed to clobber edits to package.json, tsconfig.json, etc.
func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return atomicfile.WriteFile(path, []byte(content), 0o644)
}

// WriteUINativePackageFiles emits the packages/ui-native/ scaffold:
//
//   - packages/ui-native/package.json (workspace member)
//   - packages/ui-native/tsconfig.json (no DOM lib, React JSX)
//   - packages/ui-native/src/tokens.ts (design tokens)
//   - packages/ui-native/src/index.ts (re-export barrel)
//   - packages/ui-native/src/components/<primitive>.tsx (one per primitive)
//   - packages/ui-native/README.md
//
// Idempotent: write-if-missing throughout. Once a user edits a
// primitive (or rewrites the tokens), `forge generate` will not
// clobber the edit — the package is effectively scaffolded once and
// owned by the project after.
//
// Guarded by the two preconditions documented at the call sites in
// generate_pipeline.go and add.go:
//
//  1. `frontend.workspaces: true`
//  2. At least one frontend with `type: react-native`
//
// When either is false this function should not be called (callers
// gate); calling it directly still works but emits files that have
// no consumer.
func WriteUINativePackageFiles(projectDir string, layout FrontendWorkspaceLayout) error {
	uiDir := filepath.Join(projectDir, "packages", "ui-native")
	componentsDir := filepath.Join(uiDir, "src", "components")
	if err := os.MkdirAll(componentsDir, 0o755); err != nil {
		return fmt.Errorf("create packages/ui-native: %w", err)
	}

	pkg := fmt.Sprintf(`{
  "name": "%s",
  "version": "0.0.0",
  "private": true,
  "main": "./src/index.ts",
  "types": "./src/index.ts",
  "exports": {
    ".": "./src/index.ts",
    "./tokens": "./src/tokens.ts",
    "./components/*": "./src/components/*.tsx"
  },
  "scripts": {
    "typecheck": "tsc --noEmit"
  },
  "peerDependencies": {
    "react": "*",
    "react-native": "*",
    "react-native-safe-area-context": "*"
  },
  "devDependencies": {
    "@types/react": "~18.3.0",
    "typescript": "^5.8.0"
  }
}
`, layout.UINativePackage)
	if err := writeIfMissing(filepath.Join(uiDir, "package.json"), pkg); err != nil {
		return err
	}

	// tsconfig: NO DOM lib — this package must compile under the same
	// constraints as the React Native runtime (no document/window).
	// JSX preserved so Metro's babel pass handles it.
	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022"],
    "jsx": "react-jsx",
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true,
    "allowSyntheticDefaultImports": true
  },
  "include": ["src/**/*.ts", "src/**/*.tsx"]
}
`
	if err := writeIfMissing(filepath.Join(uiDir, "tsconfig.json"), tsconfig); err != nil {
		return err
	}

	// Copy embedded primitives + tokens. Write-if-missing so user
	// edits survive a regen.
	lib := nativecomponents.NewLibrary()
	for _, p := range lib.Primitives() {
		src, err := lib.Get(p.Name)
		if err != nil {
			return fmt.Errorf("read native primitive %s: %w", p.Name, err)
		}
		dest := filepath.Join(componentsDir, p.Name+".tsx")
		if err := writeIfMissing(dest, src); err != nil {
			return err
		}
	}
	tokens, err := lib.Tokens()
	if err != nil {
		return fmt.Errorf("read tokens: %w", err)
	}
	if err := writeIfMissing(filepath.Join(uiDir, "src", "tokens.ts"), tokens); err != nil {
		return err
	}

	// Index barrel is generated from the primitive registry so adding
	// a new primitive automatically surfaces it. Write-if-missing
	// so user-curated barrels survive — they can pin the export
	// list to whatever subset they want without forge clobbering it.
	if err := writeIfMissing(filepath.Join(uiDir, "src", "index.ts"), lib.IndexBarrel()); err != nil {
		return err
	}

	readme := fmt.Sprintf("# %s\n\n"+
		"Thin React Native primitive set for the project's mobile frontends.\n\n"+
		"This is **not** a full design system — it's ~10 primitives that mirror\n"+
		"the web component library's semantic names (Button, Card, Stack, Text,\n"+
		"…) so cross-platform code can carry the same mental model without\n"+
		"forge having to ship a Tamagui or Unistyles fork.\n\n"+
		"## Ownership\n\n"+
		"`forge generate` writes these files **once** at scaffold time. Edits to\n"+
		"any file under `src/` survive subsequent runs — the package is\n"+
		"effectively yours after the initial copy. If you want forge to re-emit\n"+
		"a primitive from the embedded source, delete the file and re-run\n"+
		"`forge generate`.\n\n"+
		"## When to outgrow this\n\n"+
		"If you need:\n\n"+
		"- A single design system across web AND native (write components once,\n"+
		"  render on both).\n"+
		"- Runtime theme switching, brand variants, a token graph.\n"+
		"- DataTable / Sidebar / NavHeader equivalents for mobile.\n\n"+
		"…install **Tamagui** or **Unistyles** and replace this package. See the\n"+
		"`ui-native-package` skill for the migration shape.\n\n"+
		"## What ships\n\n"+
		"Button, Input, Label, Card, Stack (+ HStack/VStack), Text, Spinner,\n"+
		"Switch, Pressable, SafeAreaView, plus `tokens.ts` for colors / spacing\n"+
		"/ radius / text sizes.\n\n"+
		"Frontends consume the package via `\"%s\": \"workspace:*\"` and\n"+
		"`import { Button, Stack } from \"%s\"`.\n",
		layout.UINativePackage, layout.UINativePackage, layout.UINativePackage)
	if err := writeIfMissing(filepath.Join(uiDir, "README.md"), readme); err != nil {
		return err
	}
	return nil
}
