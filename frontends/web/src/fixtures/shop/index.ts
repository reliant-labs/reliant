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
 *
 * The story is told for one app, `shop` by default (the public demo's). The
 * forge tour's sample tells it for its own reserved name, in the person's org,
 * with every id it answers prefixed (shopPack({ app, org, idPrefix }),
 * src/fixtures/scope.tsx): the same pack, never a copy of it.
 */

import type { DescMethod, DescService } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";
import { createMockTransport, type MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { ChangesService } from "@/gen/controlplane/services/changes/v1/changes_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { VulnerabilityService } from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";
import { CoverageSourceService } from "@/gen/reliant/v1/coverage_source_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createFixtureTransport, defineFixturePack, type CreateFixtureTransportOptions, type FixturePack } from "../transport";
import { changesHandlers } from "./handlers/changes";
import { configScenarioHandlers } from "./handlers/config";
import { consoleScenarioHandlers } from "./handlers/console";
import { coverageSourceHandlers } from "./handlers/coverageSource";
import { DEMO_DEFAULT_SERVICES, defaultHandlers, withMessageRequests } from "./handlers/defaults";
import { inventoryScenarioHandlers } from "./handlers/inventory";
import { observabilityHandlers } from "./handlers/observability";
import { organizationScenarioHandlers } from "./handlers/organization";
import { withIdPrefix } from "./handlers/idPrefix";
import { DEMO_PROJECT, DEMO_SCOPE, scopedToDemo, type PackScope } from "./handlers/scope";
import { securityHandlers } from "./handlers/security";
import { siteAuditScenarioHandlers } from "./handlers/siteAudit";
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
 * Every service the pack answers: the console's (DEMO_DEFAULT_SERVICES) and
 * the ones whose handlers scope themselves. The id prefix reads and writes
 * exactly these (__tests__/scope.test.tsx holds the list to the handlers).
 */
export const SHOP_PACK_SERVICES: readonly DescService[] = [...DEMO_DEFAULT_SERVICES, OrganizationService, UserService, VulnerabilityService, TestsService, CoverageSourceService, ChangesService];

/** Who the story is told for, and how its ids are minted (ShopPackOptions). */
export interface ShopScenarioOptions {
  /** The app and org the pack answers for (handlers/scope.ts). Default: the demo's, shop in its org. */
  scope?: PackScope;
  /** What every id the pack answers starts with (handlers/idPrefix.ts). Default: none. */
  idPrefix?: string;
}

/**
 * The pack's scenario over `fixtures` (a model-level ObservabilityApi; the
 * story's, for the scope's app, by default).
 */
export function shopScenario(fixtures?: ObservabilityApi, { scope = DEMO_SCOPE, idPrefix = "" }: ShopScenarioOptions = {}): MockScenario {
  const story = fixtures ?? createShopApi(undefined, scope.app);
  // Dispatch, most specific first: the tour's encoded observability reads, the console's story
  // (handlers/console.ts: Releases, Environments, Secrets, Domains, Activity; config, inventory,
  // storage; handlers/siteAudit.ts: Site insights' audits), then forge's generated defaults for
  // every other read of the console's services (handlers/defaults.ts). A read naming another project or environment is answered empty
  // first (handlers/scope.ts), defaults included, which is why the pack composes its own
  // defaults rather than declaring them as the pack's `defaults`.
  // The id prefix is the outermost layer: a request loses it before the scope check reads it, and an answer gains it last.
  const handlers = withSpeedInsights({ ...defaultHandlers(), ...consoleScenarioHandlers(scope), ...configScenarioHandlers(), ...inventoryScenarioHandlers(), ...storageScenarioHandlers(), ...organizationScenarioHandlers(), ...observabilityHandlers(story), ...siteAuditScenarioHandlers(scope), ...securityHandlers(undefined, scope), ...testsHandlers(undefined, scope), ...coverageSourceHandlers(scope), ...changesHandlers(undefined, scope) });
  const scoped = withMessageRequests(scopedToDemo(handlers, DEMO_DEFAULT_SERVICES, scope));
  return { name: "shop", handlers: withIdPrefix(scoped, SHOP_PACK_SERVICES, idPrefix, new Set([scope.app])) as unknown as MockScenario["handlers"] };
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
  /**
   * The app the story is about: every answer names it, and a read naming
   * another app is answered empty. Default `shop` (the public demo's).
   */
  app?: string;
  /**
   * The org a read must name to be answered, or null for any. Default: the
   * story's own (shopStory.ts DEMO_ORG). The forge tour's sample passes null:
   * it runs in whichever org the person is in.
   */
  org?: string | null;
  /** What every id the pack answers starts with (handlers/idPrefix.ts). Default: none. */
  idPrefix?: string;
}

/** The scenario options `options` name. */
function scenarioOptions({ app = DEMO_PROJECT, org = DEMO_SCOPE.org, idPrefix = "" }: ShopPackOptions): ShopScenarioOptions {
  return { scope: { app, org }, idPrefix };
}

/**
 * The pack, its clock pinned to `now` when given, which must happen before
 * anything reads it (shopStory.ts pinClock). Serve it with the pack clock:
 * `createFixtureTransport({ pack: shopPack(), now: demoNow, readOnly: true })`.
 *
 * Its `answers` are DEMO_PACK_ANSWERS, so only a pure fixture transport may
 * serve it (createShopTransport's passthrough serves shopScenario instead).
 */
export function shopPack(options: ShopPackOptions = {}): FixturePack {
  if (options.now !== undefined) pinClock(options.now);
  const scenario = scenarioOptions(options);
  return defineFixturePack({
    name: "shop",
    scenario: () => shopScenario(undefined, scenario),
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
    scenario: { ...shopScenario(undefined, scenarioOptions(pack)), passthrough: "first" },
    fallback: passthrough.fallback,
    ...readOnly,
    ...(passthrough.timeoutMs !== undefined ? { passthroughTimeoutMs: passthrough.timeoutMs } : {}),
    ...(passthrough.onFallback ? { onPassthroughFallback: passthrough.onFallback } : {}),
  });
}
