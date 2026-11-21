import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";

import { reliantOnlyRule, twinOf } from "./vite-plugin-demo-bundle";

const root = __dirname;
const at = (rel: string) => path.join(root, rel);

/** Every demo twin under src/, relative to frontends/web. */
function twins(dir = at("src")): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return entry.name === "__tests__" ? [] : twins(full);
    return /\.demo\.tsx?$/.test(entry.name) ? [path.relative(root, full)] : [];
  });
}

describe("reliantOnlyRule", () => {
  it("names Reliant-only modules, pnpm's package layout included", () => {
    expect(reliantOnlyRule(at("node_modules/.pnpm/@supabase+auth-js@2.90.1/node_modules/@supabase/auth-js/dist/module/GoTrueClient.js"), root)?.why).toBe("Supabase sign-in");
    expect(reliantOnlyRule(at("src/store/chatStore.ts"), root)?.why).toBe("Reliant chat");
    expect(reliantOnlyRule(at("src/store/authStore.ts"), root)?.why).toBe("Reliant auth");
    expect(reliantOnlyRule(at("src/components/Onboarding/journeys/catalog.ts"), root)).toBeDefined();
    expect(reliantOnlyRule(at("src/components/Settings/AISettings.tsx"), root)).toBeDefined();
  });

  it("passes the forge console, the demo's own code and the listed exceptions", () => {
    expect(reliantOnlyRule(at("src/components/Forge/Console/ConsoleLayout.tsx"), root)).toBeUndefined();
    expect(reliantOnlyRule(at("src/components/Onboarding/JourneyTour.tsx"), root)).toBeUndefined();
    expect(reliantOnlyRule(at("src/lib/chatMachine.demo.ts"), root)).toBeUndefined();
    expect(reliantOnlyRule(at("src/components/Settings/providers/accountFormat.ts"), root)).toBeUndefined();
    expect(reliantOnlyRule("\0vite/preload-helper.js", root)).toBeUndefined();
  });
});

describe("demo twins", () => {
  it("resolves a module to its twin, and nothing else", () => {
    expect(twinOf(at("src/lib/connect.ts"))).toBe(at("src/lib/connect.demo.ts"));
    expect(twinOf(at("src/lib/connect.demo.ts"))).toBeUndefined();
    expect(twinOf(at("src/lib/shell.ts"))).toBeUndefined();
  });

  it("gives demo routes a stable anonymous epoch, without exporting real auth operations", async () => {
    const real = at("src/lib/auth/sessionOwner.ts");
    const twin = at("src/lib/auth/sessionOwner.demo.ts");
    expect(reliantOnlyRule(real, root)?.why).toBe("Reliant auth");
    expect(twinOf(real)).toBe(twin);
    expect(reliantOnlyRule(twin, root)).toBeUndefined();

    const demo = await import("./src/lib/auth/sessionOwner.demo");
    expect(Object.keys(demo)).toEqual(["currentSessionOwner"]);
    const owner = demo.currentSessionOwner();
    expect(owner).toEqual({ userId: null, epoch: 0 });
    expect(demo.currentSessionOwner()).toBe(owner);
    expect(Object.isFrozen(owner)).toBe(true);
  });

  it("rejects session-owner mutators at demo bundle time", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "demo-session-owner-"));
    const entry = path.join(dir, "entry.ts");
    const config = path.join(dir, "vite.config.ts");
    try {
      fs.writeFileSync(entry, 'import { beginSessionTransition } from "session-owner-fixture";\nbeginSessionTransition();\n');
      fs.writeFileSync(config, `
        import { demoSubstitutes } from ${JSON.stringify(at("vite-plugin-demo-bundle.ts"))};
        export default {
          root: ${JSON.stringify(dir)},
          plugins: [{
            name: "session-owner-fixture",
            resolveId(source) {
              return source === "session-owner-fixture" ? ${JSON.stringify(at("src/lib/auth/sessionOwner.ts"))} : null;
            },
          }, demoSubstitutes()],
          build: { write: false, minify: false, rollupOptions: { input: ${JSON.stringify(entry)} } },
        };
      `);
      // Run the real CLI outside Vitest's VM, where native bundler bindings
      // cannot receive plugin objects from the test realm.
      const result = spawnSync(process.execPath, [at("node_modules/vite/bin/vite.js"), "build", "--config", config], {
        cwd: root,
        encoding: "utf8",
        timeout: 15_000,
      });
      expect(result.error).toBeUndefined();
      expect(result.status).toBe(1);
      expect(result.stderr).toMatch(/"beginSessionTransition" is not exported by .*sessionOwner\.demo\.ts/);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }, 20_000);

  // The first twin pays the cold import of the app's module graph (~2.5-3s
  // on an idle host), which a loaded gate stretches past vitest's default 5s.
  it.each(twins())("%s replaces a real module and exports only names it has", async (twin) => {
    const real = twin.replace(/\.demo(\.tsx?)$/, "$1");
    const realFile = [real, real.replace(/\.tsx?$/, ".ts"), real.replace(/\.tsx?$/, ".tsx")].find((f) => fs.existsSync(at(f)));
    expect(realFile, `${twin} has no module beside it to replace`).toBeDefined();
    const [twinExports, realExports] = await Promise.all([import(/* @vite-ignore */ at(twin)), import(/* @vite-ignore */ at(realFile!))]);
    const missing = Object.keys(twinExports).filter((name) => !(name in realExports) && name !== "SAMPLE_DATA_UNAVAILABLE");
    expect(missing, `${twin} exports names ${realFile} does not`).toEqual([]);
  }, 30_000);
});
