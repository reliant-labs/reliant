// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's Connect transport: the shop fixture pack's
 * (src/fixtures/shop, over src/fixtures/transport.ts), installed for the whole demo shell at boot
 * (lib/connect.ts installShellTransport), so nothing on the demo host can
 * reach a real backend by accident.
 *
 * Where the reads come from is runtime config, not code:
 *
 *   DEMO_DATA_SOURCE=fixtures (default)  the scenario answers; no network.
 *   DEMO_DATA_SOURCE=remote + DEMO_DATA_URL
 *                                        the demo-data gateway answers first
 *                                        (Connect/JSON, read RPCs only); on any
 *                                        failure or after REMOTE_TIMEOUT_MS the
 *                                        scenario answers instead, and the
 *                                        banner says it is showing sample data.
 *
 * Writes never leave the browser either way: the transport refuses every RPC
 * that is not a read before the scenario or the gateway sees it (and the
 * console's read-only layer disables the controls first, obsReadOnly.tsx).
 * The gateway's contract is research/2026-11-05-console-health/demo-plan.md,
 * "Phase 2: demo-data gateway contract".
 */

import type { Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { createShopTransport } from "@/fixtures/shop";
import { loadConfig } from "@/lib/config_gen";

import { setDemoDataStatus } from "./demoDataStatus";

/** How long the gateway gets before the sample data answers instead. */
export const REMOTE_TIMEOUT_MS = 4_000;

export interface DemoDataConfig {
  source: "fixtures" | "remote";
  url: string;
}

/** DEMO_DATA_SOURCE / DEMO_DATA_URL; "remote" without a URL is the sample data. */
export function demoDataConfig(): DemoDataConfig {
  let source = "";
  let url = "";
  try {
    const config = loadConfig();
    source = config.DEMO_DATA_SOURCE;
    url = config.DEMO_DATA_URL.trim();
  } catch {
    // An invalid config document is configGuard's to report.
  }
  return source === "remote" && /^https?:\/\//.test(url) ? { source: "remote", url: url.replace(/\/$/, "") } : { source: "fixtures", url: "" };
}

/** The transport for `config` (the runtime config's, by default): the shop fixture pack's (src/fixtures/shop). `gateway` replaces the Connect transport to DEMO_DATA_URL (tests). */
export function createDemoTransport(config: DemoDataConfig = demoDataConfig(), gateway?: Transport): Transport {
  if (config.source !== "remote") {
    setDemoDataStatus("fixtures");
    return createShopTransport();
  }
  setDemoDataStatus("live");
  return createShopTransport({
    passthrough: {
      // No auth interceptors: the gateway is public and holds its own credential.
      fallback: gateway ?? createConnectTransport({ baseUrl: config.url, useBinaryFormat: false }),
      timeoutMs: REMOTE_TIMEOUT_MS,
      onFallback: () => setDemoDataStatus("fallback"),
    },
  });
}
