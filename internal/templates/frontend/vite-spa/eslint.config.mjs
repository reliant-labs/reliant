// ESLint v9 flat config for the Vite SPA scaffold.
import js from "@eslint/js";
import jsxA11y from "eslint-plugin-jsx-a11y";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import tseslint from "typescript-eslint";

// The jsx-a11y base every forge web frontend shares; read its header before
// adding a rule. Deviations for this project go AFTER the spread below.
import { a11yRules } from "./eslint.a11y.mjs";

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
  {
    // data_table.tsx exports STACK, the class map that lets a table DataTable
    // cannot draw stack the same way on a phone. It is an object, which
    // allowConstantExport does not cover, so the rule warns on every new
    // project. Exempted here by path, not in the file: the component ships
    // byte-identical into the Next.js tree, whose config does not load
    // react-refresh, and a directive naming an unloaded rule is a hard error
    // there (frontend_cross_kind_lint_test.go). The cost is HMR falling back
    // to a full reload when that one file is edited.
    files: ["src/components/ui/data_table.tsx"],
    rules: {
      "react-refresh/only-export-components": "off",
    },
  },
];

export default config;
