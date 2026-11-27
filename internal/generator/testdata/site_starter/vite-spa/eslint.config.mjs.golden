// ESLint v9 flat config for the site. `npm run lint` runs it. Yours to extend.
import js from "@eslint/js";
import jsxA11y from "eslint-plugin-jsx-a11y";
import tseslint from "typescript-eslint";

// The jsx-a11y base every forge web frontend shares: a control with no
// accessible name, no keyboard path or a role that lies is an error. Read its
// header before adding a rule; deviations go AFTER the spread below.
import { a11yRules } from "./eslint.a11y.mjs";

const config = [
  {
    ignores: [
      "node_modules/**",
      "dist/**",
      // The runtime config `forge generate` renders per environment: a
      // classic script that assigns window.__FORGE_CONFIG__.
      "public/config.js",
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    plugins: { "jsx-a11y": jsxA11y },
    rules: {
      ...a11yRules,
    },
  },
];

export default config;
