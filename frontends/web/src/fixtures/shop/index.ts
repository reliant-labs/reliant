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

import type { DescMethod } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";
import { createMockTransport, type MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createFixtureTransport, defineFixturePack, type CreateFixtureTransportOptions, type FixturePack } from "../transport";
import { configScenarioHandlers } from "./handlers/config";
import { consoleScenarioHandlers } from "./handlers/console";
import { coverageSourceHandlers } from "./handlers/coverageSource";
import { DEMO_DEFAULT_SERVICES, defaultHandlers, withMessageRequests } from "./handlers/defaults";
import { inventoryScenarioHandlers } from "./handlers/inventory";
import { observabilityHandlers } from "./handlers/observability";
import { organizationScenarioHandlers } from "./handlers/organization";
import { scopedToDemo } from "./handlers/scope";
import { securityHandlers } from "./handlers/security";
import { withSpeedInsights } from "./handlers/speedInsights";
import { storageScenarioHandlers } from "./handlers/storage";
import { testsHandlers } from "./handlers/tests";
import { createShopApi } from "./model/shopApi";
import { demoNow, pinClock } from "./shopStory";

/** What a refused write says. */
export const SHOP_READ_ONLY_MESSAGE = "This is a demo. Changes are turned off.";

/**
 * How the pack is served: on the pack clock, writes refused. Every transport
 * over the shop pack takes these (createShopTransport, the dev previews'
 * `<FixturePreview serve={SHOP_SERVE}>`).
 */
export const SHOP_SERVE = { now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE } satisfies Omit<CreateFixtureTransportOptions, "pack">;

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
  const handlers = withSpeedInsights({ ...defaultHandlers(), ...consoleScenarioHandlers(), ...configScenarioHandlers(), ...inventoryScenarioHandlers(), ...storageScenarioHandlers(), ...organizationScenarioHandlers(), ...observabilityHandlers(fixtures), ...securityHandlers(), ...testsHandlers(), ...coverageSourceHandlers() });
  return { name: "shop", handlers: withMessageRequests(scopedToDemo(handlers, DEMO_DEFAULT_SERVICES)) as unknown as MockScenario["handlers"] };
}

/** One rpc the demo pack answers although its proto does not declare NO_SIDE_EFFECTS, and why that is safe. */
export interface DemoPackAnswer {
  method: DescMethod;
  /** Why answering it from a fixture is safe: the side effect the server has, and that a fixture has none. */
  reason: string;
}

// ─────────────────────────────────────────────────────────────────────────────
// DEMO_PACK_ANSWERS: the demo pack's read-only exceptions (owner-approved,
// 2026-11-24). Exactly control-plane#3523's seven data-browser reads, whose
// only side effect is server-side: an audit_log row per call, and for
// PreviewObject a minted presigned URL. A fixture writes no row and mints no
// URL, so the Data and Storage pages read the shop pack through them.
//
// To keep the demo pack's `answers` empty instead, delete the one `answers:`
// line in shopPack below: the demo then refuses all seven once
// control-plane#3523 is re-vendored, and its Data and Storage pages show no
// sample data.
//
// Never add a machine-waking read (FileSystemService, the WorktreeService git
// reads) or anything outside these seven: __tests__/demoPackAnswers.test.ts
// fails on either, and on an entry without a reason.
// ─────────────────────────────────────────────────────────────────────────────
export const DEMO_PACK_ANSWERS: readonly DemoPackAnswer[] = [
  { method: StorageBrowserService.method.previewObject, reason: "server writes an audit row and mints a presigned URL per call; a fixture writes none and returns inline sample bytes or a fake same-origin URL" },
  { method: StorageBrowserService.method.listObjects, reason: "server writes an audit row (and an activity event on open) per call; a fixture writes none" },
  { method: StorageBrowserService.method.getObjectMetadata, reason: "server writes an audit row per call; a fixture writes none" },
  { method: AppDatabaseService.method.browseTable, reason: "server writes an audit row per call; a fixture writes none" },
  { method: AppDatabaseService.method.describeDatabase, reason: "server writes an audit row and an activity event per call; a fixture writes none" },
  { method: AppDatabaseService.method.queryDatabase, reason: "server writes an audit row per call; a fixture writes none" },
  { method: AppDatabaseService.method.queryDatabasePanel, reason: "server writes an audit row per call; a fixture writes none" },
];

export interface ShopPackOptions {
  /** The instant the story is told at (epoch ms). Default: the page load (the clock's first read). */
  now?: number;
}

/**
 * The pack, its clock pinned to `now` when given, which must happen before
 * anything reads it (shopStory.ts pinClock). Serve it with the pack clock:
 * `createFixtureTransport({ pack: shopPack(), now: demoNow, readOnly: true })`.
 *
 * Its `answers` are DEMO_PACK_ANSWERS, so only a pure fixture transport may
 * serve it (createShopTransport's passthrough serves shopScenario instead).
 */
export function shopPack({ now }: ShopPackOptions = {}): FixturePack {
  if (now !== undefined) pinClock(now);
  return defineFixturePack({
    name: "shop",
    scenario: () => shopScenario(),
    // DEMO_PACK_ANSWERS, wired. Delete this one line to keep the demo pack's `answers` empty.
    answers: DEMO_PACK_ANSWERS.map((a) => a.method),
  });
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

/**
 * A transport serving the shop pack, read-only: only an rpc whose proto
 * declares NO_SIDE_EFFECTS answers, plus DEMO_PACK_ANSWERS on the pure
 * fixture path.
 */
export function createShopTransport({ passthrough, ...pack }: ShopTransportOptions = {}): Transport {
  if (!passthrough) return createFixtureTransport({ ...SHOP_SERVE, pack: shopPack(pack) });
  const readOnly = { readOnly: SHOP_SERVE.readOnly, readOnlyMessage: SHOP_SERVE.readOnlyMessage };
  // A fixture transport never reaches a backend (forge's neither), so the gateway rides forge's
  // mock transport, over the same scenario, with forge's `readOnly: true` (NO_SIDE_EFFECTS only).
  // That path reaches live traffic, so it serves the scenario WITHOUT DEMO_PACK_ANSWERS: the
  // gateway never sees a read the proto does not declare side-effect free.
  if (pack.now !== undefined) pinClock(pack.now);
  return createMockTransport({
    scenario: { ...shopScenario(), passthrough: "first" },
    fallback: passthrough.fallback,
    ...readOnly,
    ...(passthrough.timeoutMs !== undefined ? { passthroughTimeoutMs: passthrough.timeoutMs } : {}),
    ...(passthrough.onFallback ? { onPassthroughFallback: passthrough.onFallback } : {}),
  });
}
