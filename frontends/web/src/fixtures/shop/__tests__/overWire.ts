// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack as the demo host reads it: the console's REAL observability
 * client (controlPlaneObservabilityApi: api.ts and friends, their mappers, the
 * read scheduler) over the pack's transport, beside the story's model
 * (model/shopApi.ts), so a test can hold the two to one answer.
 *
 * The importing test file must mock the control-plane config first (vi.mock
 * is per file and hoisted):
 *
 *   vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
 */

import type { Transport } from "@connectrpc/connect";

import { installShellTransport, setTransport } from "@/lib/connect";
import { controlPlaneObservabilityApi, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopTransport } from "..";
import { createShopApi } from "../model/shopApi";
import { demoNow, resetDemoNowForTests } from "../shopStory";

export interface OverWire {
  /** The pack's transport, installed as the shell's. */
  transport: Transport;
  /** The real client: every read goes over `transport`. */
  wire: ObservabilityApi;
  /** The story's model at the same instant: what the wire must decode back to. */
  model: ObservabilityApi;
  now: number;
}

/** Pin the pack clock to `at`, install the shop transport, and hand back both sides. Call `uninstallOverWire` after. */
export function installOverWire(at: number): OverWire {
  resetDemoNowForTests(at);
  const now = demoNow();
  setTransport(null);
  const transport = createShopTransport();
  installShellTransport(transport);
  return { transport, wire: controlPlaneObservabilityApi, model: createShopApi(now), now };
}

export function uninstallOverWire(): void {
  installShellTransport(null as never);
  resetDemoNowForTests();
}
