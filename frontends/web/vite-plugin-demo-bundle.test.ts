import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";

import { DEMO_FORBIDDEN, demoForbiddenRule, twinOf } from "./vite-plugin-demo-bundle";

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

describe("demoForbiddenRule", () => {
  it("names sign-in and account modules, pnpm's package layout included", () => {
    expect(demoForbiddenRule(at("node_modules/.pnpm/@supabase+auth-js@2.90.1/node_modules/@supabase/auth-js/dist/module/GoTrueClient.js"), root)?.why).toBe("Supabase sign-in");
    expect(demoForbiddenRule(at("src/store/authStore.ts"), root)?.why).toBe("Reliant auth");
    expect(demoForbiddenRule(at("src/lib/supabase.ts"), root)?.why).toBe("Supabase sign-in");
    expect(demoForbiddenRule(at("src/routes.tsx"), root)?.why).toBe("the app's route tree");
    expect(demoForbiddenRule(at("src/components/Billing/LinkIdentityModal.tsx"), root)?.why).toBe("billing");
    expect(demoForbiddenRule(at("src/settingsSectionRoutes.tsx"), root)?.why).toBe("Reliant settings");
    expect(demoForbiddenRule(at("src/components/Settings/AISettings.tsx"), root)?.why).toBe("Reliant settings");
    expect(demoForbiddenRule(at("src/components/OnboardingFlow/OnboardingPage.tsx"), root)?.why).toBe("Reliant onboarding");
    expect(demoForbiddenRule(at("src/components/Onboarding/journeys/catalog.ts"), root)).toBeDefined();
  });

  it("passes the app's own pages: chat, inbox, workflows, projects, machines", () => {
    for (const file of [
      "src/store/chatStore.ts",
      "src/lib/chatMachine.ts",
      "src/components/Chat/ChatInput.tsx",
      "src/components/inbox/InboxPage.tsx",
      "src/components/workflows/list/WorkflowsListPage.tsx",
      "src/workflowsAreaRoutes.tsx",
      "src/components/Projects/ProjectPanel.tsx",
      "src/components/Machines/PairMachinePanel.tsx",
      "src/components/Terminal/TerminalPanel.tsx",
    ]) {
      expect(demoForbiddenRule(at(file), root), file).toBeUndefined();
    }
  });

  it("passes the forge console, the demo's own code and the listed exceptions", () => {
    expect(demoForbiddenRule(at("src/components/Forge/Console/ConsoleLayout.tsx"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/components/Onboarding/JourneyTour.tsx"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/lib/auth/accountScopedState.demo.ts"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/components/Settings/providers/accountFormat.ts"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/components/Settings/ToolCallSettings.tsx"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/components/OnboardingFlow/onboardingGatePolicy.ts"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/lib/auth/apiKeyUser.ts"), root)).toBeUndefined();
    // A directory exception covers its files, and nothing beside it.
    expect(demoForbiddenRule(at("src/components/Settings/cloud/ui/card.tsx"), root)).toBeUndefined();
    expect(demoForbiddenRule(at("src/components/Settings/cloud/billing.tsx"), root)?.why).toBe("Reliant settings");
    expect(demoForbiddenRule("\0vite/preload-helper.js", root)).toBeUndefined();
  });

  it("lists only exceptions that exist, each under its own rule", () => {
    for (const rule of DEMO_FORBIDDEN) {
      for (const file of rule.except ?? []) {
        expect(file.startsWith(rule.match), `${file} is not under ${rule.match}`).toBe(true);
        expect(fs.existsSync(at(file)), `${file} (an exception) does not exist`).toBe(true);
      }
    }
  });

  it("passes the session port and forbids the app's auth-store session behind it", () => {
    for (const file of ["port.ts", "fixed.ts", "index.ts"]) {
      expect(demoForbiddenRule(at(`src/lib/session/${file}`), root)).toBeUndefined();
    }
    expect(demoForbiddenRule(at("src/lib/auth/authStoreSession.tsx"), root)?.why).toBe("Reliant auth");
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
    expect(demoForbiddenRule(real, root)?.why).toBe("Reliant auth");
    expect(twinOf(real)).toBe(twin);
    expect(demoForbiddenRule(twin, root)).toBeUndefined();

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
