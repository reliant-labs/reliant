// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The publish contract: what a consumer resolves, and what it must never be
// able to resolve.
//
// This package used to publish its TypeScript SOURCES — `types` pointed at
// src/index.ts — which made every consumer typecheck this package's internals
// against its own React. That is not a version-pinning bug to chase; it is
// structural. A React 18 consumer (Expo SDK 52 pins 18.3) compiling sources
// written against @types/react 19 got errors in files it never rendered:
//
//   providers.tsx: 'RuntimeErrorBoundary' cannot be used as a JSX component
//   resource.tsx:  Type '0n' is not assignable to type 'ReactNode'
//
// Now it publishes dist/. Every consumer gets .d.ts, which their tsconfig's
// skipLibCheck does not re-typecheck, and whose React references are resolved
// in THEIR program against THEIR React. This test pins the three properties
// that keeps true.
//
// `pretest` builds dist/ before vitest runs, so these assertions always read
// a current build rather than whatever happened to be on disk.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface Manifest {
  files: string[];
  main: string;
  types: string;
  exports: Record<string, { types: string; default: string } | string>;
  peerDependencies: Record<string, string>;
  peerDependenciesMeta?: Record<string, { optional?: boolean }>;
}

const pkg = JSON.parse(
  readFileSync(join(pkgDir, "package.json"), "utf8"),
) as Manifest;

const distFiles = readdirSync(join(pkgDir, "dist"));
const declarations = distFiles.filter((f) => f.endsWith(".d.ts"));

describe("the published surface", () => {
  it("resolves to dist/, and offers no route back into src/", () => {
    // Nothing a resolver can follow may name src/. `exports` fences modern
    // resolvers off the rest of the tree by itself; `main`/`types` are what
    // the node10-era resolvers read instead.
    const entryPoints = [
      pkg.main,
      pkg.types,
      ...Object.values(pkg.exports).flatMap((e) =>
        typeof e === "string" ? [e] : [e.types, e.default],
      ),
    ];
    for (const entry of entryPoints) {
      expect(
        entry.startsWith("./src/"),
        `${entry} still points into src/`,
      ).toBe(false);
    }

    // And src/ is not in the tarball at all, so a tool that GLOBS the package
    // directory rather than resolving through it — Tailwind's `@source`
    // directive scans exactly this package — cannot reach the sources either.
    // NOTICE is listed because Apache-2.0 §4 obliges every redistributor to
    // pass it on; npm packs LICENSE by itself, so it needs no entry.
    expect(pkg.files).not.toContain("src");
    expect(pkg.files).toEqual(["dist", "interceptors", "README.md", "NOTICE"]);
  });

  it("publishes the engine subpaths OUTSIDE the barrel", () => {
    // The exact entry points a consumer can name. Adding one is a public API
    // decision, so it is spelled out here rather than derived.
    expect(Object.keys(pkg.exports).sort()).toEqual([
      ".",
      "./interceptors",
      "./mock-transport",
      "./node",
      "./package.json",
      "./service-hooks",
      "./telemetry",
      "./vite",
    ]);

    // `./mock-transport`, `./telemetry` and `./service-hooks` are deliberately NOT
    // re-exported from the barrel, and each for its own reason:
    //
    //   - mock-transport is dev-only. It pulls the fixture dispatch engine
    //     in, and a production bundle has to be able to shake it out
    //     entirely. A barrel re-export would anchor it in every import of
    //     this package.
    //   - telemetry imports @hyperdx/browser (lazily). The Next.js and Vite
    //     scaffolds install it; a React-Native frontend declares
    //     @opentelemetry/api alone. Re-exporting from the barrel would leave
    //     it resolving a package it never installed — a build error in a
    //     file it does not use. The barrel carries only error-reporter.ts,
    //     the dependency-free seam the error boundary reports through.
    //   - service-hooks imports @tanstack/react-query. Every frontend forge
    //     scaffolds installs it, but the barrel is also what a consumer
    //     imports for interceptors and errors alone; anchoring React Query
    //     there would make a non-React consumer resolve a package it never
    //     declared.
    //   - node is the Node / Electron-main telemetry entry. It imports
    //     node:async_hooks, node:crypto and node:os, which a browser bundle
    //     cannot resolve.
    //   - vite is a dev-server plugin. It imports node:fs and node:http,
    //     which a browser bundle cannot resolve; it is loaded by
    //     vite.config.ts, in Node, and nowhere else.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const forbidden of [
      "./mock-transport",
      "./node",
      "./telemetry",
      "./service-hooks",
      "./vite",
    ]) {
      expect(
        barrel.includes(forbidden),
        `index.d.ts re-exports ${forbidden}; it must stay reachable only through its own subpath`,
      ).toBe(false);
    }
  });

  it("pins @hyperdx/browser exactly, declares it an optional peer, and keeps @opentelemetry/api required", () => {
    // The `./telemetry` subpath's SDK is a peer, not a dependency: the
    // consuming app owns the install so the bundle carries one copy. OPTIONAL
    // is what lets a frontend that never imports `./telemetry` (React Native)
    // install nothing and still get a clean, warning-free `npm install`.
    //
    // EXACT, not a range: the SDK is 0.x and bundles rrweb and its own OTel
    // web stack, so a minor can change the ingest wire shape (replay chunks,
    // span names) that the collector and HyperDX's saved sources depend on.
    expect(pkg.peerDependencies["@hyperdx/browser"]).toMatch(/^\d+\.\d+\.\d+$/);
    expect(pkg.peerDependenciesMeta?.["@hyperdx/browser"]?.optional).toBe(true);

    // The retired `./otel` SDK wiring must not creep back: HyperDX registers
    // the one global tracer provider, and a second registration silently loses.
    const staleSdk = Object.keys(pkg.peerDependencies).filter(
      (name) =>
        name.startsWith("@opentelemetry/") && name !== "@opentelemetry/api",
    );
    expect(staleSdk).toEqual([]);

    // @opentelemetry/api is required: trace.ts propagates W3C traceparent on
    // every RPC from the BARREL, with no collector and no SDK.
    expect(pkg.peerDependenciesMeta?.["@opentelemetry/api"]?.optional).not.toBe(
      true,
    );
  });

  it("keeps the HyperDX SDK out of the barrel's dependency graph", () => {
    // error-boundary reports through error-reporter.ts, never telemetry.ts.
    // If a barrel module imported the SDK, React Native — which installs no
    // @hyperdx/browser — would fail to resolve it from a file it never uses.
    for (const name of distFiles.filter((f) => f.endsWith(".js"))) {
      if (name === "telemetry.js") continue;
      const body = readFileSync(join(pkgDir, "dist", name), "utf8");
      expect(
        body.includes("@hyperdx/"),
        `dist/${name} imports the HyperDX SDK; only dist/telemetry.js may`,
      ).toBe(false);
    }
  });

  it("makes every peer the ./node entry never imports OPTIONAL, so a Node-only consumer installs none of them", () => {
    // npm installs a non-optional peer into EVERY consumer, whether or not an
    // entry it imports reaches it. An Electron main process that imports only
    // ./node used to get React, Connect and protobuf in its lockfile for
    // nothing. So a peer the ./node import closure does not reach must be
    // optional; the browser entries' consumers declare those packages
    // themselves (every forge frontend scaffold lists them as direct deps).
    const bare = new Set<string>();
    const seen = new Set<string>();
    const walk = (file: string): void => {
      if (seen.has(file)) return;
      seen.add(file);
      const body = readFileSync(join(pkgDir, "dist", file), "utf8");
      const specs = body.matchAll(
        /(?:^|\n)\s*(?:import|export)\b[^"']*?from\s*["']([^"']+)["']|(?:^|\n)\s*import\s*["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)|require\(\s*["']([^"']+)["']\s*\)/g,
      );
      for (const m of specs) {
        const spec = m[1] ?? m[2] ?? m[3] ?? m[4];
        if (spec === undefined) continue;
        if (spec.startsWith(".")) {
          walk(join(dirname(file), spec));
        } else if (!spec.startsWith("node:")) {
          // The package name: "@scope/name" or "name", without a subpath.
          bare.add(
            spec
              .split("/")
              .slice(0, spec.startsWith("@") ? 2 : 1)
              .join("/"),
          );
        }
      }
    };
    walk("node.js");
    expect(
      seen.size,
      "the ./node closure walk found no modules",
    ).toBeGreaterThan(1);

    // What ./node reaches is exactly the one required peer.
    expect([...bare].sort()).toEqual(["@opentelemetry/api"]);
    for (const name of Object.keys(pkg.peerDependencies)) {
      const optional = pkg.peerDependenciesMeta?.[name]?.optional === true;
      expect(
        optional,
        bare.has(name)
          ? `${name} is imported by ./node and must stay a REQUIRED peer`
          : `${name} is never imported by ./node and must be an OPTIONAL peer (peerDependenciesMeta)`,
      ).toBe(!bare.has(name));
    }
  });

  it("declares @tanstack/react-query an optional peer, reachable only via ./service-hooks", () => {
    // Same rule as the OTel SDKs: the ONLY module that imports React Query is
    // the ./service-hooks subpath. A consumer that imports the barrel for
    // interceptors and errors must not be told it is missing a dependency it
    // will never resolve.
    expect(pkg.peerDependencies["@tanstack/react-query"]).toBeDefined();
    expect(
      pkg.peerDependenciesMeta?.["@tanstack/react-query"]?.optional,
      "@tanstack/react-query must be an OPTIONAL peer — only ./service-hooks imports it",
    ).toBe(true);
  });

  it("keeps the base-path helpers IN the barrel", () => {
    // The counterpart to the subpath rule above. basepath.ts has no imports
    // at all, so there is no dependency footprint to fence off — and the
    // scaffolded src/lib/basepath_gen.ts is a one-liner that reaches for it
    // from the barrel. A regression that moved it behind a subpath would
    // break every generated frontend.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "createBasePath",
      "joinBasePath",
      "normalizeBasePath",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
  });

  it("keeps track, setTelemetryUser and disableTelemetry IN the barrel, free of imports", () => {
    // Components call these without importing the SDK, so they must resolve
    // for a frontend that never installed @hyperdx/browser. track.ts imports
    // nothing; the SDK-side sink is installed by the ./telemetry subpath.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of ["track", "setTelemetryUser", "disableTelemetry"]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    const trackJs = readFileSync(join(pkgDir, "dist", "track.js"), "utf8");
    expect(trackJs).not.toMatch(/^\s*import\s/m);
  });

  it("keeps the money helpers IN the barrel, free of imports", () => {
    // The frontend skill tells pages to import these from the barrel rather
    // than hand-roll them. money.ts imports nothing, which is what earns it a
    // place there; an import creeping in would make every barrel consumer
    // resolve it.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "formatMinorUnits",
      "parseMinorUnits",
      "minorUnitsToInput",
      "currencyMinorDigits",
      "formatBasisPoints",
      "parseBasisPoints",
      "basisPointsToInput",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    const money = readFileSync(join(pkgDir, "dist", "money.js"), "utf8");
    expect(money).not.toMatch(/^\s*import\s/m);
  });

  it("declares a React type surface that is identical on React 18 and 19", () => {
    // The peer range is the promise; this is the property that makes it true.
    expect(pkg.peerDependencies["react"]).toBe("^18.3.0 || ^19.0.0");

    // @types/react 19 declares JSX.Element inside the "react" module; 18.3
    // declares it globally and re-exports it from "react/jsx-runtime". So an
    // INFERRED component return type is emitted as
    // `import("react").JSX.Element` when built against 19 and
    // `import("react/jsx-runtime").JSX.Element` when built against 18 — the
    // published .d.ts would silently depend on which major happened to be
    // installed when it was compiled, and the wrong one breaks the consumer.
    //
    // Every exported component therefore declares `: ReactElement`, a name
    // both majors export from "react". Adding a component and letting its
    // return type be inferred is what this assertion catches.
    const offenders: string[] = [];
    for (const name of declarations) {
      const body = readFileSync(join(pkgDir, "dist", name), "utf8");
      for (const [line, text] of body.split("\n").entries()) {
        if (/\bJSX\.Element\b/.test(text)) {
          offenders.push(`dist/${name}:${line + 1}: ${text.trim()}`);
        }
      }
    }
    expect(
      offenders,
      "declare these components' return type as ReactElement instead of letting it infer",
    ).toEqual([]);
  });

  it('marks every hook-bearing module "use client"', () => {
    // The React Server Components boundary is invisible to every gate this
    // package already has. A module that calls a hook but lacks the
    // directive typechecks, lints and `next build`s clean, then compiles
    // into the SERVER graph — where it binds a SECOND @tanstack/react-query
    // instance and every generated hook throws "No QueryClient set" at
    // runtime. service-hooks.ts shipped exactly that way, and the app that
    // hit it paid for a ~200-line reimplementation and a webpack alias
    // before anyone found the missing line.
    //
    // The directive is applied per MODULE, never package-wide: ./interceptors
    // exists to be importable without React, and a blanket banner would drag
    // server-safe code into the client graph. So the invariant has to be
    // "carries it iff it needs it", which is what this checks.
    //
    // Detection is on call SYNTAX after comments are stripped, not on the
    // spelling. basepath.js mentions `useRouter().push()` in prose and holds
    // no hook — matching text alone would fail it and teach the next person
    // to stamp the directive on a module that must not have it.
    const stripComments = (src: string) =>
      src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
    const callsHook = (src: string) =>
      /(?:^|[^.\w])(?:React\.)?use[A-Z][A-Za-z0-9]*\s*\(/m.test(src);

    const missing: string[] = [];
    const gratuitous: string[] = [];
    for (const name of distFiles.filter((f) => f.endsWith(".js"))) {
      const body = readFileSync(join(pkgDir, "dist", name), "utf8");
      const hasDirective = /^\s*(["'])use client\1\s*;?/.test(body);
      if (callsHook(stripComments(body))) {
        if (!hasDirective) missing.push(`dist/${name}`);
      } else if (hasDirective && !/\bReact\b|\bjsx\b/.test(body)) {
        gratuitous.push(`dist/${name}`);
      }
    }
    expect(
      missing,
      'these dist modules call React hooks without a leading "use client" — ' +
        "Next.js will compile them into the server graph and their React " +
        "Query / context reads will fail at runtime",
    ).toEqual([]);
    expect(
      gratuitous,
      "these dist modules carry the directive but touch no React at all; " +
        "the directive pulls them into the client graph for nothing",
    ).toEqual([]);
  });

  it("emits a .d.ts and a .js for every module a consumer can import", () => {
    for (const entry of Object.values(pkg.exports)) {
      if (typeof entry === "string") {
        continue; // "./package.json"
      }
      for (const target of [entry.types, entry.default]) {
        expect(distFiles, `${target} is declared but not emitted`).toContain(
          target.replace("./dist/", ""),
        );
      }
    }
    // Nothing from a test file leaks into the artifact.
    expect(distFiles.filter((f) => f.includes(".test."))).toEqual([]);
  });
});
