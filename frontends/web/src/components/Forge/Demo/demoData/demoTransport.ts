// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's Connect transport: forge's mock transport with the `demo`
 * scenario (demoScenario.ts), installed for the whole demo shell at boot
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
 * console's read-only layer disables the controls first, readOnlyApi.ts).
 * The gateway's contract is research/2026-11-05-console-health/demo-plan.md,
 * "Phase 2: demo-data gateway contract".
 */

import type { DescMethod } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import type { Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { loadConfig } from "@/lib/config_gen";

import { createDemoScenario } from "./demoScenario";
import { setDemoDataStatus } from "./demoDataStatus";
import { createMockTransport } from "./forgeMockCompat";

/** How long the gateway gets before the sample data answers instead. */
export const REMOTE_TIMEOUT_MS = 4_000;

/**
 * Read-only verbs, for the methods whose proto declares no idempotency (most
 * of control-plane's today): reads that list, fetch, search, query, compare,
 * watch or stream, or that describe, preview or plan a change without making
 * it. Every other name is a write.
 */
const READ_VERB = /^(List|Get|Search|Query|Compare|Watch|Stream|Describe|Preview|Plan|Check|Resolve)/;

/**
 * The reads the demo may make. The proto's own word wins: a method declared
 * `idempotency_level = NO_SIDE_EFFECTS` is a read, and one declared
 * IDEMPOTENT (a set, a delete) is a write whatever its name. Otherwise the
 * verb decides (READ_VERB).
 */
export function isDemoRead(method: Pick<DescMethod, "name"> & Partial<Pick<DescMethod, "idempotency">>): boolean {
  if (method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS) return true;
  if (method.idempotency === MethodOptions_IdempotencyLevel.IDEMPOTENT) return false;
  return READ_VERB.test(method.name);
}

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

/** The transport for `config` (the runtime config's, by default). `gateway` replaces the Connect transport to DEMO_DATA_URL (tests). */
export function createDemoTransport(config: DemoDataConfig = demoDataConfig(), gateway?: Transport): Transport {
  const scenario = createDemoScenario();
  if (config.source !== "remote") {
    setDemoDataStatus("fixtures");
    return createMockTransport({ scenario, readOnly: isDemoRead, readOnlyMessage: "This is a demo. Changes are turned off." });
  }
  setDemoDataStatus("live");
  return createMockTransport({
    scenario: { ...scenario, passthrough: "first" },
    // No auth interceptors: the gateway is public and holds its own credential.
    fallback: gateway ?? createConnectTransport({ baseUrl: config.url, useBinaryFormat: false }),
    readOnly: isDemoRead,
    readOnlyMessage: "This is a demo. Changes are turned off.",
    passthroughTimeoutMs: REMOTE_TIMEOUT_MS,
    onPassthroughFallback: () => setDemoDataStatus("fallback"),
  });
}
