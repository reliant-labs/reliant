// ESLint v9 flat config for the site. `npm run lint` runs it. Yours to extend.
import js from "@eslint/js";
import nextCoreWebVitals from "eslint-config-next/core-web-vitals";
import nextTypeScript from "eslint-config-next/typescript";

// The jsx-a11y base every forge web frontend shares: a control with no
// accessible name, no keyboard path or a role that lies is an error. Read its
// header before adding a rule; deviations go AFTER the spread below. The
// plugin is registered by next/core-web-vitals, so it is not declared here
// (two copies of one plugin is a "Cannot redefine plugin" error).
import { a11yRules } from "./eslint.a11y.mjs";

const config = [
  {
    ignores: [
      "node_modules/**",
      ".next/**",
      "out/**",
      // Next.js rewrites it on every `next dev` / `next build`.
      "next-env.d.ts",
      // The runtime config `forge generate` renders per environment: a
      // classic script that assigns window.__FORGE_CONFIG__.
      "public/config.js",
    ],
  },
  js.configs.recommended,
  ...nextCoreWebVitals,
  ...nextTypeScript,
  {
    rules: {
      ...a11yRules,
    },
  },
];

export default config;
