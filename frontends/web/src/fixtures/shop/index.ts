// Copyright (c) 2025 Reliant Labs

/**
 * The "shop" fixture pack: the ONE world every sample-data surface shows
 * (shopStory.ts tells it). A pack is what a fixture transport
 * (../transport.ts) needs to answer the real app's RPCs:
 *
 *   shopStory.ts   the story: cast, releases, services, the incident, the clock
 *   model/         the domain model the story computes (traffic, issues,
 *                  sessions, panels, …), as the console's model types
 *   handlers/      the wire: one module per service, answering
 *                  "pkg.Service/Method" with proto messages; defaults.ts
 *                  (forge's generated answers for the rest) and scope.ts
 *                  (reads outside shop are answered empty)
 *
 * The clock is the pack's one `now` (shopStory.ts demoNow): the page load by
 * default, or the instant shopPack({ now }) pins, which must happen before
 * anything reads the pack.
 */

import type { Transport } from "@connectrpc/connect";
import { createMockTransport, type MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { isDemoRead } from "../readOnly";
import { createFixtureTransport, defineFixturePack, type FixturePack } from "../transport";
import { configScenarioHandlers } from "./handlers/config";
import { consoleScenarioHandlers } from "./handlers/console";
import { DEMO_DEFAULT_SERVICES, defaultHandlers, withMessageRequests } from "./handlers/defaults";
import { inventoryScenarioHandlers } from "./handlers/inventory";
import { observabilityHandlers } from "./handlers/observability";
import { scopedToDemo } from "./handlers/scope";
import { storageScenarioHandlers } from "./handlers/storage";
import { createShopApi } from "./model/shopApi";
import { demoNow, pinClock } from "./shopStory";

/** What a refused write says. */
export const SHOP_READ_ONLY_MESSAGE = "This is a demo. Changes are turned off.";

/**
 * The pack's scenario over `fixtures` (a model-level ObservabilityApi; the
 * story's by default).
 */
export function shopScenario(fixtures: ObservabilityApi = createShopApi()): MockScenario {
  // Dispatch, most specific first: the tour's encoded observability reads, the console's story
  // (handlers/console.ts: Releases, Environments, Secrets, Domains, Activity; config, inventory,
  // storage), then forge's generated defaults for every other read of the console's services
  // (handlers/defaults.ts). A read naming another project or environment is answered empty
  // first (handlers/scope.ts), defaults included, which is why the pack composes its own
  // defaults rather than declaring them as the pack's `defaults`.
  const handlers = { ...defaultHandlers(), ...consoleScenarioHandlers(), ...configScenarioHandlers(), ...inventoryScenarioHandlers(), ...storageScenarioHandlers(), ...observabilityHandlers(fixtures) };
  return { name: "shop", handlers: withMessageRequests(scopedToDemo(handlers, DEMO_DEFAULT_SERVICES)) as unknown as MockScenario["handlers"] };
}

export interface ShopPackOptions {
  /** The instant the story is told at (epoch ms). Default: the page load (the clock's first read). */
  now?: number;
}

/**
 * The pack, its clock pinned to `now` when given, which must happen before
 * anything reads it (shopStory.ts pinClock). Serve it with the pack clock:
 * `createFixtureTransport({ pack: shopPack(), now: demoNow, readOnly })`.
 */
export function shopPack({ now }: ShopPackOptions = {}): FixturePack {
  if (now !== undefined) pinClock(now);
  return defineFixturePack({ name: "shop", scenario: () => shopScenario() });
}

export interface ShopTransportOptions extends ShopPackOptions {
  /**
   * Ask `fallback` (a real Connect transport) first, and answer from the pack
   * when it fails or takes longer than `timeoutMs`: the demo's
   * DEMO_DATA_SOURCE=remote gateway.
   */
  passthrough?: {
    fallback: Transport;
    timeoutMs?: number;
    onFallback?: () => void;
  };
}

/** A transport serving the shop pack, read-only. */
export function createShopTransport({ passthrough, ...pack }: ShopTransportOptions = {}): Transport {
  const readOnly = { readOnly: isDemoRead, readOnlyMessage: SHOP_READ_ONLY_MESSAGE };
  const served = shopPack(pack);
  if (!passthrough) return createFixtureTransport({ pack: served, now: demoNow, ...readOnly });
  // A fixture transport never reaches a backend (forge's neither), so the gateway rides forge's
  // mock transport, over the same scenario.
  return createMockTransport({
    scenario: { ...shopScenario(), passthrough: "first" },
    fallback: passthrough.fallback,
    ...readOnly,
    ...(passthrough.timeoutMs !== undefined ? { passthroughTimeoutMs: passthrough.timeoutMs } : {}),
    ...(passthrough.onFallback ? { onPassthroughFallback: passthrough.onFallback } : {}),
  });
}
