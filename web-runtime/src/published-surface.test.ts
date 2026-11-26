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
import { existsSync, readFileSync, readdirSync } from "node:fs";
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

// importClosure walks dist/ from entry through every relative import and
// returns the modules it reached and the packages (bare specifiers, "node:"
// builtins excluded, subpaths folded into the package name) they import.
function importClosure(entry: string): {
  bare: Set<string>;
  seen: Set<string>;
} {
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
  walk(entry);
  return { bare, seen };
}

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
      "./auth/auth0",
      "./auth/clerk",
      "./auth/firebase",
      "./auth/supabase",
      "./changes-server",
      "./devlog",
      "./fixture-transport",
      "./interceptors",
      "./mock-transport",
      "./node",
      "./otel-proxy",
      "./package.json",
      "./service-hooks",
      "./session",
      "./telemetry",
      "./transport-scope",
      "./vite",
    ]);

    // `./mock-transport`, `./telemetry` and `./service-hooks` are deliberately NOT
    // re-exported from the barrel, and each for its own reason:
    //
    //   - mock-transport is dev-only. It pulls the fixture dispatch engine
    //     in, and a production bundle has to be able to shake it out
    //     entirely. A barrel re-export would anchor it in every import of
    //     this package.
    //   - fixture-transport is the same for demos and previews: an app
    //     lazy-loads it into a demo chunk, and its main bundle must not
    //     carry it.
    //   - telemetry imports @hyperdx/otel-web and rrweb (lazily, each its
    //     own chunk). Re-exported from the barrel, every frontend, React
    //     Native included, would bundle the SDK for nothing. The barrel
    //     carries only error-reporter.ts, the dependency-free seam the error
    //     boundary reports through.
    //   - service-hooks imports @tanstack/react-query. Every frontend forge
    //     scaffolds installs it, but the barrel is also what a consumer
    //     imports for interceptors and errors alone; anchoring React Query
    //     there would make a non-React consumer resolve a package it never
    //     declared.
    //   - transport-scope imports @tanstack/react-query too (it mounts a
    //     scope's QueryClient), for the same reason.
    //   - node is the Node / Electron-main telemetry entry. It imports
    //     node:async_hooks, node:crypto and node:os, which a browser bundle
    //     cannot resolve.
    //   - vite is a dev-server plugin. It imports node:fs and node:http,
    //     which a browser bundle cannot resolve; it is loaded by
    //     vite.config.ts, in Node, and nowhere else.
    //   - auth/<provider> are the auth-provider adapters. Each is one
    //     provider's callbacks mapped onto identifyUser / trackAuthEvent; an
    //     app imports only its own provider's, and none belongs in every
    //     bundle.
    //   - otel-proxy is the dev servers' /_otel proxy (Vite mounts it
    //     through ./vite, Next.js's next.config.ts starts its relay). It
    //     imports node:http, node:https and node:fs.
    //   - changes-server fetches a Changes snapshot with a control plane
    //     token. It imports nothing a browser lacks, but it belongs on a
    //     server only, and a browser bundle must never carry the code that
    //     holds that token.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const forbidden of [
      "./auth/",
      "./changes-server",
      "./fixture-transport",
      "./mock-transport",
      "./node",
      "./otel-proxy",
      "./telemetry",
      "./service-hooks",
      "./transport-scope",
      "./vite",
    ]) {
      expect(
        barrel.includes(forbidden),
        `index.d.ts re-exports ${forbidden}; it must stay reachable only through its own subpath`,
      ).toBe(false);
    }
  });

  it("declares the SDK parts as exact OPTIONAL peers, carries no dependency, and keeps @opentelemetry/api a required peer", () => {
    // @hyperdx/otel-web and rrweb are OPTIONAL PEERS, not dependencies.
    // Until 0.14.0 they were dependencies, and npm installs every dependency
    // into every consumer whether or not an entry it imports reaches it: an
    // Electron main process that imports only ./node shipped about 90 MB of
    // browser packages (forge defect ae15c269). The app that imports
    // ./telemetry declares them itself: every forge web scaffold does, and
    // `forge generate` adds them to a frontend whose sources import it.
    // EXACT, not a range: both are 0.x/2.x internals the runtime drives
    // below their public surface (tracer names, the replay wire shape
    // HyperDX's player reads). devDependencies at the same version are what
    // this package builds and tests against.
    const manifest = pkg as unknown as {
      dependencies?: Record<string, string>;
      devDependencies: Record<string, string>;
    };
    expect(
      manifest.dependencies ?? {},
      "the runtime installs nothing of its own into a consumer: what an entry imports is a peer",
    ).toEqual({});
    for (const name of ["@hyperdx/otel-web", "rrweb"]) {
      expect(pkg.peerDependencies[name], `${name} must be a peer`).toMatch(
        /^\d+\.\d+\.\d+$/,
      );
      expect(
        pkg.peerDependenciesMeta?.[name]?.optional,
        `${name} must be an OPTIONAL peer: only ./telemetry imports it`,
      ).toBe(true);
      expect(manifest.devDependencies[name]).toBe(pkg.peerDependencies[name]);
    }
    // The runtime no longer imports @hyperdx/browser at all.
    expect(pkg.peerDependencies["@hyperdx/browser"]).toBeUndefined();

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

  it("reaches the SDK and rrweb only from ./telemetry, and only through dynamic import()", () => {
    // Every module that names @hyperdx/ or rrweb, and how. The barrel and
    // every other entry must reach neither; ./telemetry reaches each through
    // import() alone, which is what makes each its own chunk, loaded when
    // telemetry (and, for rrweb, replay) actually runs.
    // sdk-client.ts names the SDK through package.json's import map
    // (#hyperdx-otel-web; see the next test), so that counts too.
    const sdkImport =
      /(?:from\s*|import\s*\(\s*|import\s*)["'](@hyperdx\/[^"']+|#hyperdx-otel-web|rrweb)["']/g;
    const dynamicOnly =
      /import\(\s*["'](@hyperdx\/[^"']+|#hyperdx-otel-web|rrweb)["']\s*\)/g;
    const importers = new Map<string, string[]>();
    for (const name of distFiles.filter((f) => f.endsWith(".js"))) {
      const body = readFileSync(join(pkgDir, "dist", name), "utf8");
      const all = [...body.matchAll(sdkImport)].map((m) => m[1]!);
      if (all.length === 0) continue;
      const dynamic = [...body.matchAll(dynamicOnly)].map((m) => m[1]!);
      expect(
        dynamic,
        `dist/${name} imports ${all.join(", ")} statically; only import() keeps it a lazy chunk`,
      ).toEqual(all);
      importers.set(name, all);
    }
    expect(Object.fromEntries(importers)).toEqual({
      "sdk-client.js": ["#hyperdx-otel-web"],
      "replay-recorder.js": ["rrweb"],
    });
    // And only ./telemetry's closure reaches those two modules.
    for (const name of distFiles.filter((f) => f.endsWith(".js"))) {
      if (["telemetry.js", "sdk-client.js"].includes(name)) continue;
      const body = readFileSync(join(pkgDir, "dist", name), "utf8");
      for (const lazy of ["./sdk-client.js", "./replay-recorder.js"]) {
        expect(
          body.includes(lazy),
          `dist/${name} imports ${lazy}; only ./telemetry may`,
        ).toBe(false);
      }
    }
  });

  it("maps #hyperdx-otel-web to the SDK for bundlers, and to its Node build under node", () => {
    // A browser bundler matches `browser`, listed first so it wins even where
    // `node` is also set, and must get the SDK's own ESM build, a lazy chunk
    // of its own (sdk-conditions.test.ts resolves it with esbuild). Under Node (a test runner that
    // loads this package natively) it gets dist/hyperdx-otel-web.node.js,
    // which scripts/build.mjs builds, because Node cannot load the SDK's
    // ESM build (see "//imports note" in package.json).
    const imports = (pkg as unknown as { imports?: Record<string, unknown> })
      .imports;
    expect(imports).toEqual({
      "#hyperdx-otel-web": {
        browser: "@hyperdx/otel-web",
        node: {
          types: "./dist/hyperdx-otel-web.node.d.ts",
          default: "./dist/hyperdx-otel-web.node.js",
        },
        default: "@hyperdx/otel-web",
      },
    });
    expect(distFiles).toContain("hyperdx-otel-web.node.js");
    expect(distFiles).toContain("hyperdx-otel-web.node.d.ts");
    // The Node build shares the app's @opentelemetry/api, and reaches
    // nothing else outside itself.
    const body = readFileSync(
      join(pkgDir, "dist", "hyperdx-otel-web.node.js"),
      "utf8",
    );
    const outside = new Set(
      [...body.matchAll(/(?:from|import)\s*["']([^"'.][^"']*)["']/g)].map(
        (m) => m[1],
      ),
    );
    expect([...outside].sort()).toEqual(["@opentelemetry/api"]);
  });

  it("makes every peer the ./node entry never imports OPTIONAL, so a Node-only consumer installs none of them", () => {
    // npm installs a non-optional peer into EVERY consumer, whether or not an
    // entry it imports reaches it. An Electron main process that imports only
    // ./node used to get React, Connect and protobuf in its lockfile for
    // nothing. So a peer the ./node import closure does not reach must be
    // optional; the browser entries' consumers declare those packages
    // themselves (every forge frontend scaffold lists them as direct deps).
    const { bare, seen } = importClosure("node.js");
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

  it("declares @tanstack/react-query an optional peer, reachable only via ./service-hooks and ./transport-scope", () => {
    // Same rule as the OTel SDKs: the ONLY modules that import React Query
    // are the ./service-hooks and ./transport-scope subpaths. A consumer that
    // imports the barrel for interceptors and errors must not be told it is
    // missing a dependency it will never resolve.
    expect(pkg.peerDependencies["@tanstack/react-query"]).toBeDefined();
    expect(
      pkg.peerDependenciesMeta?.["@tanstack/react-query"]?.optional,
      "@tanstack/react-query must be an OPTIONAL peer — only ./service-hooks and ./transport-scope import it",
    ).toBe(true);
    const importers = distFiles
      .filter((f) => f.endsWith(".js"))
      .filter((f) =>
        /from\s*["']@tanstack\/react-query["']/.test(
          readFileSync(join(pkgDir, "dist", f), "utf8"),
        ),
      )
      .sort();
    expect(importers).toEqual(["service-hooks.js", "transport-scope.js"]);
  });

  it("publishes ./transport-scope with its API, importing only its peers", () => {
    // A generated hook file (forge generate) imports scopedClient from here;
    // an app mounts TransportScopeProvider. Dropping a name breaks them.
    const dts = readFileSync(
      join(pkgDir, "dist", "transport-scope.d.ts"),
      "utf8",
    );
    for (const symbol of [
      "createTransportScope",
      "TransportScopeProvider",
      "useTransportScope",
      "useTransport",
      "useClient",
      "clientFor",
      "transportOf",
      "scopedClient",
    ]) {
      expect(
        new RegExp(`export declare function ${symbol}\\b`).test(dts),
        `./transport-scope must export ${symbol}`,
      ).toBe(true);
    }
    expect(dts).toMatch(/export interface TransportScope\b/);
    const { bare } = importClosure("transport-scope.js");
    expect([...bare].sort()).toEqual([
      "@connectrpc/connect",
      "@tanstack/react-query",
      "react",
    ]);
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

  it("publishes installDevLogging from ./devlog, which imports no package", () => {
    // An adopted site that only wants browser dev logging installs the runtime
    // and nothing else. The barrel imports @connectrpc/connect and
    // @bufbuild/protobuf (optional peers such a site never declares), so
    // installDevLogging from the barrel failed to resolve there (obs
    // 34137b6e). ./devlog is the same module with no package in its closure.
    const devlog = readFileSync(join(pkgDir, "dist", "devlog.d.ts"), "utf8");
    for (const symbol of ["installDevLogging", "uninstallDevLogging"]) {
      expect(
        new RegExp(`export declare function ${symbol}\\b`).test(devlog),
        `./devlog must export ${symbol}`,
      ).toBe(true);
    }
    const { bare, seen } = importClosure("devlog.js");
    expect(
      seen.size,
      "the ./devlog closure walk found no modules",
    ).toBeGreaterThan(1);
    expect([...bare].sort(), "./devlog must import no package at all").toEqual(
      [],
    );
    // The barrel still re-exports it, so every existing import keeps working.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    expect(barrel.includes("installDevLogging")).toBe(true);
  });

  it("publishes the session API from ./session, which imports only react", async () => {
    // A bundle that must carry no auth code (a public demo, a preview) still
    // renders a shell that asks who is signed in. ./session gives it the
    // session port and fixedSession without the barrel, whose closure holds
    // the interceptors, telemetry seams and every optional peer.
    const session = readFileSync(join(pkgDir, "dist", "session.d.ts"), "utf8");
    for (const symbol of [
      "decodeJwt",
      "fixedSession",
      "SessionProvider",
      "useSession",
      "useSignedIn",
    ]) {
      expect(
        new RegExp(`export declare function ${symbol}\\b`).test(session),
        `./session must export ${symbol}`,
      ).toBe(true);
    }
    for (const type of [
      "FixedSessionOptions",
      "JwtClaims",
      "Session",
      "SessionAuth",
      "SessionUser",
    ]) {
      expect(
        new RegExp(`export interface ${type}\\b`).test(session),
        `./session must export the ${type} type`,
      ).toBe(true);
    }
    const { bare } = importClosure("session.js");
    expect([...bare].sort(), "./session must import react alone").toEqual([
      "react",
    ]);

    // Resolved by package name, through the exports map, as a consumer would.
    const viaSubpath = (await import(
      "@reliantlabs/forge-web-runtime/session"
    )) as Record<string, unknown>;
    expect(viaSubpath.fixedSession).toBeTypeOf("function");

    // The barrel re-exports the same module, not a copy: a SessionProvider
    // mounted from one entry point is the context useSession() reads from the
    // other, and the barrel's existing names keep working.
    const barrel = readFileSync(join(pkgDir, "dist", "index.js"), "utf8");
    const reexport =
      /export\s*\{([^}]*)\}\s*from\s*["']\.\/session\.js["']/.exec(barrel);
    expect(reexport, "index.js must re-export ./session.js").not.toBeNull();
    for (const symbol of [
      "decodeJwt",
      "fixedSession",
      "SessionProvider",
      "useSession",
      "useSignedIn",
    ]) {
      expect(reexport?.[1], `barrel must re-export ${symbol}`).toMatch(
        new RegExp(`\\b${symbol}\\b`),
      );
    }
  });

  it("publishes the dev-server plugins from ./vite, and keeps it off the browser half", () => {
    // vite.config.ts imports these by name: forgeDevLog replaces the
    // src/lib/devlog-plugin.ts forge used to scaffold, so dropping it breaks
    // every config that moved to it. vite.js runs in Node, inside Vite: it
    // must not pull in devlog.js, the browser half, which keeps module state.
    const vite = readFileSync(join(pkgDir, "dist", "vite.d.ts"), "utf8");
    for (const symbol of [
      "forgeRuntimeConfig",
      "forgeOtelProxy",
      "forgeDevLog",
    ]) {
      expect(
        new RegExp(`export declare function ${symbol}\\b`).test(vite),
        `./vite must export ${symbol}`,
      ).toBe(true);
    }
    const js = readFileSync(join(pkgDir, "dist", "vite.js"), "utf8");
    expect(js.includes("./devlog.js"), "vite.js imports the browser half").toBe(
      false,
    );
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

  it("keeps trackAuthEvent and identifyUser IN the barrel, importing only track.js", () => {
    // Same rule as track(): an app's auth context calls identifyUser whether
    // or not @hyperdx/browser is installed, so auth-events.ts may reach
    // nothing but the dependency-free track.ts.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "trackAuthEvent",
      "identifyUser",
      "AUTH_SEEN_STORAGE_KEY",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    const authJs = readFileSync(join(pkgDir, "dist", "auth-events.js"), "utf8");
    const imports = [
      ...authJs.matchAll(/^\s*import[^"']*["']([^"']+)["']/gm),
    ].map((m) => m[1]);
    expect(imports).toEqual(["./track.js"]);
  });

  it("keeps normalizeRoute, the analytics tiers and exposures IN the barrel, and off the SDK", () => {
    // An app names its own events' routes the way the automatic page_viewed
    // events do, sets its consent tier and records flag exposures, all from
    // the barrel. page-views.ts, page-engagement.ts, session-identity.ts and
    // exposure.ts sit in the barrel's import closure, so none may reach the
    // SDK: they import only the import-free track.ts and basepath.ts, and
    // each other.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "normalizeRoute",
      "PAGE_VIEWED_EVENT",
      "PAGE_ENGAGED_EVENT",
      "analyticsTier",
      "grantAnalyticsConsent",
      "revokeAnalyticsConsent",
      "optOutOfAnalytics",
      "trackExposure",
      "forgeExposureHook",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    for (const name of [
      "page-views.js",
      "page-engagement.js",
      "session-identity.js",
      "exposure.js",
    ]) {
      const js = readFileSync(join(pkgDir, "dist", name), "utf8");
      const imports = [
        ...js.matchAll(
          /^\s*(?:import|export)\b[^;]*?from\s*["']([^"']+)["']/gm,
        ),
      ].map((m) => m[1]);
      expect(
        imports.filter(
          (i) =>
            i !== "./track.js" &&
            i !== "./basepath.js" &&
            i !== "./page-engagement.js",
        ),
        `dist/${name} imports more than track.js, basepath.js and page-engagement.js`,
      ).toEqual([]);
    }
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

  it("keeps the Changes evaluator IN the barrel, free of imports", () => {
    // Every frontend imports the barrel, so the evaluator must be pure:
    // hashing and parsing, no SDK, no network, nothing on import.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "evaluateChange",
      "parseChangesSnapshot",
      "changePreviewHash",
      "ChangeResult",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    const changes = readFileSync(join(pkgDir, "dist", "changes.js"), "utf8");
    expect(changes).not.toMatch(/^\s*import\s/m);
  });

  it("keeps the Changes store and OpenFeature provider IN the barrel, off the SDK", () => {
    // loadChanges and forgeChangesProvider sit in the barrel's import
    // closure, so they may reach only the evaluator and exposure.ts (which
    // reaches the import-free track.ts): no SDK, no OpenFeature, nothing
    // fetched on import.
    const barrel = readFileSync(join(pkgDir, "dist", "index.d.ts"), "utf8");
    for (const symbol of [
      "loadChanges",
      "CHANGES_SNAPSHOT_URL",
      "ChangesStore",
      "changeBool",
      "changeString",
      "changeNumber",
      "changeJson",
      "forgeChangesProvider",
    ]) {
      expect(barrel.includes(symbol), `barrel must export ${symbol}`).toBe(
        true,
      );
    }
    for (const name of ["changes-store.js", "changes-provider.js"]) {
      const js = readFileSync(join(pkgDir, "dist", name), "utf8");
      const imports = [
        ...js.matchAll(
          /^\s*(?:import|export)\b[^;]*?from\s*["']([^"']+)["']/gm,
        ),
      ].map((m) => m[1]);
      expect(
        imports.filter(
          (i) =>
            i !== "./changes.js" &&
            i !== "./changes-store.js" &&
            i !== "./exposure.js",
        ),
        `dist/${name} imports more than changes.js, changes-store.js and exposure.js`,
      ).toEqual([]);
    }
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
        // A subpath may live in a dist/ subdirectory (./auth/<provider>).
        expect(
          existsSync(join(pkgDir, target)),
          `${target} is declared but not emitted`,
        ).toBe(true);
      }
    }
    // Nothing from a test file leaks into the artifact.
    expect(distFiles.filter((f) => f.includes(".test."))).toEqual([]);
    expect(
      readdirSync(join(pkgDir, "dist", "auth")).filter((f) =>
        f.includes(".test."),
      ),
    ).toEqual([]);
  });
});
