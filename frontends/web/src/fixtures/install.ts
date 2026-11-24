// Copyright (c) 2025 Reliant Labs

/**
 * FIXTURE MODE's install: the real production build, serving a fixture pack
 * in the page. lib/fixturesBoot.ts imports this lazily, and only when the
 * runtime config names a scenario (lib/fixturesConfig.ts), so a live page
 * never loads it. The visual harness and the hosted preview both boot
 * through here.
 *
 * In order, before the app renders:
 *
 *   1. the pack: shopAppPack({ now }), which pins the pack clock first. The
 *      harness pins it by setting `window.__forgeFixtures = { now }` before
 *      the page loads; otherwise it is the load time and runs on;
 *   2. its overlays, by name, allow-listed against shop/overlays.ts: from
 *      `window.__forgeFixtures.overlays` and the URL's `?fixtures=`;
 *   3. outside.ts: the fake session and the fetch boundary (an RPC that
 *      reaches fetch, or any request outside the allowed set, throws);
 *   4. lib/connect.ts installFixtureTransport: one fixture transport per
 *      route, each behind that route's REAL interceptor chain, read-only
 *      unless the config says FIXTURES_READ_ONLY = "false".
 *
 * READ-ONLY means the fixture transport's `readOnly: true`: only an rpc
 * whose proto declares NO_SIDE_EFFECTS answers, plus the app pack's named
 * `answers` (shop/app.ts APP_LAYER_ANSWERS) and the demo pack's
 * (shop/index.ts DEMO_PACK_ANSWERS). Every other rpc is refused, with no name
 * heuristic: an unannotated read is fixed by annotating its proto.
 *
 * `window.__forgeFixtures` is then the PROBE the harness reads: `started` and
 * `inflight` (an outermost interceptor counts every call; a server stream
 * counts until its response header, so an open stream does not hold the page
 * busy but a call that never answers does), and what went wrong: `refused`
 * (a write the read-only guard refused), `unknown` (a read no layer
 * answers), `escaped` (an RPC that reached fetch), `outside` (a request the
 * boundary refused).
 */

import { Code, ConnectError, type Interceptor, type StreamResponse } from "@connectrpc/connect";

import { installFixtureTransport } from "@/lib/connect";
import type { RuntimeFixtures } from "@/lib/fixturesConfig";

import { installOutside } from "./outside";
import { shopAppPack } from "./shop/app";
import { SHOP_READ_ONLY_MESSAGE } from "./shop/index";
import { SHOP_OVERLAYS, type ShopOverlay } from "./shop/overlays";
import { demoNow } from "./shop/shopStory";
import { APP_USER } from "./shop/workspace";
import { createFixtureTransport, defineFixturePack, type FixturePack } from "./transport";

/** What a harness may set on `window.__forgeFixtures` before the page loads. */
export interface ForgeFixturesInput {
  /** The pack clock's instant, epoch ms (the visual harness's FIXED_NOW). */
  now?: number;
  /** Overlay names, applied before `?fixtures=`'s. */
  overlays?: string[];
}

/** `window.__forgeFixtures` once fixture mode is installed. */
export interface ForgeFixturesProbe {
  /** True once every step above has run. */
  ready: boolean;
  scenario: string;
  readOnly: boolean;
  /** The overlays applied, in order. */
  overlays: string[];
  /** Calls started, and calls not yet answered (a stream: until its header). */
  started: number;
  inflight: number;
  /** Calls per "pkg.Service/Method". */
  rpcs: Record<string, number>;
  refused: string[];
  unknown: string[];
  escaped: string[];
  outside: string[];
}

declare global {
  interface Window {
    __forgeFixtures?: ForgeFixturesInput & Partial<ForgeFixturesProbe>;
  }
}

/** The scenarios this build serves (FIXTURES_SCENARIO). */
export const FIXTURE_SCENARIOS = ["shop"] as const;

/** The registered overlays among `names`, in order and once each; the rest are reported and ignored. */
export function resolveOverlays(
  names: readonly string[],
  registry: Readonly<Record<string, ShopOverlay>> = SHOP_OVERLAYS,
  warn: (message: string) => void = console.warn,
): string[] {
  const out: string[] = [];
  for (const name of names) {
    if (!Object.prototype.hasOwnProperty.call(registry, name)) {
      warn(`[fixtures] no overlay named "${name}" (known: ${Object.keys(registry).join(", ") || "none"}); ignored`);
      continue;
    }
    if (!out.includes(name)) out.push(name);
  }
  return out;
}

const keyOf = (req: { method: { name: string; parent: { typeName: string } } }) => `${req.method.parent.typeName}/${req.method.name}`;

/** Outermost: counts every call for settle(). A stream counts until its response header. */
export function countingInterceptor(probe: Pick<ForgeFixturesProbe, "started" | "inflight" | "rpcs">): Interceptor {
  return (next) => async (req) => {
    const key = keyOf(req);
    probe.started += 1;
    probe.inflight += 1;
    probe.rpcs[key] = (probe.rpcs[key] ?? 0) + 1;
    try {
      return await next(req);
    } finally {
      probe.inflight -= 1;
    }
  };
}

/**
 * Innermost, next to the pack: records the pack's own refusals before any of
 * the app's interceptors rewrites them. A server stream's error arrives while
 * its body is read, so the body is watched too.
 */
export function recordingInterceptor(probe: Pick<ForgeFixturesProbe, "refused" | "unknown">, readOnlyMessage: string): Interceptor {
  const record = (key: string, err: unknown) => {
    if (!(err instanceof ConnectError)) return;
    if (err.code === Code.PermissionDenied && err.rawMessage === readOnlyMessage) probe.refused.push(key);
    else if (err.code === Code.Unimplemented && err.rawMessage.startsWith("fixture-transport: no route")) probe.unknown.push(key);
  };
  return (next) => async (req) => {
    const key = keyOf(req);
    let res;
    try {
      res = await next(req);
    } catch (err) {
      record(key, err);
      throw err;
    }
    if (!res.stream) return res;
    const body = res.message;
    async function* watched() {
      try {
        yield* body;
      } catch (err) {
        record(key, err);
        throw err;
      }
    }
    return { ...res, message: watched() } as StreamResponse;
  };
}

/** Install fixture mode for `config` (lib/fixturesBoot.ts calls this). */
export async function installFixtures(config: RuntimeFixtures): Promise<void> {
  if (!(FIXTURE_SCENARIOS as readonly string[]).includes(config.scenario)) {
    throw new Error(`fixture mode: no scenario "${config.scenario}" in this build (it serves: ${FIXTURE_SCENARIOS.join(", ")})`);
  }
  const input = window.__forgeFixtures ?? {};
  const now = typeof input.now === "number" && Number.isFinite(input.now) ? input.now : undefined;
  const overlays = resolveOverlays([...(Array.isArray(input.overlays) ? input.overlays : []), ...config.overlays]);

  const probe: ForgeFixturesProbe = {
    ready: false, scenario: config.scenario, readOnly: config.readOnly, overlays,
    started: 0, inflight: 0, rpcs: {}, refused: [], unknown: [], escaped: [], outside: [],
  };
  window.__forgeFixtures = Object.assign(input, probe);
  const live = window.__forgeFixtures as ForgeFixturesProbe;

  // The pack first: it pins the clock, which the session below is issued on.
  const app = shopAppPack({ now });
  const pack: FixturePack = overlays.length === 0
    ? app
    : defineFixturePack({ name: `${app.name}+${overlays.join("+")}`, extends: [app, ...overlays.map((name) => SHOP_OVERLAYS[name]())] });

  installOutside({
    user: APP_USER,
    now: demoNow(),
    onBlocked: (kind, request) => (kind === "rpc" ? live.escaped : live.outside).push(request),
  });

  const counting = countingInterceptor(live);
  const recording = recordingInterceptor(live, SHOP_READ_ONLY_MESSAGE);
  installFixtureTransport((_route, interceptors) =>
    createFixtureTransport({
      pack,
      now: demoNow,
      readOnly: config.readOnly,
      readOnlyMessage: SHOP_READ_ONLY_MESSAGE,
      interceptors: [counting, ...interceptors, recording],
    }),
  );
  live.ready = true;
}
