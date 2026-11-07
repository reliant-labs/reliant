// Copyright (c) 2025 Reliant Labs

/**
 * The demo console's ObservabilityApi.
 *
 *  - Every shell: the sample app's fixtures (the shop graph, the toured trace
 *    crossing into inventory, dashboards read-only), behind the read-only
 *    layer (readOnlyApi.ts).
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

import { createDemoFixtures } from "./busyApp";
import { withReadOnly } from "../../Observability/readOnlyApi";
import { DEMO_SCENARIO_RPCS, type DemoScenarioMethod } from "./demoScenario";

export interface DemoApiOptions {
  /** Read the scenario's RPCs over the Connect transport. Default: on the demo host with a control-plane URL configured. */
  wire?: boolean;
}

export function createDemoApi({ wire = isDemoShell() && hasControlPlane }: DemoApiOptions = {}): ObservabilityApi {
  const fixtures = createDemoFixtures();
  if (!wire) return withReadOnly(fixtures);
  const overWire = Object.fromEntries(
    (Object.keys(DEMO_SCENARIO_RPCS) as DemoScenarioMethod[]).map((method) => [method, controlPlaneObservabilityApi[method]]),
  ) as Pick<ObservabilityApi, DemoScenarioMethod>;
  return withReadOnly({ ...fixtures, ...overWire });
}
