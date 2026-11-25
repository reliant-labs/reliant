#!/usr/bin/env node
// The forge demo bundle's size, against scripts/demo/bundle-budget.json.
//
//   task demo:bundle-size                      build the demo, measure it, fail over budget
//   task demo:bundle-size -- --report-only     the same, never failing (a PR body's table)
//
//   node scripts/demo/bundle-size.mjs --report <file> --dist <dir> [options]
//     --report <file>   the DEMO_BUNDLE_REPORT JSON that vite-plugin-demo-bundle.ts wrote
//                       (default: $DEMO_BUNDLE_REPORT)
//     --dist <dir>      that build's output (default: frontends/web/dist-demo)
//     --budget <file>   the ceilings (default: bundle-budget.json beside this script)
//     --report-only     print the table, exit 0 even over budget
//     --before <file>   a --json-out file from another build: adds a "before" row and the change
//     --json-out <file> write this build's sizes as JSON, for a later --before
//
// It prints a markdown table, for pull request bodies, and exits 1 when a
// measured size is over its ceiling (2 on bad input).
//
// What it measures, as dev-docs/demo-inventory.md ("Bundle size") defines it:
// - initial: the entry script plus every `modulepreload` and stylesheet link in
//   the built index.html, i.e. what the first page load fetches before any lazy
//   chunk, counting only the build's own files (assets/; not the host's
//   /config.js). Raw bytes, gzip (level 9) and brotli (zlib's defaults).
// - total: every JS and CSS file the build emitted, raw and gzip.
// The build's own report names the files, so a stale file left in --dist from
// an earlier build is never counted, and a file index.html links that the
// report does not name fails the run (a report from another build).
//
// The demo bundle's GUARD (which modules may be in it) is the build itself,
// vite-plugin-demo-bundle.ts. This is only its size.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import zlib from "node:zlib";

const here = path.dirname(fileURLToPath(import.meta.url));
const KIB = 1024;
/** Where vite writes the build's own files (build.assetsDir's default). */
const ASSETS_DIR = "assets/";

/** The measured sizes the budget names, with the JSON keys bundle-budget.json uses. */
export const METRICS = [
  { key: "initial_gzip", label: "initial gzip" },
  { key: "total_gzip", label: "total gzip" },
];

/** The JS and CSS files index.html loads up front: its entry script, modulepreload and stylesheet links. */
export function initialFiles(html) {
  const files = new Set();
  for (const tag of html.match(/<(?:script|link)\b[^>]*>/g) ?? []) {
    const ref = /\b(?:src|href)="\/?([^"?#]+\.(?:js|css))"/.exec(tag)?.[1];
    if (!ref) continue;
    if (
      tag.startsWith("<script") ||
      /\brel="(?:modulepreload|stylesheet)"/.test(tag)
    )
      files.add(ref);
  }
  return [...files].sort();
}

/**
 * Every JS and CSS file the build's report names: its chunks and its CSS
 * assets. An empty chunk is not one: the report is written at generateBundle,
 * before vite drops a chunk that held only CSS (its CSS is an asset).
 */
export function emittedFiles(report) {
  if (!Array.isArray(report?.chunks) || !Array.isArray(report?.assets)) {
    throw new Error("not a DEMO_BUNDLE_REPORT (no chunks/assets arrays)");
  }
  return [...report.chunks.filter((c) => c.bytes > 0), ...report.assets]
    .map((f) => f.fileName)
    .filter((f) => /\.(?:js|css)$/.test(f))
    .sort();
}

/**
 * Sizes the build. `read(fileName)` returns a file's bytes (a Buffer); the
 * file names are the report's, relative to the build's output directory.
 */
export function measure(report, html, read) {
  const emitted = emittedFiles(report);
  const known = new Set(emitted);
  // Only the build's own output counts: vite writes it under assets/. A link
  // to anything else is not the bundle (/config.js is the runtime config the
  // host serves beside it), but an assets/ file the report does not name
  // means the report is from another build.
  const linked = initialFiles(html).filter((f) => f.startsWith(ASSETS_DIR));
  const unknown = linked.filter((f) => !known.has(f));
  if (unknown.length > 0) {
    throw new Error(
      `index.html loads ${unknown.join(", ")}, which the report does not name: the report and the build differ`,
    );
  }
  const initial = linked;
  const initialSet = new Set(initial);
  const sizes = {
    initial_files: initial.length,
    initial_raw: 0,
    initial_gzip: 0,
    initial_brotli: 0,
    total_files: emitted.length,
    total_raw: 0,
    total_gzip: 0,
  };
  for (const f of emitted) {
    const bytes = read(f);
    const gzip = zlib.gzipSync(bytes, { level: 9 }).length;
    sizes.total_raw += bytes.length;
    sizes.total_gzip += gzip;
    if (initialSet.has(f)) {
      sizes.initial_raw += bytes.length;
      sizes.initial_gzip += gzip;
      sizes.initial_brotli += zlib.brotliCompressSync(bytes).length;
    }
  }
  return sizes;
}

/** Each budgeted size over its ceiling: `{key, label, bytes, max}` (bytes), in METRICS order. */
export function overBudget(sizes, budget) {
  const over = [];
  for (const { key, label } of METRICS) {
    const maxKiB = budget?.[`${key}_kib`];
    if (typeof maxKiB !== "number" || !(maxKiB > 0))
      throw new Error(`the budget has no ${key}_kib ceiling`);
    if (typeof sizes[key] !== "number") throw new Error(`no measured ${key}`);
    const max = maxKiB * KIB;
    if (sizes[key] > max) over.push({ key, label, bytes: sizes[key], max });
  }
  return over;
}

const kib = (bytes) =>
  `${(bytes / KIB).toLocaleString("en-US", { minimumFractionDigits: 1, maximumFractionDigits: 1 })} KiB`;
const delta = (bytes) => `${bytes >= 0 ? "+" : "−"}${kib(Math.abs(bytes))}`;

/** The markdown a pull request body pastes: the sizes, the ceilings, and the verdict. */
export function renderMarkdown(
  sizes,
  budget,
  over,
  before,
  budgetName = "frontends/web/scripts/demo/bundle-budget.json",
) {
  const row = (name, s) =>
    `| ${name} | ${kib(s.initial_raw)} (${s.initial_files} files) | **${kib(s.initial_gzip)}** | ${kib(s.initial_brotli)} | ${kib(s.total_raw)} (${s.total_files} files) | **${kib(s.total_gzip)}** |`;
  const lines = [
    "| Demo bundle | Initial raw | Initial gzip | Initial brotli | Total JS+CSS raw | Total gzip |",
    "|---|---|---|---|---|---|",
  ];
  if (before) {
    lines.push(row("before", before));
    lines.push(row("after", sizes));
    lines.push(
      `| change | ${delta(sizes.initial_raw - before.initial_raw)} | ${delta(sizes.initial_gzip - before.initial_gzip)} | ${delta(sizes.initial_brotli - before.initial_brotli)} | ${delta(sizes.total_raw - before.total_raw)} | ${delta(sizes.total_gzip - before.total_gzip)} |`,
    );
  } else {
    lines.push(row("this build", sizes));
  }
  lines.push(
    `| budget | | ≤ ${kib(budget.initial_gzip_kib * KIB)} (${delta(budget.initial_gzip_kib * KIB - sizes.initial_gzip)} left) | | | ≤ ${kib(budget.total_gzip_kib * KIB)} (${delta(budget.total_gzip_kib * KIB - sizes.total_gzip)} left) |`,
  );
  lines.push("");
  if (over.length === 0) {
    lines.push(`Within the demo bundle budget (\`${budgetName}\`).`);
  } else {
    lines.push(`**Over the demo bundle budget** (\`${budgetName}\`):`);
    for (const o of over)
      lines.push(
        `- ${o.label} ${kib(o.bytes)} > ${kib(o.max)} (${delta(o.bytes - o.max)})`,
      );
  }
  return lines.join("\n");
}

function parseArgs(argv) {
  const opts = {
    report: process.env.DEMO_BUNDLE_REPORT,
    dist: path.resolve(here, "../../dist-demo"),
    budget: path.join(here, "bundle-budget.json"),
    reportOnly: false,
  };
  const value = (i, flag) => {
    if (i + 1 >= argv.length) throw new Error(`${flag} needs a value`);
    return argv[i + 1];
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    switch (a) {
      case "--report":
        opts.report = value(i++, a);
        break;
      case "--dist":
        opts.dist = value(i++, a);
        break;
      case "--budget":
        opts.budget = value(i++, a);
        break;
      case "--before":
        opts.before = value(i++, a);
        break;
      case "--json-out":
        opts.jsonOut = value(i++, a);
        break;
      case "--report-only":
        opts.reportOnly = true;
        break;
      default:
        throw new Error(`unknown argument ${a}`);
    }
  }
  if (!opts.report)
    throw new Error(
      "no report: pass --report <file> or set DEMO_BUNDLE_REPORT",
    );
  return opts;
}

function main() {
  let opts, sizes, budget, over, before;
  try {
    opts = parseArgs(process.argv.slice(2));
    const report = JSON.parse(fs.readFileSync(opts.report, "utf8"));
    const html = fs.readFileSync(path.join(opts.dist, "index.html"), "utf8");
    budget = JSON.parse(fs.readFileSync(opts.budget, "utf8"));
    sizes = measure(report, html, (f) =>
      fs.readFileSync(path.join(opts.dist, f)),
    );
    over = overBudget(sizes, budget);
    if (opts.before) before = JSON.parse(fs.readFileSync(opts.before, "utf8"));
    if (opts.jsonOut)
      fs.writeFileSync(opts.jsonOut, `${JSON.stringify(sizes, null, 2)}\n`);
  } catch (err) {
    console.error(`bundle-size: ${err instanceof Error ? err.message : err}`);
    process.exit(2);
  }
  const defaultBudget =
    path.join(here, "bundle-budget.json") === path.resolve(opts.budget);
  console.log(
    renderMarkdown(
      sizes,
      budget,
      over,
      before,
      defaultBudget ? undefined : opts.budget,
    ),
  );
  if (over.length > 0 && !opts.reportOnly) process.exit(1);
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href
)
  main();
