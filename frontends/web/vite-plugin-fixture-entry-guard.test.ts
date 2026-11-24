import { describe, expect, it } from "vitest";

import { fixtureEntryReport, type GuardChunk } from "./vite-plugin-fixture-entry-guard";

const ROOT = "/repo/frontends/web";
const chunk = (fileName: string, over: Partial<GuardChunk>): GuardChunk => ({
  type: "chunk", fileName, name: fileName.replace(/-.*$/, ""), isEntry: false, imports: [], dynamicImports: [], moduleIds: [], code: "x".repeat(10), ...over,
});

describe("fixtureEntryReport", () => {
  it("walks an entry's static imports only: a lazily imported fixture chunk is outside its graph", () => {
    const bundle = {
      "index-a.js": chunk("index-a.js", { isEntry: true, name: "index", imports: ["vendor-b.js"], dynamicImports: ["install-c.js"], moduleIds: [`${ROOT}/src/main.tsx`, `${ROOT}/src/lib/fixturesBoot.ts`] }),
      "vendor-b.js": chunk("vendor-b.js", { moduleIds: [`${ROOT}/node_modules/react/index.js`] }),
      "install-c.js": chunk("install-c.js", { imports: ["vendor-b.js"], moduleIds: [`${ROOT}/src/fixtures/install.ts`, `${ROOT}/src/fixtures/shop/app.ts`] }),
      "style.css": { type: "asset" as const },
    };
    const report = fixtureEntryReport(bundle, ROOT, ["index"]);
    expect(report.entries).toEqual([{ entry: "index", chunks: ["index-a.js", "vendor-b.js"], modules: 3, bytes: 20, fixtureModules: [] }]);
    expect(report.lazyFixtureChunks).toEqual([{ fileName: "install-c.js", bytes: 10, fixtureModules: 2 }]);
  });

  it("finds a fixture module that a static import pulled into the entry's graph", () => {
    const bundle = {
      "index-a.js": chunk("index-a.js", { isEntry: true, name: "index", imports: ["shared-b.js"] }),
      "shared-b.js": chunk("shared-b.js", { moduleIds: [`${ROOT}/src/components/Home.tsx`, `\0${ROOT}/src/fixtures/shop/shopStory.ts?x`] }),
    };
    expect(fixtureEntryReport(bundle, ROOT, ["index"]).entries[0].fixtureModules).toEqual(["src/fixtures/shop/shopStory.ts"]);
  });

  it("checks only the named entries (the demo's own entry is a fixture shell)", () => {
    const bundle = { "demo-a.js": chunk("demo-a.js", { isEntry: true, name: "demo", moduleIds: [`${ROOT}/src/fixtures/shop/index.ts`] }) };
    expect(fixtureEntryReport(bundle, ROOT, ["index", "findBar"]).entries).toEqual([]);
  });
});
