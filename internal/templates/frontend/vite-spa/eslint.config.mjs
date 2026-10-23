// ESLint v9 flat config for the Vite SPA scaffold.
import js from "@eslint/js";
import jsxA11y from "eslint-plugin-jsx-a11y";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
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
// name is a defect, not a preference.
//
// This block is byte-identical to the one in the Next.js scaffold's
// eslint.config.mjs, and that matters: the component library at
// src/components/ui/**, and everything under shared-web/, ships the SAME
// BYTES into both trees. A rule enabled in one config and not the other
// makes the same file clean in one frontend and red in the other.
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

const config = [
  {
    ignores: [
      "node_modules/**",
      "dist/**",
      "build/**",
      "coverage/**",
      // Protobuf-es output is regenerated on every `buf generate` run.
      "src/gen/**",
      // The runtime config `forge generate` renders per environment: a
      // generated classic script that assigns window.__FORGE_CONFIG__, which
      // the module-scoped rules above would read as an undefined global.
      "public/config.js",
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    plugins: {
      "jsx-a11y": jsxA11y,
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      ...a11yRules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
    },
  },
];

export default config;
