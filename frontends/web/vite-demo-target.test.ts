// @vitest-environment node
import { readFileSync } from "node:fs";
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
    expect(config.build.rolldownOptions.input).toEqual({ demo: path.resolve(__dirname, "demo.html") });
    expect(config.plugins.map((plugin) => plugin.name)).toEqual(expect.arrayContaining([
      "reliant:demo-substitutes", "reliant:demo-document", "reliant:demo-bundle-guard",
    ]));
    const output = config.build.rolldownOptions.output as { codeSplitting: { groups: { name: string }[] } };
    expect(output.codeSplitting.groups.map((group) => group.name)).not.toContain("vendor-monaco");
  });

  it("the demo document is noindex at every URL; the app's document is not", () => {
    // The demo host serves demo.html (as index.html) for /forge/demo and every
    // deep state URL, so one static tag in its <head> covers them all.
    const robots = (file: string) =>
      [...readFileSync(path.resolve(__dirname, file), "utf8").matchAll(/<meta\s+name="robots"\s+content="([^"]*)"/g)].map((m) => m[1]);
    expect(robots("demo.html")).toEqual(["noindex"]);
    expect(robots("index.html")).toEqual([]);
  });

  it("an ordinary app build keeps its app document and output", async () => {
    const config = await selectedConfig("production");
    expect(config.define?.__WEB_TARGET__).toBe(JSON.stringify("app"));
    expect(config.build?.outDir).toBeUndefined(); // Vite's default dist
    expect(config.build?.rolldownOptions?.input).toEqual({
      index: path.resolve(__dirname, "index.html"),
      findBar: path.resolve(__dirname, "find-bar.html"),
    });
  });
});
