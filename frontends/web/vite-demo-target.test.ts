// @vitest-environment node
import path from "node:path";
import { describe, expect, it } from "vitest";
import { resolveConfig, type ConfigEnv, type UserConfig } from "vite";
import viteConfig, { isDemoBuild } from "./vite.config";

/** Evaluate the real config exactly as forge's `npm run build` sees it. */
async function selectedConfig(mode: string, target?: string) {
  const env: ConfigEnv = { command: "build", mode };
  const previous = process.env.WEB_BUILD_TARGET;
  try {
    if (target === undefined) delete process.env.WEB_BUILD_TARGET;
    else process.env.WEB_BUILD_TARGET = target;
    return typeof viteConfig === "function" ? await viteConfig(env) : await viteConfig as UserConfig;
  } finally {
    if (previous === undefined) delete process.env.WEB_BUILD_TARGET;
    else process.env.WEB_BUILD_TARGET = previous;
  }
}

describe("forge-demo build target (the actual Vite config)", () => {
  it("selects demo only for demo mode or the env's explicit WEB_BUILD_TARGET", () => {
    expect(isDemoBuild("production", {})).toBe(false);
    expect(isDemoBuild("demo", {})).toBe(true);
    expect(isDemoBuild("production", { WEB_BUILD_TARGET: "demo" })).toBe(true);
    expect(isDemoBuild("production", { WEB_BUILD_TARGET: "app" })).toBe(false);
  });

  it.each([
    ["production", "demo"], // forge env_vars, without --mode demo
    ["demo", undefined], // npm run build:demo
  ])("%s / WEB_BUILD_TARGET=%s emits only the guarded standalone demo", async (mode, target) => {
    const selected = await selectedConfig(mode, target);
    // Resolve plugins as Vite does, without starting a server or doing a build.
    const config = await resolveConfig({ ...selected, configFile: false, mode }, "build", mode);
    expect(config.define.__WEB_TARGET__).toBe(JSON.stringify("demo"));
    expect(config.build.outDir).toBe("dist-demo");
    expect(config.build.rollupOptions.input).toEqual({ demo: path.resolve(__dirname, "demo.html") });
    expect(config.plugins.map((plugin) => plugin.name)).toEqual(expect.arrayContaining([
      "reliant:demo-substitutes", "reliant:demo-document", "reliant:demo-bundle-guard",
    ]));
    const output = config.build.rollupOptions.output as { manualChunks: Record<string, string[]> };
    expect(output.manualChunks).not.toHaveProperty("vendor-monaco");
  });

  it("an ordinary app build keeps its app document and output", async () => {
    const config = await selectedConfig("production");
    expect(config.define?.__WEB_TARGET__).toBe(JSON.stringify("app"));
    expect(config.build?.outDir).toBeUndefined(); // Vite's default dist
    expect(config.build?.rollupOptions?.input).toEqual({
      main: path.resolve(__dirname, "index.html"),
      findBar: path.resolve(__dirname, "find-bar.html"),
    });
  });
});
