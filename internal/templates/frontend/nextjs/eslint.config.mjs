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
import tseslint from "typescript-eslint";

// ── Accessibility (jsx-a11y) ───────────────────────────────────────────
//
// EXTRACTION SEAM — read this before adding a rule here.
//
// `a11yRules` is a NAMED BASE, spread into the rules object below; anything
// declared after the spread overrides it. That shape is deliberate, because
// this file is scaffold-once: forge writes it when the frontend is created
// and never again — it is not in generator.managedFiles(), so
// `forge project upgrade` does not refresh it either. Rules inlined here are
// frozen for the life of the project, so a rule forge later fixes, or a newly
// useful one, never reaches an app that already exists.
//
// The answer to that is a published eslint config package both scaffolds
// import, so `npm update` carries rule changes into existing projects; that
// is a release-surface decision and is not taken here. Until it is, the move
// must stay MECHANICAL: replace `const a11yRules = {…}` with an import of the
// package's base and change nothing else. Two things keep it that way —
// TestBothESLintConfigsShareOneA11yBlock fails if the Next.js and Vite copies
// drift, and per-project deviations belong AFTER the spread, never inside it.
//
// Do not split this into a forge-generated eslint fragment in the meantime.
// It would buy only "no npm publish", at the price of a config mechanism
// nothing else in the ecosystem has — and it would have to be unwound when
// the package lands.
//
// Calibrated the way internal/templates/project/golangci.yml is: a rule
// earns its place by finding a BROKEN CONTROL — one with no accessible
// name, no keyboard path, or a role that lies about what it is. A rule that
// fires on idiomatic React is not finding bugs, it is teaching you to skim
// past lint output. Every rule below is silent on the code forge itself
// scaffolds; the ones that were not are listed under "NOT enabled".
//
// ERROR, not warn. `eslint .` exits 0 on warnings, so a warn-level a11y
// rule is a notice, not a gate — and a control a screen-reader user cannot
// name is a defect, not a preference. That includes the six rules
// next/core-web-vitals already sets: this block RAISES them rather than
// re-deriving them (`alt-text`, `aria-props`, `aria-proptypes`,
// `aria-unsupported-elements`, `role-has-required-aria-props`,
// `role-supports-aria-props` ship from Next at "warn").
//
// The plugin itself is registered by `next/core-web-vitals` below —
// eslint-config-next lists eslint-plugin-jsx-a11y among its plugins — so
// this file names rules without re-declaring the plugin. Two copies of the
// same plugin under one flat config is a hard "Cannot redefine plugin"
// error, and eslint-config-next's own resolver patch already prefers the
// project's copy. The Vite SPA config, which has no such base, declares the
// plugin itself and carries this same block.
const a11yRules = {
  // ── ARIA that silently does nothing ──
  // A misspelled aria-* attribute, a value of the wrong type, or a role
  // that is not a real role does not fail loudly: the browser drops it and
  // the control ships unnamed or mis-announced. Nothing else in the
  // toolchain looks at these strings — tsc types `role` as `string`.
  "jsx-a11y/aria-props": "error",
  "jsx-a11y/aria-proptypes": "error",
  "jsx-a11y/aria-role": "error",
  "jsx-a11y/aria-unsupported-elements": "error",
  "jsx-a11y/role-has-required-aria-props": "error",
  "jsx-a11y/role-supports-aria-props": "error",
  // A composite widget that points aria-activedescendant at an option, and
  // an aria-hidden wrapper around something still in the tab order, are
  // both "focus goes somewhere the screen reader cannot follow".
  "jsx-a11y/aria-activedescendant-has-tabindex": "error",
  "jsx-a11y/no-aria-hidden-on-focusable": "error",
  // autocomplete="firstname" (not a token) turns password managers and
  // form autofill off silently. Same typo family.
  "jsx-a11y/autocomplete-valid": "error",

  // ── Controls and content with no accessible name ──
  // The defect these catch is an element that IS a control but announces
  // as nothing: an icon-only <button> holding only an <svg>, a
  // role="progressbar" with no label, an <a> with no text, an empty <h2>.
  //
  // control-has-associated-label is OFF in jsx-a11y's own recommended
  // preset; it is on here because on forge's component library it found
  // five real ones (four icon-only dismiss/clear buttons and an unnamed
  // progress bar) and zero false positives. Its ignoreElements list is the
  // preset's, and it is LOAD-BEARING, not a soft-pedal: ESLint sees one
  // JSX element at a time and cannot resolve `htmlFor="x"` to `id="x"` on
  // a sibling, so with <input>/<textarea> in scope the rule flags the
  // correctly-labelled control exactly as loudly as the unlabelled one.
  // A rule that cannot tell those apart cannot gate either. Native form
  // controls are covered at render time instead — `getByLabelText` in the
  // component test resolves the id graph a linter cannot.
  "jsx-a11y/control-has-associated-label": [
    "error",
    {
      ignoreElements: [
        "audio",
        "canvas",
        "embed",
        "input",
        "textarea",
        "tr",
        "video",
      ],
      ignoreRoles: [
        "grid",
        "listbox",
        "menu",
        "menubar",
        "radiogroup",
        "row",
        "tablist",
        "toolbar",
        "tree",
        "treegrid",
      ],
    },
  ],
  "jsx-a11y/label-has-associated-control": "error",
  "jsx-a11y/alt-text": "error",
  "jsx-a11y/anchor-has-content": "error",
  "jsx-a11y/heading-has-content": "error",
  "jsx-a11y/iframe-has-title": "error",
  "jsx-a11y/html-has-lang": "error",
  "jsx-a11y/lang": "error",

  // ── Keyboard and pointer parity ──
  // A <div> wired to onClick is a button that only a mouse can press, and
  // an element given an interactive role but no tab stop is a control the
  // keyboard cannot reach at all. Between them these two cover the
  // div-as-button defect from both ends.
  "jsx-a11y/no-static-element-interactions": "error",
  "jsx-a11y/interactive-supports-focus": "error",
  // Same defect on elements that carry a non-interactive role of their own
  // (<li>, <p>, <img>). Unlike click-events-have-key-events this one reads
  // the explicit `role`, so it stays quiet on a correct ARIA listbox.
  "jsx-a11y/no-noninteractive-element-interactions": "error",
  // Roles that contradict the tag: <button role="presentation"> is
  // unreachable, <li role="button"> breaks the list it lives in. The
  // option maps are the plugin's recommended ones — they permit exactly
  // the composite-widget shapes ARIA blesses (ul→listbox, li→option),
  // which is what forge's EntityPicker builds.
  "jsx-a11y/no-interactive-element-to-noninteractive-role": [
    "error",
    { tr: ["none", "presentation"], canvas: ["img"] },
  ],
  "jsx-a11y/no-noninteractive-element-to-interactive-role": [
    "error",
    {
      ul: ["listbox", "menu", "menubar", "radiogroup", "tablist", "tree", "treegrid"],
      ol: ["listbox", "menu", "menubar", "radiogroup", "tablist", "tree", "treegrid"],
      li: ["menuitem", "menuitemradio", "menuitemcheckbox", "option", "row", "tab", "treeitem"],
      table: ["grid"],
      td: ["gridcell"],
      fieldset: ["radiogroup", "presentation"],
    },
  ],
  "jsx-a11y/no-noninteractive-tabindex": [
    "error",
    { tags: [], roles: ["tabpanel"], allowExpressionValues: true },
  ],
  // A positive tabIndex re-orders the whole page's tab sequence, not just
  // this element's — always a bug, never a style choice.
  "jsx-a11y/tabindex-no-positive": "error",
  // Hover-only affordances (a menu that opens on onMouseOver with no
  // onFocus) are invisible to the keyboard. Scoped to mouseover/mouseout
  // by default, so decorative onMouseEnter tinting does not trip it.
  "jsx-a11y/mouse-events-have-key-events": "error",
  // An <a> with no href, or href="#"/"javascript:", is announced as a link
  // and cannot be reached by keyboard. If it acts like a button, it is a
  // <button>.
  "jsx-a11y/anchor-is-valid": "error",
  // accessKey collides with screen-reader and browser shortcuts.
  "jsx-a11y/no-access-key": "error",
  // scope="col" on anything but a <th> is dropped; <marquee>/<blink> are
  // dead HTML that still moves. Both free.
  "jsx-a11y/scope": "error",
  "jsx-a11y/no-distracting-elements": "error",

  // ── NOT enabled, and why ──
  //
  // click-events-have-key-events — the only rule in this family that
  //   ignores the explicit `role`, so it cannot see keyboard handling that
  //   lives on a composite widget's CONTAINER. It fires on every ARIA APG
  //   listbox (`<li role="option" onClick>`), including forge's own
  //   EntityPicker, where arrow-keys and Enter are wired on the search
  //   input via aria-activedescendant and the option rows are correctly
  //   not focusable. Its only repair there is a keydown handler on an
  //   element that can never receive focus — dead code that reads like a
  //   contract. no-static-element-interactions + interactive-supports-focus
  //   above catch the defect it is aimed at. The residue it alone would
  //   have caught: `<div role="button" tabIndex={0} onClick>` with no key
  //   handler — an author who got role and focusability right and stopped
  //   one step short.
  //
  // no-autofocus — autoFocus on a search page or a modal's first field is
  //   a considered product decision, and the rule cannot tell it from a
  //   careless one. A preference, not a bug.
  //
  // prefer-tag-over-role — tells you to swap <select size> in for a
  //   server-paged combobox and <progress> in for a styled bar. It fires
  //   on five correct composite widgets in the scaffold and is right about
  //   none of them.
  //
  // img-redundant-alt / anchor-ambiguous-text — prose review ("image of…",
  //   "click here"). Real advice, but content is not a control, and
  //   anchor-ambiguous-text is off in the plugin's own preset too.
  //
  // media-has-caption — a caption is a FILE, not a code change, so the
  //   only repair available to the author of a decorative muted <video> is
  //   a suppression. That is the shape of rule that trains you to reach
  //   for eslint-disable.
  //
  // no-redundant-roles — <ul role="list"> is redundant, not broken.
};

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
  ...tseslint.configs.recommended,
  // eslint-config-next 16 ships a NATIVE flat config, so these are imported
  // and spread directly. Through 15 it was eslintrc-only and had to come in
  // via FlatCompat (`compat.extends("next/core-web-vitals", …)`); on 16 that
  // shim throws "Converting circular structure to JSON" inside
  // @eslint/eslintrc, because it tries to validate an already-flat config
  // against the legacy schema. Keep these as direct imports.
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
