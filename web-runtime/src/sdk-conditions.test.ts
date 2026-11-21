// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Which SDK build a bundler gets for "#hyperdx-otel-web", the package.json
// import map sdk-client.ts loads @hyperdx/otel-web through. A real bundler
// (esbuild) resolves the published dist/ here; nothing is mapped by hand.
//
//   - A browser bundle gets the SDK's own ESM build, as its own lazy chunk,
//     and never dist/hyperdx-otel-web.node.js. That holds even when the
//     bundler also sets the `node` condition: `browser` is listed first.
//   - Node gets dist/hyperdx-otel-web.node.js (sdk-client-node.test.ts proves
//     it starts and exports a span).
//   - That Node build needs nothing of Node itself, no builtin and no
//     require(), so a bundler that reached it anyway would not break.
//
// `pretest` builds dist/ first, as published-surface.test.ts relies on.
import { readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { build, type Metafile } from "esbuild";
import { describe, expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const NODE_BUILD = "dist/hyperdx-otel-web.node.js";
const SDK_ESM = "node_modules/@hyperdx/otel-web/dist/esm/index.js";

async function bundle(
  entry: string,
  options: { platform: "browser" | "node"; conditions?: string[] },
): Promise<{ inputs: string[]; outputs: string[] }> {
  const result = await build({
    entryPoints: [join(pkgDir, entry)],
    absWorkingDir: pkgDir,
    bundle: true,
    splitting: true,
    format: "esm",
    outdir: "out",
    write: false,
    metafile: true,
    logLevel: "silent",
    // The app's copy, as in an app's bundle; not what is under test.
    external: ["@opentelemetry/api"],
    ...options,
  });
  const meta = result.metafile as Metafile;
  return {
    inputs: Object.keys(meta.inputs).map((p) =>
      relative(pkgDir, resolve(pkgDir, p)),
    ),
    outputs: result.outputFiles.map((f) => f.text),
  };
}

describe('"#hyperdx-otel-web" as bundlers resolve it', () => {
  it("gives a browser bundle the SDK's ESM build, as a lazy chunk, never the Node build", async () => {
    const { inputs, outputs } = await bundle("dist/telemetry.js", {
      platform: "browser",
    });
    expect(inputs).toContain(SDK_ESM);
    expect(inputs).not.toContain(NODE_BUILD);
    // The SDK is still its own chunk: the entry imports it with import().
    expect(outputs.length).toBeGreaterThan(1);
    for (const text of outputs) {
      expect(text).not.toMatch(/from\s*["']node:/);
    }
  }, 60_000);

  it("prefers the browser build when a browser bundler also sets the node condition", async () => {
    const { inputs } = await bundle("dist/sdk-client.js", {
      platform: "browser",
      conditions: ["node"],
    });
    expect(inputs).toContain(SDK_ESM);
    expect(inputs).not.toContain(NODE_BUILD);
  }, 60_000);

  it("gives Node the pre-bundled Node build", async () => {
    const { inputs } = await bundle("dist/sdk-client.js", {
      platform: "node",
    });
    expect(inputs).toContain(NODE_BUILD);
    expect(inputs).not.toContain(SDK_ESM);
  }, 60_000);

  it("keeps the Node build free of Node: one import, @opentelemetry/api, and no require()", () => {
    const body = readFileSync(join(pkgDir, NODE_BUILD), "utf8");
    const imports = new Set(
      [...body.matchAll(/(?:from|import)\s*["']([^"']+)["']/g)].map(
        (m) => m[1],
      ),
    );
    expect([...imports]).toEqual(["@opentelemetry/api"]);
    expect(body).not.toMatch(/["']node:/);
    // esbuild's shim for a require() it could not bundle, which needs Node's
    // require at run time. None may remain.
    expect(body).not.toContain("Dynamic require of");
  });
});
