import path from "path";
import type { Plugin } from "vite";

/**
 * FIXTURE CODE STAYS OUT OF THE APP'S ENTRY CHUNK.
 *
 * Fixture mode (src/lib/fixturesConfig.ts) is a runtime switch in config.js,
 * so the production bundle carries the fixture packs: lib/fixturesBoot.ts
 * reaches src/fixtures/install.ts through a lazy `import()`, and a document
 * without FIXTURES_SCENARIO never loads that chunk. That holds only while no
 * module under src/fixtures/ is in the static graph of an app entry: the
 * entry chunk and every chunk it imports statically, which a page always
 * loads. One stray static import (a component reading the pack's story, say)
 * would ship sample data to every live page and run its module code there.
 *
 * So the build is the test: at generateBundle, each app entry's static chunk
 * graph is walked and any src/fixtures module in it FAILS the build, naming
 * the import that brought it in. The forge demo's own entry (`demo`) is not
 * checked: the demo IS a fixture shell. One line reports the numbers.
 */

/** The output chunk fields the guard reads (rollup's / rolldown's OutputChunk). */
export interface GuardChunk {
  type: "chunk";
  fileName: string;
  name: string;
  isEntry: boolean;
  imports: string[];
  dynamicImports: string[];
  moduleIds: string[];
  code: string;
}

export interface EntryGraph {
  entry: string;
  /** The entry chunk and every chunk it imports statically. */
  chunks: string[];
  modules: number;
  bytes: number;
  /** src/fixtures modules in that graph: must be empty. */
  fixtureModules: string[];
}

export interface FixtureEntryReport {
  entries: EntryGraph[];
  /** Chunks outside every entry's static graph that carry fixture code. */
  lazyFixtureChunks: { fileName: string; bytes: number; fixtureModules: number }[];
}

const FIXTURES_DIR = "src/fixtures/";

/** A module id relative to `root`, `/`-separated; virtual ids as they are. */
function relative(id: string, root: string): string {
  const file = id.replace(/^\0/, "").split("?")[0];
  return path.isAbsolute(file) ? path.relative(root, file).split(path.sep).join("/") : file;
}

export const isFixtureModule = (id: string, root: string): boolean => relative(id, root).startsWith(FIXTURES_DIR);

/** Walk each entry named in `entries` through its static imports. Pure over the bundle. */
export function fixtureEntryReport(bundle: Record<string, GuardChunk | { type: "asset" }>, root: string, entries: readonly string[]): FixtureEntryReport {
  const chunks = new Map<string, GuardChunk>();
  for (const item of Object.values(bundle)) if (item.type === "chunk") chunks.set(item.fileName, item);
  const statics = new Set<string>();
  const report: FixtureEntryReport = { entries: [], lazyFixtureChunks: [] };
  for (const entry of [...chunks.values()].filter((c) => c.isEntry && entries.includes(c.name))) {
    const seen = new Set<string>();
    const queue = [entry.fileName];
    while (queue.length > 0) {
      const name = queue.shift()!;
      if (seen.has(name)) continue;
      seen.add(name);
      queue.push(...(chunks.get(name)?.imports ?? []));
    }
    let modules = 0;
    let bytes = 0;
    const fixtureModules: string[] = [];
    for (const name of seen) {
      statics.add(name);
      const chunk = chunks.get(name);
      if (!chunk) continue;
      modules += chunk.moduleIds.length;
      bytes += Buffer.byteLength(chunk.code);
      for (const id of chunk.moduleIds) if (isFixtureModule(id, root)) fixtureModules.push(relative(id, root));
    }
    report.entries.push({ entry: entry.name, chunks: [...seen], modules, bytes, fixtureModules });
  }
  for (const chunk of chunks.values()) {
    if (statics.has(chunk.fileName)) continue;
    const fixtureModules = chunk.moduleIds.filter((id) => isFixtureModule(id, root)).length;
    if (fixtureModules > 0) report.lazyFixtureChunks.push({ fileName: chunk.fileName, bytes: Buffer.byteLength(chunk.code), fixtureModules });
  }
  return report;
}

const kb = (bytes: number) => `${(bytes / 1024).toFixed(1)} kB`;

/** The guard, for the app build's entries (`index`, `findBar`). */
export function fixtureEntryGuard(entries: readonly string[] = ["index", "findBar"]): Plugin {
  let root = "";
  return {
    name: "reliant:fixture-entry-guard",
    apply: "build",
    configResolved(config) {
      root = config.root;
    },
    generateBundle(_options, bundle) {
      const report = fixtureEntryReport(bundle as unknown as Record<string, GuardChunk>, root, entries);
      if (report.entries.length === 0) return; // not the app build (the demo's entry is `demo`)
      const leaked = report.entries.filter((e) => e.fixtureModules.length > 0);
      if (leaked.length > 0) {
        // The static imports that cross from the entry graph's own code into src/fixtures.
        const inGraph = new Set<string>();
        for (const e of leaked) {
          for (const name of e.chunks) {
            const chunk = bundle[name] as unknown as GuardChunk | undefined;
            chunk?.moduleIds?.forEach((id) => inGraph.add(id));
          }
        }
        const crossings = new Set<string>();
        for (const id of inGraph) {
          if (!isFixtureModule(id, root)) continue;
          for (const importer of this.getModuleInfo(id)?.importers ?? []) {
            if (inGraph.has(importer) && !isFixtureModule(importer, root)) crossings.add(`${relative(importer, root)} → ${relative(id, root)}`);
          }
        }
        this.error(
          `fixture code is in the app's entry chunk graph (vite-plugin-fixture-entry-guard.ts): ` +
            leaked.map((e) => `${e.entry}: ${e.fixtureModules.join(", ")}`).join("; ") +
            `.\nReach src/fixtures only through lib/fixturesBoot.ts's lazy import. The static imports into it:\n` +
            [...crossings].map((c) => `  ${c}`).join("\n"),
        );
      }
      const lazy = report.lazyFixtureChunks.reduce((sum, c) => sum + c.bytes, 0);
      this.info?.(
        `fixture entry guard: ${report.entries.map((e) => `${e.entry} ${e.chunks.length} chunks / ${e.modules} modules / ${kb(e.bytes)}, 0 fixture modules`).join("; ")}; ` +
          `fixture code in ${report.lazyFixtureChunks.length} lazy chunks (${kb(lazy)})`,
      );
    },
  };
}
