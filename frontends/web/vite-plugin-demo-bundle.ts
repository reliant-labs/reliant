import fs from "node:fs";
import path from "node:path";
import type { Plugin } from "vite";

/**
 * The forge demo bundle's guard (`vite build --mode demo`, vite.config.ts).
 *
 * demo.reliantlabs.io serves ONLY the forge demo: the console views, the mock
 * transport and its fixtures, the journey engine and "How this app was built".
 * Reliant's chat, onboarding, sign-in, Supabase, daemon and settings code must
 * not be in it: not because the demo would call it (the demo shell's routes
 * never mount it), but because one stray import pulls a whole Reliant subtree
 * into a public bundle that is meant to be small and to reach no backend.
 *
 * So the build itself is the test. At generateBundle every module rollup put
 * in any chunk is checked against RELIANT_ONLY; one match FAILS the build,
 * naming the module and the chunk, and the importer chain is one
 * `vite build --mode demo` + DEMO_BUNDLE_REPORT away. A demo bundle that
 * builds is one with none of them in it.
 *
 * DEMO_BUNDLE_REPORT=<file> also writes every chunk's modules and sizes as
 * JSON (outside the published tree), for size comparisons and for finding
 * who imported what.
 */

/** A module the demo bundle must not contain, and why. Paths are relative to frontends/web, `/`-separated. */
export interface ReliantOnlyRule {
  /** An exact file (it has an extension), or else a path prefix (`src/components/Chat/`, `src/lib/daemon-`). */
  match: string;
  why: string;
  /** Files under `match` that are plain UI or constants, used by the forge pages too. */
  except?: string[];
}

export const RELIANT_ONLY: readonly ReliantOnlyRule[] = [
  // Sign-in and sessions.
  { match: "node_modules/@supabase/", why: "Supabase sign-in" },
  { match: "src/lib/auth/", why: "Reliant auth" },
  { match: "src/lib/supabase.ts", why: "Supabase sign-in" },
  { match: "src/components/Auth/", why: "Reliant sign-in screens" },
  { match: "src/components/AuthGuard.tsx", why: "Reliant sign-in" },
  { match: "src/components/AuthInitializer.tsx", why: "Reliant authenticated startup" },
  { match: "src/components/AuthLayout.tsx", why: "Reliant sign-in" },
  { match: "src/components/AuthScreen.tsx", why: "Reliant sign-in" },
  // The Reliant app and the other shells' route trees.
  { match: "src/routes.tsx", why: "Reliant's route tree" },
  { match: "src/forgeShellRoutes.tsx", why: "the forge shell's route tree" },
  { match: "src/settingsSectionRoutes.tsx", why: "Reliant settings" },
  { match: "src/settingsConnectorRoutes.tsx", why: "Reliant settings" },
  { match: "src/workflowsAreaRoutes.tsx", why: "Reliant workflows" },
  { match: "src/components/App.tsx", why: "Reliant's app root" },
  { match: "src/components/Root.tsx", why: "Reliant's app root" },
  // Chat, workflows, the daemon and what runs on it.
  { match: "src/components/Chat/", why: "Reliant chat", except: ["src/components/Chat/LightweightCodeViewer.tsx"] },
  { match: "src/components/workflow/", why: "Reliant workflows" },
  { match: "src/components/workflows/", why: "Reliant workflows" },
  { match: "src/components/Automations/", why: "Reliant automations" },
  { match: "src/components/runs/", why: "Reliant runs" },
  { match: "src/components/inbox/", why: "Reliant inbox" },
  { match: "src/components/Machines/", why: "Reliant machines (daemon)" },
  { match: "src/components/MachineDisk/", why: "Reliant machines (daemon)" },
  { match: "src/components/Terminal/", why: "Reliant terminal (daemon)" },
  { match: "src/components/Worktrees/", why: "Reliant worktrees (daemon)" },
  { match: "src/components/FileBrowser/", why: "Reliant files (daemon)" },
  { match: "src/components/Git/", why: "Reliant git (daemon)" },
  { match: "src/components/SourceControl/", why: "Reliant source control (daemon)" },
  { match: "src/components/DaemonWaitState.tsx", why: "Reliant daemon" },
  { match: "src/lib/daemon-wait.ts", why: "Reliant daemon" },
  { match: "src/lib/daemon-retry.ts", why: "Reliant daemon" },
  { match: "src/lib/daemon-resume.ts", why: "Reliant daemon" },
  { match: "src/lib/chat", why: "Reliant chat" },
  { match: "src/store/chat", why: "Reliant chat" },
  { match: "src/store/authStore.ts", why: "Reliant auth" },
  { match: "src/api/authProvider.ts", why: "Reliant auth" },
  // Reliant's onboarding (the journey ENGINE and the forge journey are the demo's; these are not).
  { match: "src/components/OnboardingFlow/", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/OnboardingWizard.tsx", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/OnboardingChecklist.tsx", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/ContextualTip", why: "Reliant onboarding" },
  { match: "src/components/Onboarding/journeys/workflows", why: "Reliant's workflows journey" },
  { match: "src/components/Onboarding/journeys/fixMyPr", why: "Reliant's workflows journey" },
  // Settings, billing, orgs, projects: account pages.
  { match: "src/components/Settings/", why: "Reliant settings", except: ["src/components/Settings/privacyLinks.ts", "src/components/Settings/providers/accountFormat.ts"] },
  { match: "src/components/Billing/", why: "billing" },
  { match: "src/components/Projects/", why: "Reliant projects", except: ["src/components/Projects/cloneTargets.ts"] },
  { match: "src/components/Claim/", why: "the forge shell's claim flow" },
];

/** The rule `moduleId` breaks, if any. `root` is frontends/web. Virtual and non-file ids never match. */
export function reliantOnlyRule(moduleId: string, root: string, rules: readonly ReliantOnlyRule[] = RELIANT_ONLY): ReliantOnlyRule | undefined {
  const id = moduleId.replace(/^\0/, "").split("?")[0];
  // A demo twin is the demo's own code, wherever it sits.
  if (!path.isAbsolute(id) || /\.demo\.[cm]?[jt]sx?$/.test(id)) return undefined;
  const rel = path.relative(root, id).split(path.sep).join("/");
  // pnpm keeps packages under node_modules/.pnpm/<pkg>@<v>/node_modules/<pkg>/:
  // match on the LAST node_modules segment, which names the package.
  const nm = rel.lastIndexOf("node_modules/");
  const key = nm >= 0 ? rel.slice(nm) : rel;
  return rules.find((r) => (/\.[a-z]+$/.test(r.match) ? key === r.match : key.startsWith(r.match) && !r.except?.includes(key)));
}

/**
 * TEMPORARY substitutions (src/demoBundle/interim/README.md): for one importer
 * only, a Reliant-only import is answered by an interim stub until its owner
 * cuts it. Keyed by importer so the same module reached from anywhere else
 * still fails the guard. Paths relative to frontends/web.
 */
export const INTERIM_SUBSTITUTES: readonly { importer: string; module: string; substitute: string }[] = [
  {
    importer: "src/components/Onboarding/journeys/registry.ts",
    module: "src/components/Onboarding/journeys/workflows.tsx",
    substitute: "src/demoBundle/interim/workflowsJourney.ts",
  },
  {
    importer: "src/components/Onboarding/useTourNavigation.ts",
    module: "src/store/chatStore.ts",
    substitute: "src/demoBundle/interim/chatStore.ts",
  },
  {
    importer: "src/components/Onboarding/useTourNavigation.ts",
    module: "src/components/OnboardingFlow/useOnboardingComplete.ts",
    substitute: "src/demoBundle/interim/useOnboardingComplete.ts",
  },
];

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

/** The demo build's substitutions: demo twins, then the interim stubs. */
export function demoSubstitutes(): Plugin {
  let root = "";
  return {
    name: "reliant:demo-substitutes",
    enforce: "pre",
    configResolved(config) {
      root = config.root;
    },
    async resolveId(source, importer, options) {
      if (!importer) return null;
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true });
      if (!resolved || resolved.external) return resolved;
      const file = resolved.id.split("?")[0];
      const rel = (id: string) => path.relative(root, id).split(path.sep).join("/");
      const interim = INTERIM_SUBSTITUTES.find((s) => s.importer === rel(importer.split("?")[0]) && s.module === rel(file));
      if (interim) return path.resolve(root, interim.substitute);
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
      const banned = new Map<string, ReliantOnlyRule>();
      for (const item of Object.values(bundle)) {
        if (item.type === "asset") {
          report.assets.push({ fileName: item.fileName, bytes: typeof item.source === "string" ? Buffer.byteLength(item.source) : item.source.byteLength });
          continue;
        }
        report.chunks.push({ fileName: item.fileName, bytes: Buffer.byteLength(item.code), modules: item.moduleIds.map(rel) });
        for (const id of item.moduleIds) {
          const rule = reliantOnlyRule(id, root);
          if (rule) banned.set(id, rule);
        }
      }
      const out = process.env.DEMO_BUNDLE_REPORT;
      if (out) fs.writeFileSync(path.resolve(out), JSON.stringify(report, null, 2));
      if (banned.size === 0) return;

      // Where the demo's graph crosses into Reliant-only code: each import
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
        `the forge demo bundle contains ${banned.size} Reliant-only modules (vite-plugin-demo-bundle.ts RELIANT_ONLY). ` +
          `They arrive through these imports; cut each from the demo's graph:\n${[...crossings.keys()].map((c) => `  ${c}`).join("\n")}\n` +
          `Every banned module: ${[...banned.keys()].map(rel).join(", ")}`,
      );
    },
  };
}
