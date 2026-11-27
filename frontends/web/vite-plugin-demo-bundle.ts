import fs from "node:fs";
import path from "node:path";
import type { Plugin } from "vite";

/**
 * The forge demo bundle's guard (`vite build --mode demo`, vite.config.ts).
 *
 * demo.reliantlabs.io is the app as a visitor sees it with nobody signed in:
 * the app's one shell (frame, nav, keyboard host), the forge console views,
 * and the app's own Agents pages — chats and their transcripts, the new-chat
 * home, Inbox, Workflows, Connectors, Sandboxes — mounted under the demo's
 * route tree (demoShellRoutes.tsx, demoAgentsRoutes.tsx). Every RPC they make
 * goes to the demo's one transport (lib/connect.demo.ts), answered from the
 * shop sample data (fixtures/shop: the demo pack and its app layer); writes
 * are refused there, and no request leaves the page's origin.
 *
 * What the demo bundle must NEVER contain is sign-in and account code:
 * Supabase, lib/auth, the auth store and provider, the app's own roots and
 * route tree (they mount sign-in), the claim flow, billing (Stripe checkout,
 * identity linking), the Settings page's account sections and its route
 * files, and onboarding's sign-up flow. Not because the demo would call it —
 * its routes never mount it — but because a public page that carries
 * credential handling is one config mistake from using it, and it cannot
 * then be proved inert by reading the bundle. App code is welcome; account
 * code is not. That line is DEMO_FORBIDDEN.
 *
 * So the build itself is the test. At generateBundle every module rollup put
 * in any chunk is checked against DEMO_FORBIDDEN; one match FAILS the build,
 * naming each import that crosses from allowed code into forbidden code, and
 * the full importer chain is one DEMO_BUNDLE_REPORT away. A demo bundle that
 * builds is one with none of them in it.
 *
 * Cutting a crossing, in order of preference: (1) a module that only needs
 * "is someone signed in, and who" reads the session port (`@/lib/session`),
 * which the app backs with the auth store and the demo with a fixed visitor;
 * (2) otherwise a demo twin (below) of the HIGHEST module that keeps the rest
 * of the UI intact, answering what the demo needs: nobody signed in, no
 * token, no funding prompt, no billing, writes impossible.
 *
 * DEMO_BUNDLE_REPORT=<file> also writes every chunk's modules and sizes as
 * JSON (outside the published tree), for size comparisons and for finding
 * who imported what.
 */

/** A module the demo bundle must not contain, and why. Paths are relative to frontends/web, `/`-separated. */
export interface DemoForbiddenRule {
  /** An exact file (it has an extension), or else a path prefix (`src/components/Chat/`, `src/lib/daemon-`). */
  match: string;
  why: string;
  /**
   * Files under `match` that are plain UI, preferences or constants, and reach
   * no sign-in or account code. An entry ending in `/` excepts a directory.
   */
  except?: string[];
}

export const DEMO_FORBIDDEN: readonly DemoForbiddenRule[] = [
  // Sign-in and sessions. Never weakened: the session port (lib/session) is
  // how shared code asks who is signed in without importing any of these.
  { match: "node_modules/@supabase/", why: "Supabase sign-in" },
  {
    match: "src/lib/auth/",
    why: "Reliant auth",
    // An import-free constant (the API-key principal's id) the org store compares against.
    except: ["src/lib/auth/apiKeyUser.ts"],
  },
  { match: "src/lib/supabase.ts", why: "Supabase sign-in" },
  { match: "src/components/Auth/", why: "Reliant sign-in screens" },
  { match: "src/components/AuthGuard.tsx", why: "Reliant sign-in" },
  { match: "src/components/AuthInitializer.tsx", why: "Reliant authenticated startup" },
  { match: "src/components/AuthLayout.tsx", why: "Reliant sign-in" },
  { match: "src/components/AuthScreen.tsx", why: "Reliant sign-in" },
  { match: "src/store/authStore.ts", why: "Reliant auth" },
  { match: "src/api/authProvider.ts", why: "Reliant auth" },
  // The app's roots and route tree (both app hosts: Reliant and the forge
  // brand): they mount sign-in, the auth guard and every account page. The
  // demo has its own tree (demoShellRoutes.tsx) and mounts the app's pages
  // one by one (demoAgentsRoutes.tsx, workflowsAreaRoutes.tsx).
  { match: "src/routes.tsx", why: "the app's route tree" },
  { match: "src/components/App.tsx", why: "Reliant's app root" },
  { match: "src/components/Root.tsx", why: "Reliant's app root" },
  { match: "src/components/Claim/", why: "the forge shell's claim flow" },
  // Account pages: billing (Stripe checkout, identity linking), and the
  // Settings page's account sections and its route files.
  { match: "src/components/Billing/", why: "billing" },
  { match: "src/settingsSectionRoutes.tsx", why: "Reliant settings" },
  { match: "src/settingsConnectorRoutes.tsx", why: "Reliant settings" },
  {
    match: "src/components/Settings/",
    why: "Reliant settings",
    // UI preferences the chat page reads, and the sections the Agents pages
    // (Connectors, Sandboxes) and the project panel render. Whatever they
    // reach in auth or billing is cut by a twin, never by widening this list.
    except: [
      // Chat display preferences and the model picker's local models.
      "src/components/Settings/ToolCallSettings.tsx",
      "src/components/Settings/SpawnDisplaySettings.ts",
      "src/components/Settings/localModels.ts",
      // The provider usage chip's display pieces (no account reads).
      "src/components/Settings/providers/PlanBadge.tsx",
      "src/components/Settings/providers/UsageBars.tsx",
      "src/components/Settings/providers/useNow.ts",
      "src/components/Settings/providers/accountFormat.ts",
      "src/components/Settings/SettingsPageHeader.tsx",
      // Connectors: integrations, and the MCP tab with its pieces.
      "src/components/Settings/IntegrationsSettings.tsx",
      "src/components/Settings/MCPSettings.tsx",
      "src/components/Settings/MCPTabs.tsx",
      "src/components/Settings/MCPInstalledTab.tsx",
      "src/components/Settings/MCPDiscoverTab.tsx",
      "src/components/Settings/MCPServerListItem.tsx",
      "src/components/Settings/MCPCustomServerModal.tsx",
      "src/components/Settings/MCPSetupWizard.tsx",
      "src/components/Settings/mcpAvatar.tsx",
      "src/components/Settings/ConfigScopeSelector.tsx",
      "src/components/Settings/SettingsProjectScope.tsx",
      // The project panel's details card.
      "src/components/Settings/projects/CurrentProjectDetails.tsx",
      "src/components/Settings/projects/projectSource.ts",
      // Sandboxes: the machine list and its machine cards. Its billing
      // (costs, growing a disk, compute checkout) stays forbidden.
      "src/components/Settings/cloud/machines.tsx",
      "src/components/Settings/cloud/ui/",
      "src/components/Settings/cloud/machineLifecycle.ts",
      "src/components/Settings/cloud/machineAccess.tsx",
      "src/components/Settings/cloud/MachineDiskCard.tsx",
      "src/components/Settings/cloud/MachineResourcesCard.tsx",
      "src/components/Settings/cloud/RenameMachine.tsx",
    ],
  },
  // Onboarding's sign-up flow (the journey ENGINE and the forge journey are
  // the demo's; these read the signed-in account).
  {
    match: "src/components/OnboardingFlow/",
    why: "Reliant onboarding",
    // The chat routes' setup policy: pure decisions over the machine list
    // and the current user, and the hook that applies them (ModernApp).
    except: [
      "src/components/OnboardingFlow/onboardingGatePolicy.ts",
      "src/components/OnboardingFlow/usableDaemon.ts",
      "src/components/OnboardingFlow/useChatSetup.ts",
    ],
  },
  { match: "src/components/Onboarding/OnboardingWizard.tsx", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/OnboardingChecklist.tsx", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/journeys/catalog.ts", why: "Reliant's journeys and end-of-tour hooks" },
];

/** The rule `moduleId` breaks, if any. `root` is frontends/web. Virtual and non-file ids never match. */
export function demoForbiddenRule(moduleId: string, root: string, rules: readonly DemoForbiddenRule[] = DEMO_FORBIDDEN): DemoForbiddenRule | undefined {
  const id = moduleId.replace(/^\0/, "").split("?")[0];
  // A demo twin is the demo's own code, wherever it sits.
  if (!path.isAbsolute(id) || /\.demo\.[cm]?[jt]sx?$/.test(id)) return undefined;
  const rel = path.relative(root, id).split(path.sep).join("/");
  // pnpm keeps packages under node_modules/.pnpm/<pkg>@<v>/node_modules/<pkg>/:
  // match on the LAST node_modules segment, which names the package.
  const nm = rel.lastIndexOf("node_modules/");
  const key = nm >= 0 ? rel.slice(nm) : rel;
  const excepted = (r: DemoForbiddenRule) => r.except?.some((e) => (e.endsWith("/") ? key.startsWith(e) : key === e)) ?? false;
  return rules.find((r) => (/\.[a-z]+$/.test(r.match) ? key === r.match : key.startsWith(r.match) && !excepted(r)));
}

/**
 * DEMO TWINS. A module `x.ts` with a sibling `x.demo.ts` is replaced by it in
 * the demo build, for every importer. A twin is the module as the demo needs
 * it (no backend, nobody signed in): lib/connect.demo.ts sends every RPC to the
 * demo's transport, services/controlPlane/config.demo.ts turns the cloud reads
 * on, Forge/Console/consoleHost.demo.ts has no Reliant projects, … A twin
 * exports a subset of its module's names, each typed `typeof Real.<name>`
 * against a type-only import of the module (erased at build, so the twin never
 * imports what it replaces); rollup fails the build when a demo importer needs
 * a name the twin lacks.
 */
export function twinOf(file: string): string | undefined {
  const ext = path.extname(file);
  if (!/^\.[cm]?[jt]sx?$/.test(ext) || file.endsWith(`.demo${ext}`) || file.includes(`${path.sep}node_modules${path.sep}`)) return undefined;
  for (const twinExt of [ext, ".ts", ".tsx"]) {
    const twin = `${file.slice(0, -ext.length)}.demo${twinExt}`;
    if (fs.existsSync(twin)) return twin;
  }
  return undefined;
}

/** The demo build's substitutions: every module with a demo twin is replaced by it. */
export function demoSubstitutes(): Plugin {
  return {
    name: "reliant:demo-substitutes",
    enforce: "pre",
    async resolveId(source, importer, options) {
      if (!importer) return null;
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true });
      if (!resolved || resolved.external) return resolved;
      const file = resolved.id.split("?")[0];
      const twin = twinOf(file);
      return twin ?? resolved;
    },
  };
}

export interface DemoBundleReport {
  chunks: { fileName: string; bytes: number; modules: string[] }[];
  assets: { fileName: string; bytes: number }[];
  /** Every module's importers (static and dynamic), to trace how one arrived. */
  importers: Record<string, string[]>;
}

export function demoBundleGuard(): Plugin {
  let root = "";
  return {
    name: "reliant:demo-bundle-guard",
    apply: "build",
    configResolved(config) {
      root = config.root;
    },
    generateBundle(_options, bundle) {
      const rel = (id: string) => path.relative(root, id.replace(/^\0/, "")).split(path.sep).join("/");
      const report: DemoBundleReport = { chunks: [], assets: [], importers: {} };
      for (const id of this.getModuleIds()) {
        const info = this.getModuleInfo(id);
        if (info) report.importers[rel(id)] = [...info.importers, ...info.dynamicImporters].map(rel);
      }
      const banned = new Map<string, DemoForbiddenRule>();
      for (const item of Object.values(bundle)) {
        if (item.type === "asset") {
          report.assets.push({ fileName: item.fileName, bytes: typeof item.source === "string" ? Buffer.byteLength(item.source) : item.source.byteLength });
          continue;
        }
        report.chunks.push({ fileName: item.fileName, bytes: Buffer.byteLength(item.code), modules: item.moduleIds.map(rel) });
        for (const id of item.moduleIds) {
          const rule = demoForbiddenRule(id, root);
          if (rule) banned.set(id, rule);
        }
      }
      const out = process.env.DEMO_BUNDLE_REPORT;
      if (out) fs.writeFileSync(path.resolve(out), JSON.stringify(report, null, 2));
      if (banned.size === 0) return;

      // Where the demo's graph crosses into forbidden code: each import
      // from an allowed module to a banned one, found by a walk from the
      // entries. Cutting those imports is the fix; everything behind them
      // goes with them.
      const crossings = new Map<string, Set<string>>();
      const seen = new Set<string>();
      const queue = [...this.getModuleIds()].filter((id) => this.getModuleInfo(id)?.isEntry);
      while (queue.length > 0) {
        const id = queue.shift()!;
        if (seen.has(id)) continue;
        seen.add(id);
        const info = this.getModuleInfo(id);
        if (!info) continue;
        for (const next of [...info.importedIds, ...info.dynamicallyImportedIds]) {
          if (banned.has(next) && !banned.has(id)) {
            const key = `${rel(id)} → ${rel(next)} (${banned.get(next)!.why})`;
            crossings.set(key, crossings.get(key) ?? new Set());
          }
          if (!banned.has(next)) queue.push(next);
        }
      }
      this.error(
        `the forge demo bundle contains ${banned.size} sign-in or account modules (vite-plugin-demo-bundle.ts DEMO_FORBIDDEN). ` +
          `They arrive through these imports; cut each from the demo's graph (read the session port, @/lib/session, ` +
          `if the importer only needs who is signed in; else give the importer, or a module above it, a .demo twin):\n` +
          `${[...crossings.keys()].map((c) => `  ${c}`).join("\n")}\n` +
          `Every banned module: ${[...banned.keys()].map(rel).join(", ")}`,
      );
    },
  };
}
