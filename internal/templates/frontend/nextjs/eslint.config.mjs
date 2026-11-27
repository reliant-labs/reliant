// ESLint v9 flat config. Replaces the deprecated `next lint` + .eslintrc.json
// combo that was removed in Next.js 16. The shape of this file is
// intentionally compact: one config array entry per concern.
//
// Keep this file checked in (not generated) so users can extend it without
// fighting the scaffold.

import js from "@eslint/js";
import nextCoreWebVitals from "eslint-config-next/core-web-vitals";
import nextTypeScript from "eslint-config-next/typescript";
import importPlugin from "eslint-plugin-import";
import react from "eslint-plugin-react";
import unicorn from "eslint-plugin-unicorn";

// The jsx-a11y base every forge web frontend shares; read its header before
// adding a rule. Deviations for this project go AFTER the spread below.
//
// It also RAISES the six rules next/core-web-vitals already sets to "warn"
// (`alt-text`, `aria-props`, `aria-proptypes`, `aria-unsupported-elements`,
// `role-has-required-aria-props`, `role-supports-aria-props`) to errors.
//
// The plugin itself is registered by `next/core-web-vitals` below —
// eslint-config-next lists eslint-plugin-jsx-a11y among its plugins — so
// this file names rules without re-declaring the plugin. Two copies of the
// same plugin under one flat config is a hard "Cannot redefine plugin"
// error, and eslint-config-next's own resolver patch already prefers the
// project's copy. The Vite configs, which have no such base, declare the
// plugin themselves.
import { a11yRules } from "./eslint.a11y.mjs";

// Named so import/no-anonymous-default-export stays happy when users add
// plugins that enforce it (e.g. airbnb, next/typescript in strict mode).
const config = [
  {
    // Files ESLint must never touch: build artefacts, node_modules, and
    // generated code. Keeping this as the first entry makes the intent
    // explicit and short-circuits traversal.
    ignores: [
      "node_modules/**",
      ".next/**",
      ".next-prod/**",
      "out/**",
      "coverage/**",
      "dist/**",
      "build/**",
      // Protobuf-es output is regenerated on every `buf generate` run and
      // carries /* eslint-disable */ prologue banners that trigger the
      // `reportUnusedDisableDirectives` warning under next/core-web-vitals.
      // Treat the whole tree as read-only to silence the noise.
      "src/gen/**",
      // Next.js auto-generates next-env.d.ts on `next dev` / `next build`
      // and uses a triple-slash directive that
      // @typescript-eslint/triple-slash-reference rejects. Standard
      // convention is to ignore it — Next regenerates it as needed.
      "next-env.d.ts",
    ],
  },
  js.configs.recommended,
  // eslint-config-next 16 ships a NATIVE flat config, so these are imported
  // and spread directly. Through 15 it was eslintrc-only and had to come in
  // via FlatCompat (`compat.extends("next/core-web-vitals", …)`); on 16 that
  // shim throws "Converting circular structure to JSON" inside
  // @eslint/eslintrc, because it tries to validate an already-flat config
  // against the legacy schema. Keep these as direct imports.
  //
  // typescript-eslint comes from HERE and nowhere else: next/typescript is
  // `tseslint.configs.recommended` built from eslint-config-next's own
  // typescript-eslint, and next/core-web-vitals registers that same plugin
  // for .ts/.tsx. Do not also spread the project's `typescript-eslint`
  // package: the day the two copies resolve to different versions (a
  // dependency bump the lockfile does not dedupe), ESLint refuses the whole
  // config with `Cannot redefine plugin "@typescript-eslint"`.
  ...nextCoreWebVitals,
  ...nextTypeScript,
  {
    plugins: {
      react,
      import: importPlugin,
      unicorn,
    },
    settings: {
      // Classify "@/..." tsconfig-path imports as the "internal" group
      // deterministically. Without this, eslint-plugin-import can't
      // resolve the alias and files drift between "unknown" orderings —
      // generated code must lint identically on every machine.
      "import/internal-regex": "^@/",
    },
    rules: {
      ...a11yRules,
      complexity: "off",
      "max-lines": "off",
      "max-depth": "off",
      "max-params": "off",
      // Honour the universal underscore-prefix stub idiom. A leading `_`
      // is the cross-ecosystem signal for "intentionally unused" — unused
      // function args (`_ctx`), placeholder bindings (`_eventName`), and
      // swallowed catch clauses (`catch (_err)`). typescript-eslint's
      // recommended preset enables no-unused-vars with NO ignore patterns,
      // so without this the idiom warns in every scaffolded file. Turn off
      // the base `no-unused-vars` (superseded by the typed rule) to avoid
      // double-reporting.
      "no-unused-vars": "off",
      "@typescript-eslint/no-unused-vars": [
        "warn",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
      "react/forbid-dom-props": [
        "warn",
        {
          forbid: [
            {
              propName: "style",
              message:
                "Prefer Tailwind utilities, CSS variables, or component variants. Use inline styles only for dynamic values that cannot be expressed safely in CSS.",
            },
          ],
        },
      ],
      // Import hygiene — block circular deps outright; nudge users toward
      // named exports and grouped/sorted imports. Errors on the cycle
      // case because cycles silently break code-splitting + cause subtle
      // initialisation bugs; warn-only on the others so users can ratchet.
      "import/no-cycle": ["error", { maxDepth: 10 }],
      "import/no-default-export": "warn",
      "import/order": [
        "warn",
        {
          groups: [
            "builtin",
            "external",
            "internal",
            "parent",
            "sibling",
            "index",
            "object",
            "type",
          ],
          "newlines-between": "always",
          alphabetize: { order: "asc", caseInsensitive: true },
        },
      ],
      // Selective unicorn rules. The plugin as a whole is too noisy
      // (e.g. no-array-for-each fires on idiomatic React .map adjacent
      // patterns); these three are uncontroversial wins.
      "unicorn/prefer-string-trim-start-end": "warn",
      "unicorn/prefer-set-has": "warn",
      "unicorn/prefer-includes": "warn",
    },
  },
  {
    // Default exports are the CONTRACT in three places, so the
    // import/no-default-export nudge is noise there:
    //   - src/app/**: Next.js App Router entry files (page, layout,
    //     loading, error, global-error, not-found, template, route) must
    //     default-export. Scoping the whole dir beats enumerating the
    //     conventions and silently missing one (global-error.tsx used to
    //     slip through).
    //   - src/components/ui/**: the forge component library ships one
    //     default-exported component per file (shadcn-style); generated
    //     pages import them as defaults.
    //   - src/mocks/scenarios/**: scenario files default-export
    //     `defineScenario({...})` so the registry barrel can re-export
    //     them by name.
    // Tooling configs (next.config.ts, vitest.config.ts, postcss, ...)
    // default-export by ecosystem convention — the `**/` prefix on the
    // config glob is load-bearing: bare `*.config.*` matches only the
    // first path segment under ESLint flat config's minimatch flavour.
    files: [
      "src/app/**/*.{ts,tsx}",
      "src/components/ui/**/*.{ts,tsx}",
      "src/middleware.{ts,tsx}",
      "src/mocks/scenarios/**/*.{ts,tsx}",
      "next.config.{js,ts,mjs}",
      "**/*.config.{js,ts,mjs}",
    ],
    rules: {
      "import/no-default-export": "off",
    },
  },
  {
    // The forge component library under src/components/ui/** is
    // shadcn-derived: installed via the shadcn CLI and OWNED by the
    // library, not hand-authored per project, so forge can't rewrite it to
    // satisfy the project-wide nudges. Two patterns it legitimately uses:
    //   - react/forbid-dom-props: primitives like progress/chart/resizable
    //     set inline `style` for RUNTIME-computed values (widths, transforms)
    //     that cannot be expressed as static Tailwind utilities.
    //   - @next/next/no-img-element: the avatar primitive renders a raw
    //     <img> fallback (Radix Avatar), which is correct — next/image
    //     would fight its intrinsic sizing.
    // Scope the exemptions to the library dir so app code still gets nudged.
    files: ["src/components/ui/**/*.{ts,tsx}"],
    rules: {
      "react/forbid-dom-props": "off",
      "@next/next/no-img-element": "off",
    },
  },
  {
    // The theme provider projects a brand row into CSS custom properties and
    // sets them inline — the one place in the scaffold where the style prop is
    // the mechanism rather than a shortcut, since the values exist only at
    // runtime. The exemption lives HERE rather than as an
    // `eslint-disable-next-line` in the file because theme-provider.tsx ships
    // byte-identical into the Vite SPA tree, whose config does not load
    // eslint-plugin-react: a directive naming an unloaded rule is a hard error
    // under that config, not a no-op. Same reason the component-library
    // exemption above is declared by path.
    files: ["src/lib/theme/theme-provider.tsx"],
    rules: {
      "react/forbid-dom-props": "off",
    },
  },
  {
    // eslint-config-next 16 pulls eslint-plugin-react-hooks v7 (15 pinned
    // v5), whose `recommended` set turns the React Compiler diagnostics into
    // ERRORS. Two of them fire on scaffold code that is correct as written:
    //
    //   - set-state-in-effect: the scaffold's mount-only effects
    //     (`useEffect(() => setHydrated(true), [])` in route-guard.tsx and
    //     session_nav.tsx, the stored-theme read in use-theme.ts, the session
    //     restore in auth/context.tsx, the highlight reset in
    //     entity-picker.tsx). These exist BECAUSE the value is not knowable
    //     during SSR — reading localStorage or a session cookie in render
    //     would produce a hydration mismatch, which is the bug this rule's
    //     own suggested fix would introduce here.
    //   - refs: event-context.tsx assigns `handlerRef.current = handler`
    //     during render, the standard latest-handler idiom that keeps the
    //     bus subscription from re-subscribing on every render.
    //
    // Downgraded to "warn" rather than "off": the rules are genuinely useful
    // on app code written later, and the scaffold should not silence a class
    // of real bug for the life of the project. They are not errors because a
    // freshly scaffolded project must pass `npm run lint` clean.
    //
    // Declared project-wide rather than per-file because five of the six
    // files ship byte-identical into the Vite SPA tree as well; a
    // path-scoped list here would drift from that copy the moment either
    // moves. Revisit if the React Compiler is adopted (`reactCompiler: true`
    // in next.config.ts), which changes the calculus on these patterns.
    rules: {
      "react-hooks/set-state-in-effect": "warn",
      "react-hooks/refs": "warn",
    },
  },
];

export default config;
