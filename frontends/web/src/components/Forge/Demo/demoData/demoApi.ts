// Copyright (c) 2025 Reliant Labs

/**
 * The demo console's ObservabilityApi.
 *
 *  - Every shell: shop's story (shopStory.ts, answered by shop/shopApi.ts),
 *    behind the read-only layer (readOnlyApi.ts).
 *  - The public demo host (its Connect transport is forge's mock transport,
 *    demoShellBoot.ts): the reads the `demo` scenario serves on the wire
 *    (DEMO_SCENARIO_RPCS) go through the console's REAL client instead, so
 *    api.ts, its mappers and the read scheduler run as in production, and
 *    DEMO_DATA_SOURCE=remote swaps the data with no code change.
 *
 * Elsewhere (Reliant's and the forge shell's /forge/demo) the real client
 * would reach the real control plane, so those shells keep the fixtures.
 */

import { hasControlPlane } from "@/services/controlPlane/config";
import { controlPlaneObservabilityApi, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { isDemoShell } from "@/lib/shell";
import { yieldToMain } from "@/lib/yieldToMain";

import { createShopApi } from "./shop/shopApi";
import { withReadOnly } from "../../Observability/readOnlyApi";
import { DEMO_SCENARIO_RPCS, type DemoScenarioMethod } from "./demoScenario";

/**
 * The fixtures answer in the browser, synchronously: each read waits for a new
 * task first, so a page's burst of reads is many short tasks, not one long one.
 */
function yielding(api: ObservabilityApi): ObservabilityApi {
  const out: Record<string, unknown> = {};
  for (const [key, member] of Object.entries(api)) {
    out[key] =
      typeof member === "function"
        ? async (...args: unknown[]) => {
            await yieldToMain();
            return (member as (...a: unknown[]) => unknown)(...args);
          }
        : member;
  }
  return out as unknown as ObservabilityApi;
}

export interface DemoApiOptions {
  /** Read the scenario's RPCs over the Connect transport. Default: on the demo host with a control-plane URL configured. */
  wire?: boolean;
}

export function createDemoApi({ wire = isDemoShell() && hasControlPlane }: DemoApiOptions = {}): ObservabilityApi {
  const fixtures = yielding(createShopApi());
  if (!wire) return withReadOnly(fixtures);
  const overWire = Object.fromEntries(
    (Object.keys(DEMO_SCENARIO_RPCS) as DemoScenarioMethod[]).map((method) => [method, controlPlaneObservabilityApi[method]]),
  ) as Pick<ObservabilityApi, DemoScenarioMethod>;
  return withReadOnly({ ...fixtures, ...overWire });
}
