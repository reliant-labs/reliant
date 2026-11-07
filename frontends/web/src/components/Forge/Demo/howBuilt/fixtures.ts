// Copyright (c) 2025 Reliant Labs

/**
 * The recorded forge run behind "How this app was built": its types, a strict
 * parser for each fixture, and the two fixtures themselves.
 *
 * Both JSON files under data/ are written by
 * frontends/web/scripts/demo/capture-forge-scaffold.sh from a real run of the
 * forge version reliant pins; nothing in them is typed by hand. Re-run the
 * script on a forge bump (README.md beside it). The parsers refuse a file
 * whose `schema` or `producer` is not the script's, so a hand-made fixture
 * fails here (and in __tests__/fixtures.test.ts) rather than on screen.
 *
 * Kept out of the app's main chunk: only the demo console (a lazy route) and
 * the standalone HowThisAppWasBuilt import this module.
 */

import castRaw from "./data/forge-scaffold-cast.json?raw";
import manifestRaw from "./data/forge-scaffold-manifest.json?raw";

export const CAST_SCHEMA = "forge-demo.scaffold-cast/v1";
export const MANIFEST_SCHEMA = "forge-demo.scaffold-manifest/v1";
/** The script that writes both fixtures, as each records it. */
export const CAPTURE_PRODUCER = "frontends/web/scripts/demo/capture-forge-scaffold.sh";

/** `[seconds since the step started, after shortening long waits; output text]`. */
export type CastEvent = [number, string];

export interface CommandStep {
  kind: "command";
  /** Where it ran: `~` or `~/shop`. */
  cwd: string;
  command: string;
  exit_code: number;
  /** How long it really took. */
  real_seconds: number;
  /** How long it plays: each wait over the cast's idle_time_limit is shortened to it. */
  duration: number;
  events: CastEvent[];
}

/** The proto you write: `text` replaces forge's `replaces` line in `path`. */
export interface EditStep {
  kind: "edit";
  cwd: string;
  path: string;
  replaces: string;
  text: string;
  lines: number;
}

export type CastStep = CommandStep | EditStep;

export interface ScaffoldCast {
  schema: typeof CAST_SCHEMA;
  producer: string;
  forge_version: string;
  captured_at: string;
  /** asciicast v2's header fields; toAsciicast() writes a standard .cast from the steps. */
  asciicast: { version: 2; width: number; height: number; idle_time_limit: number; title: string };
  steps: CastStep[];
}

export type CategoryId =
  | "contract"
  | "migrations"
  | "handlers"
  | "store"
  | "wiring"
  | "mocks"
  | "tests"
  | "frontend"
  | "observability"
  | "deploy"
  | "ci"
  | "project";

/** "regen": forge rewrites it on every generate. "yours": forge wrote it once; it is the project's now. */
export type Ownership = "regen" | "yours";

/** `[path, lines, lines you wrote, ownership]`. */
export type ManifestEntry = [string, number, number, Ownership];

export interface ManifestCategory {
  id: CategoryId;
  files: number;
  lines: number;
  you_lines: number;
  /** Of `lines - you_lines`, the ones in files forge regenerates. */
  regen_lines: number;
  entries: ManifestEntry[];
}

export interface ChainExcerpt {
  id: "proto" | "migration" | "orm" | "handler" | "hook" | "test";
  path: string;
  lang: string;
  start_line: number;
  end_line: number;
  file_lines: number;
  truncated: boolean;
  /** 1-based lines of `code` that are word for word what you wrote. */
  yours: number[];
  code: string;
}

export interface ScaffoldManifest {
  schema: typeof MANIFEST_SCHEMA;
  producer: string;
  forge_version: string;
  captured_at: string;
  project: { name: string; command: string; services: string[]; frontend: string; entities: string[]; custom_rpcs: string[] };
  totals: {
    files: number;
    lines: number;
    you_wrote_lines: number;
    you_wrote_files: number;
    forge_lines: number;
    /** protoc / buf output (gen/, frontends/<f>/src/gen/). */
    machine_lines: number;
    regen_lines: number;
  };
  by_step: Record<string, { files?: number; added?: number; changed?: number; removed?: number }>;
  you_wrote: { path: string; lines: number }[];
  categories: ManifestCategory[];
  /** Left out of every count: lockfiles, forge's own state (.forge/), binaries. */
  excluded: { reason: string; files: number; lines: number }[];
  chain: ChainExcerpt[];
}

class FixtureError extends Error {}

function check(ok: unknown, what: string): asserts ok {
  if (!ok) throw new FixtureError(`forge scaffold fixture: ${what}`);
}

function checkProvenance(value: Record<string, unknown>, schema: string) {
  check(value.schema === schema, `schema is ${String(value.schema)}, want ${schema}`);
  check(value.producer === CAPTURE_PRODUCER, `producer is ${String(value.producer)}, want ${CAPTURE_PRODUCER}`);
  check(typeof value.forge_version === "string" && /^v\d+\.\d+\.\d+/.test(value.forge_version), "forge_version is not a forge version");
  check(typeof value.captured_at === "string" && !Number.isNaN(Date.parse(value.captured_at)), "captured_at is not a time");
}

/** Parses and checks a cast written by the capture script. Throws on anything else. */
export function parseCast(raw: string): ScaffoldCast {
  const value = JSON.parse(raw) as ScaffoldCast;
  checkProvenance(value as unknown as Record<string, unknown>, CAST_SCHEMA);
  check(value.asciicast?.version === 2, "asciicast.version is not 2");
  check(Array.isArray(value.steps) && value.steps.length > 0, "no steps");
  for (const step of value.steps) {
    if (step.kind === "command") {
      check(typeof step.command === "string" && step.command.length > 0, "a command step has no command");
      check(Array.isArray(step.events), `${step.command}: no events`);
      let last = 0;
      for (const [at, text] of step.events) {
        check(typeof at === "number" && at >= last && at <= step.duration, `${step.command}: event times out of order`);
        check(typeof text === "string", `${step.command}: an event has no text`);
        last = at;
      }
    } else {
      check(step.kind === "edit" && typeof step.path === "string" && typeof step.text === "string", "a step is neither a command nor an edit");
    }
  }
  return value;
}

/** Parses and checks a manifest written by the capture script, totals included. Throws on anything else. */
export function parseManifest(raw: string): ScaffoldManifest {
  const value = JSON.parse(raw) as ScaffoldManifest;
  checkProvenance(value as unknown as Record<string, unknown>, MANIFEST_SCHEMA);
  const { totals, categories } = value;
  check(Array.isArray(categories) && categories.length > 0, "no categories");
  let files = 0;
  let lines = 0;
  let you = 0;
  for (const cat of categories) {
    check(cat.entries.length === cat.files, `${cat.id}: ${cat.entries.length} entries, says ${cat.files} files`);
    check(cat.entries.reduce((n, e) => n + e[1], 0) === cat.lines, `${cat.id}: entry lines do not add up to ${cat.lines}`);
    files += cat.files;
    lines += cat.lines;
    you += cat.you_lines;
  }
  check(files === totals.files, `categories hold ${files} files, totals say ${totals.files}`);
  check(lines === totals.lines, `categories hold ${lines} lines, totals say ${totals.lines}`);
  check(you === totals.you_wrote_lines, `categories hold ${you} of your lines, totals say ${totals.you_wrote_lines}`);
  check(totals.forge_lines === totals.lines - totals.you_wrote_lines, "forge_lines is not lines minus yours");
  check(Array.isArray(value.chain) && value.chain.length >= 4 && value.chain.length <= 6, "the chain is not 4 to 6 excerpts");
  return value;
}

/** A standard asciicast v2 file (header line, then `[time, "o", data]` lines) for any asciicast player. */
export function toAsciicast(cast: ScaffoldCast): string {
  const lines: string[] = [JSON.stringify({ ...cast.asciicast, timestamp: Math.floor(Date.parse(cast.captured_at) / 1000) })];
  let offset = 0;
  const out = (at: number, text: string) => lines.push(JSON.stringify([Math.round((offset + at) * 1000) / 1000, "o", text.replace(/\n/g, "\r\n")]));
  for (const step of cast.steps) {
    if (step.kind === "command") {
      out(0, `${promptOf(step.cwd)}${step.command}\n`);
      for (const [at, text] of step.events) out(at, text);
      offset += step.duration;
    } else {
      out(0, `# you write, in ${step.path}:\n${step.text}`);
      offset += 1;
    }
  }
  return `${lines.join("\n")}\n`;
}

/** The prompt a step's command is shown after. */
export function promptOf(cwd: string): string {
  return `${cwd} $ `;
}

/** The recorded run (lazy: parsed on first use). */
let cached: { cast: ScaffoldCast; manifest: ScaffoldManifest } | null = null;
export function scaffoldFixtures(): { cast: ScaffoldCast; manifest: ScaffoldManifest } {
  cached ??= { cast: parseCast(castRaw), manifest: parseManifest(manifestRaw) };
  return cached;
}
