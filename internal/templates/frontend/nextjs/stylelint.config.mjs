// CSS lint lane for this frontend. `npm run lint:styles` checks; `npm run
// lint:styles:fix` applies stylelint's autofix.
//
// The stylelint floor in package.json (>= 17.2.0) is load-bearing, not
// housekeeping. Before 17.2.0 the `lightness-notation` autofix rewrote an
// oklch lightness through `(0.58 * 100).toPrecision(3)` and then trimmed
// trailing zeros with /0+$/, so `oklch(0.58 0.09 196)` became
// `oklch(58.% 0.09 196deg)` — `58.` is not a valid CSS <number>, the browser
// drops the whole declaration, and a re-lint passes because `58.%` still
// reads as a percentage. Eight two-decimal lightnesses hit it (.07 .14 .28
// .29 .55 .56 .57 .58, where x*100 lands off-integer in binary floating
// point). 17.2.0 trims with /\.?0+$/. Do not lower the floor.
const tailwindAtRules = [
  "theme",
  "source",
  "utility",
  "variant",
  "custom-variant",
  "plugin",
  "reference",
  "config",
  "layer",
  "apply",
];

const config = {
  extends: ["stylelint-config-standard"],
  ignoreFiles: [".next/**", "coverage/**", "dist/**", "build/**", "src/gen/**"],
  rules: {
    "at-rule-no-unknown": [
      true,
      {
        ignoreAtRules: tailwindAtRules,
      },
    ],
    "declaration-no-important": true,
    // Tailwind v4 documents `@import "tailwindcss";` as the canonical entry
    // point. stylelint-config-standard prefers `url("tailwindcss")` notation;
    // turn that rule off so the standard Tailwind v4 setup lints clean.
    "import-notation": null,
    // This rule and prettier disagree, and prettier wins because it runs on
    // the same files. The rule demands a blank line before a custom property
    // whose PRECEDING declaration spans multiple lines — and prettier is what
    // decides which declarations wrap, re-wrapping any `oklch(...)` that
    // exceeds the print width. So `--fix` inserts blank lines, prettier then
    // re-wraps a different set of values, and the next lint reports a fresh
    // set of violations: on the scaffolded globals.css that went 3 -> 6 in one
    // round trip, and it does not converge.
    //
    // It is also purely cosmetic (a blank line inside @theme) and the scaffold
    // ships both tools, so a project forge generated failed `npm run
    // lint:styles` out of the box on three lines nobody wrote by hand.
    // Formatting inside @theme is prettier's job; this rule is the one that
    // has to yield.
    "custom-property-empty-line-before": null,
  },
};

export default config;
