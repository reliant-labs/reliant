// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's Connect transport: the shop fixture pack's APP LAYER
 * (src/fixtures/shop/app.ts createShopAppTransport: the console's pack plus
 * the reliant app's reads, so the Agents pages, the Inbox, Workflows,
 * Connectors and Sandboxes show the shop's chats, runs and machines), over
 * src/fixtures/transport.ts, installed for the whole demo shell at boot
 * (lib/connect.ts installShellTransport), so nothing on the demo host can
 * reach a real backend by accident. It is the app layer's data only: fixture
 * mode's install, its fake Supabase and its session (src/fixtures/install.ts,
 * outside.ts) are the app bundle's, never the demo's
 * (src/fixtures/shop/__tests__/appLayer.test.ts).
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
 *                                        An rpc the app layer answers (chats,
 *                                        projects, machines, …) never goes to
 *                                        the gateway: the app layer answers it.
 *
 * Writes never leave the browser either way: the transport refuses every RPC
 * that is not a read before the scenario or the gateway sees it (and the
 * console's read-only layer disables the controls first, obsReadOnly.tsx).
 * The gateway's contract is research/2026-11-05-console-health/demo-plan.md,
 * "Phase 2: demo-data gateway contract".
 */

import type { DescMethod } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { SHOP_READ_ONLY_MESSAGE } from "@/fixtures/shop";
import { createShopAppTransport } from "@/fixtures/shop/app";
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

/**
 * What the demo could not answer, by "pkg.Service/Method", on
 * `window.__demoRpcs`: a read no sample data answers (`unknown`, which the
 * page shows as "Sample data isn't available") and a write the read-only
 * guard refused (`refused`). For a smoke check, and for whoever adds the
 * missing sample data (fixture mode's probe is src/fixtures/install.ts).
 */
export interface DemoRpcProbe {
  unknown: string[];
  refused: string[];
}

declare global {
  interface Window {
    __demoRpcs?: DemoRpcProbe;
  }
}

/** `transport`, recording on `window.__demoRpcs` each call it could not answer. */
function probed(transport: Transport): Transport {
  if (typeof window === "undefined") return transport;
  const probe = (window.__demoRpcs ??= { unknown: [], refused: [] });
  const record = (method: DescMethod, err: unknown) => {
    if (!(err instanceof ConnectError)) return;
    const key = `${method.parent.typeName}/${method.name}`;
    if (err.code === Code.Unimplemented) probe.unknown.push(key);
    else if (err.code === Code.PermissionDenied && err.rawMessage === SHOP_READ_ONLY_MESSAGE) probe.refused.push(key);
  };
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      try {
        return await transport.unary(method, signal, timeoutMs, header, input, contextValues);
      } catch (err) {
        record(method, err);
        throw err;
      }
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      let res;
      try {
        res = await transport.stream(method, signal, timeoutMs, header, input, contextValues);
      } catch (err) {
        record(method, err);
        throw err;
      }
      // A refused or unanswered stream opens, and its body fails.
      const body = res.message;
      async function* watched() {
        try {
          yield* body;
        } catch (err) {
          record(method, err);
          throw err;
        }
      }
      return { ...res, message: watched() } as typeof res;
    },
  };
}

/** The transport for `config` (the runtime config's, by default): the shop app layer's (src/fixtures/shop/app.ts). `gateway` replaces the Connect transport to DEMO_DATA_URL (tests). */
export function createDemoTransport(config: DemoDataConfig = demoDataConfig(), gateway?: Transport): Transport {
  return probed(createDataTransport(config, gateway));
}

function createDataTransport(config: DemoDataConfig, gateway?: Transport): Transport {
  if (config.source !== "remote") {
    setDemoDataStatus("fixtures");
    return createShopAppTransport();
  }
  setDemoDataStatus("live");
  return createShopAppTransport({
    passthrough: {
      // No auth interceptors: the gateway is public and holds its own credential.
      fallback: gateway ?? createConnectTransport({ baseUrl: config.url, useBinaryFormat: false }),
      timeoutMs: REMOTE_TIMEOUT_MS,
      onFallback: () => setDemoDataStatus("fallback"),
    },
  });
}
