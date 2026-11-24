// Copyright (c) 2025 Reliant Labs

/**
 * The fixture transport: the ONE seam every surface takes its sample data
 * from (the public demo today; the screenshot harness, the hosted preview and
 * the component previews next). A surface renders the real app over a
 * Connect transport whose answers come from a fixture pack (src/fixtures/<pack>/),
 * and every write is refused before a handler sees it.
 *
 * Local stand-in for forge's `createFixtureTransport` and `defineFixturePack`
 * (@reliantlabs/forge-web-runtime/fixture-transport, forge#2716, not yet
 * released). The names and call signatures here are a SUBSET of forge's, so
 * once a forge release ships it this file becomes a re-export and no caller
 * changes. Until then it is forge's published `createMockTransport`
 * underneath, which means:
 *
 *   - supported: a pack's `name`, `idPrefix`, `scenario` (a MockScenario, its
 *     handler map, or a function of the pack context) and `defaults`; the
 *     options' own `scenario` / `defaults` as a layer on top; `readOnly`,
 *     `readOnlyMessage`, `now`, `idPrefix`;
 *   - typed but refused (they throw): `routes`, `interceptors`, a pack's
 *     `extends`, `data` or `routes`. Nothing passes them yet;
 *   - one difference: `readOnly` also accepts a classifier function here
 *     (readOnly.ts isDemoRead). Deprecated; forge takes `true` only.
 *
 * Answers are not serialized across a Connect hop as forge's are, so a
 * fixture that could not travel over the wire is caught only after the swap.
 */

import type { DescMethod, DescService } from "@bufbuild/protobuf";
import type { ConnectRouter, Interceptor, Transport } from "@connectrpc/connect";
import { createMockTransport, isServiceDefaults, serviceDefaults, type AnyServiceDefaults, type MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

/** Named JSON fixtures (forge's `fixtureJson`). Not supported locally yet. */
export type FixtureDataMap = Record<string, unknown>;

/** What a pack's handlers are built with: the pack clock and its id namespace. */
export interface FixtureContext {
  /** The pack clock, in ms since the epoch. */
  now(): number;
  /** The pack's reserved id namespace ("" when none is declared). */
  readonly idPrefix: string;
  /** `${idPrefix}${local}`. */
  id(local: string): string;
  /** The pack's decoded `data`. Always empty locally. */
  readonly data: FixtureDataMap;
}

/** Typed ServiceImpl registrations (Connect's `createRouterTransport`). Not supported locally yet. */
export type FixtureRoutes = (router: ConnectRouter, ctx: FixtureContext) => void;

/** forge's mock-transport scenario, whole or its handler map ("pkg.Service/Method" keys). */
export type FixtureScenario = MockScenario | MockScenario["handlers"];

/** A service whose unanswered RPCs get descriptor-derived defaults, or prebuilt `serviceDefaults(...)`. */
export type FixtureDefaults = DescService | AnyServiceDefaults;

/** One world of sample data. Define with {@link defineFixturePack}, serve with `createFixtureTransport({ pack })`. */
export interface FixturePack {
  readonly name: string;
  /** Not supported locally yet. */
  readonly extends?: readonly FixturePack[];
  readonly idPrefix?: string;
  /** Not supported locally yet. */
  readonly data?: FixtureDataMap;
  /** Not supported locally yet. */
  readonly routes?: FixtureRoutes;
  readonly scenario?: FixtureScenario | ((ctx: FixtureContext) => FixtureScenario);
  readonly defaults?: readonly FixtureDefaults[];
}

/** Declare a fixture pack: an identity function that checks its name. */
export function defineFixturePack(pack: FixturePack): FixturePack {
  if (typeof pack.name !== "string" || pack.name === "") throw new Error("defineFixturePack: a pack needs a non-empty name");
  return pack;
}

export interface CreateFixtureTransportOptions {
  /** The pack to serve; `scenario` and `defaults` given here form one more layer on top of it (they win). */
  pack?: FixturePack;
  /** Not supported locally yet. */
  routes?: FixtureRoutes;
  scenario?: FixtureScenario;
  defaults?: readonly FixtureDefaults[];
  /**
   * `true`: refuse every RPC whose proto does not declare
   * `idempotency_level = NO_SIDE_EFFECTS`, with PermissionDenied, before any
   * handler sees it.
   *
   * @deprecated The function form (readOnly.ts isDemoRead) is local only and
   * is removed once the read RPCs are annotated; forge's `readOnly` is a boolean.
   */
  readOnly?: boolean | ((method: DescMethod) => boolean);
  /** The message a refused RPC carries. */
  readOnlyMessage?: string;
  /** The pack clock (ms since the epoch); default `Date.now`. Defaults are anchored at its value when the transport is built. */
  now?: () => number;
  /** The id namespace when the pack declares none. */
  idPrefix?: string;
  /** Not supported locally yet. */
  interceptors?: Interceptor[];
}

const handlersOf = (scenario: FixtureScenario | undefined): MockScenario["handlers"] =>
  !scenario ? {} : "handlers" in scenario && typeof scenario.name === "string" ? (scenario as MockScenario).handlers : (scenario as MockScenario["handlers"]);

export function createFixtureTransport(options: CreateFixtureTransportOptions = {}): Transport {
  const { pack, routes, scenario, defaults = [], readOnly, readOnlyMessage, now = Date.now, idPrefix = "", interceptors } = options;
  const unsupported = [routes && "routes", interceptors && "interceptors", pack?.extends && "pack.extends", pack?.data && "pack.data", pack?.routes && "pack.routes"].filter(Boolean);
  if (unsupported.length) throw new Error(`createFixtureTransport: ${unsupported.join(", ")} not supported until forge's fixture transport ships; pass a scenario.`);
  const prefix = pack?.idPrefix ?? idPrefix;
  const ctx: FixtureContext = { now, idPrefix: prefix, id: (local) => `${prefix}${local}`, data: {} };
  const packScenario = typeof pack?.scenario === "function" ? pack.scenario(ctx) : pack?.scenario;
  const declared = [...(pack?.defaults ?? []), ...defaults];
  // The clock is read only when a default needs it: reading a pack's clock fixes it (shop/shopStory.ts).
  const anchor = declared.some((d) => !isServiceDefaults(d)) ? { now: now() } : {};
  const services = declared.map((d) => (isServiceDefaults(d) ? d : serviceDefaults(d as DescService, anchor)));
  return createMockTransport({
    scenario: { name: pack?.name ?? "fixtures", handlers: { ...handlersOf(packScenario), ...handlersOf(scenario) } },
    ...(services.length ? { services } : {}),
    ...(readOnly !== undefined ? { readOnly } : {}),
    ...(readOnlyMessage !== undefined ? { readOnlyMessage } : {}),
  });
}
