import { defineConfig } from "vitest/config";

// "#gen/*" is the SDK's own generated code in the published package and the
// host's single copy inside frontends/web; tests run against the SDK's.
// The React hooks' tests run in jsdom (a `// @vitest-environment jsdom` line in
// each .test.tsx); everything else stays in node.
export default defineConfig({
  resolve: {
    alias: [{ find: /^#gen\/(.*)\.js$/, replacement: new URL("./src/gen/$1", import.meta.url).pathname }],
  },
  test: { include: ["src/**/*.test.{ts,tsx}"], environment: "node" },
});
