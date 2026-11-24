// @vitest-environment node
import zlib from "node:zlib";
import { describe, expect, it } from "vitest";
import budgetFile from "./bundle-budget.json";
import {
  emittedFiles,
  initialFiles,
  measure,
  overBudget,
  renderMarkdown,
} from "./bundle-size.mjs";

const KIB = 1024;

// A built index.html as vite writes the demo's: icons, the host's runtime
// config, inline scripts, the entry, modulepreloads, a stylesheet, a font preload.
const HTML = `<!doctype html><html><head>
<link rel="icon" type="image/svg+xml" href="/favicon.svg" />
<script>window.x = 1</script>
<script src="/config.js"></script>
<script type="module" crossorigin src="/assets/demo-A.js"></script>
<link rel="modulepreload" crossorigin href="/assets/vendor-B.js">
<link rel="stylesheet" crossorigin href="/assets/demo-C.css">
<link rel="preload" href="/assets/font-D.woff2" as="font" type="font/woff2" crossorigin />
</head><body></body></html>`;

// What DEMO_BUNDLE_REPORT holds for it, plus a lazy chunk and an empty
// CSS-only chunk vite drops after the report is written.
const REPORT = {
  chunks: [
    {
      fileName: "assets/demo-A.js",
      bytes: 3,
      modules: ["src/demoBundle/main.tsx"],
    },
    {
      fileName: "assets/vendor-B.js",
      bytes: 3,
      modules: ["node_modules/react/index.js"],
    },
    { fileName: "assets/lazy-E.js", bytes: 3, modules: ["src/lazy.tsx"] },
    {
      fileName: "assets/style-F.js",
      bytes: 0,
      modules: ["node_modules/x/style.css"],
    },
  ],
  assets: [
    { fileName: "assets/demo-C.css", bytes: 3 },
    { fileName: "assets/font-D.woff2", bytes: 3 },
    { fileName: "index.html", bytes: 3 },
  ],
};

const FILES = {
  "assets/demo-A.js": Buffer.from("entry ".repeat(200)),
  "assets/vendor-B.js": Buffer.from("vendor ".repeat(300)),
  "assets/lazy-E.js": Buffer.from("lazy ".repeat(400)),
  "assets/demo-C.css": Buffer.from("a{color:red}".repeat(50)),
};
const read = (f) => {
  if (!FILES[f]) throw new Error(`read ${f}`);
  return FILES[f];
};
const gz = (f) => zlib.gzipSync(FILES[f], { level: 9 }).length;

describe("bundle-size", () => {
  it("counts the entry, modulepreloads and stylesheets as initial, not icons, inline scripts or font preloads", () => {
    expect(initialFiles(HTML)).toEqual([
      "assets/demo-A.js",
      "assets/demo-C.css",
      "assets/vendor-B.js",
      "config.js",
    ]);
  });

  it("names the report's JS and CSS files, skipping the empty chunk vite drops", () => {
    expect(emittedFiles(REPORT)).toEqual([
      "assets/demo-A.js",
      "assets/demo-C.css",
      "assets/lazy-E.js",
      "assets/vendor-B.js",
    ]);
    expect(() => emittedFiles({ chunks: [] })).toThrow(
      /not a DEMO_BUNDLE_REPORT/,
    );
  });

  it("measures initial (the build's own files only) and total", () => {
    const sizes = measure(REPORT, HTML, read);
    const initial = [
      "assets/demo-A.js",
      "assets/vendor-B.js",
      "assets/demo-C.css",
    ];
    expect(sizes.initial_files).toBe(3);
    expect(sizes.initial_raw).toBe(
      initial.reduce((n, f) => n + FILES[f].length, 0),
    );
    expect(sizes.initial_gzip).toBe(initial.reduce((n, f) => n + gz(f), 0));
    expect(sizes.initial_brotli).toBeGreaterThan(0);
    expect(sizes.total_files).toBe(4);
    expect(sizes.total_gzip).toBe(
      Object.keys(FILES).reduce((n, f) => n + gz(f), 0),
    );
  });

  it("refuses a report from another build", () => {
    const html = HTML.replace("vendor-B.js", "vendor-Z.js");
    expect(() => measure(REPORT, html, read)).toThrow(
      /assets\/vendor-Z\.js, which the report does not name/,
    );
  });

  it("passes at a ceiling and fails one byte over it, naming each size over", () => {
    const budget = { initial_gzip_kib: 500, total_gzip_kib: 5000 };
    expect(
      overBudget({ initial_gzip: 500 * KIB, total_gzip: 5000 * KIB }, budget),
    ).toEqual([]);
    expect(
      overBudget(
        { initial_gzip: 500 * KIB + 1, total_gzip: 5000 * KIB },
        budget,
      ),
    ).toEqual([
      {
        key: "initial_gzip",
        label: "initial gzip",
        bytes: 500 * KIB + 1,
        max: 500 * KIB,
      },
    ]);
    expect(
      overBudget(
        { initial_gzip: 600 * KIB, total_gzip: 6000 * KIB },
        budget,
      ).map((o) => o.key),
    ).toEqual(["initial_gzip", "total_gzip"]);
  });

  it("refuses a budget without a ceiling", () => {
    expect(() =>
      overBudget({ initial_gzip: 1, total_gzip: 1 }, { initial_gzip_kib: 500 }),
    ).toThrow(/total_gzip_kib/);
  });

  it("the committed budget names both ceilings, above its recorded baseline", () => {
    expect(overBudget({ initial_gzip: 0, total_gzip: 0 }, budgetFile)).toEqual(
      [],
    );
    expect(budgetFile.initial_gzip_kib).toBeGreaterThan(
      budgetFile.baseline.initial_gzip_kib,
    );
    expect(budgetFile.total_gzip_kib).toBeGreaterThan(
      budgetFile.baseline.total_gzip_kib,
    );
  });

  it("renders the table with the verdict, and before/after with the change", () => {
    const budget = { initial_gzip_kib: 10, total_gzip_kib: 100 };
    const sizes = {
      initial_files: 3,
      initial_raw: 30 * KIB,
      initial_gzip: 11 * KIB,
      initial_brotli: 9 * KIB,
      total_files: 9,
      total_raw: 300 * KIB,
      total_gzip: 90 * KIB,
    };
    const over = overBudget(sizes, budget);
    const md = renderMarkdown(sizes, budget, over);
    expect(md).toContain(
      "| this build | 30.0 KiB (3 files) | **11.0 KiB** | 9.0 KiB | 300.0 KiB (9 files) | **90.0 KiB** |",
    );
    expect(md).toContain("≤ 10.0 KiB (−1.0 KiB left)");
    expect(md).toContain("- initial gzip 11.0 KiB > 10.0 KiB (+1.0 KiB)");

    const before = { ...sizes, initial_gzip: 8 * KIB };
    const md2 = renderMarkdown(
      { ...sizes, initial_gzip: 9 * KIB },
      budget,
      [],
      before,
    );
    expect(md2).toContain("| before |");
    expect(md2).toContain("| after |");
    expect(md2).toMatch(/\| change \| \+0\.0 KiB \| \+1\.0 KiB \|/);
    expect(md2).toContain("Within the demo bundle budget");
  });
});
