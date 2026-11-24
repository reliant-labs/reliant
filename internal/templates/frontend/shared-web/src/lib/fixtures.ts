// Fixture data declared per environment, read at RUNTIME.
//
// An env serves a fixture dataset instead of the backend by declaring it on the
// frontend in deploy/kcl/<env>/main.k:
//
//   fixtures = forge.Fixtures { scenario = "shop" }   // read_only = True
//
// forge renders that into this frontend's runtime config document (config.js,
// which assigns window.__FORGE_CONFIG__ before the bundle runs) as two keys:
//
//   FIXTURES_SCENARIO   the scenario (src/mocks/scenarios/<name>.ts) to answer from
//   FIXTURES_READ_ONLY  "true", or "false" when the env set read_only = False
//
// Nothing here reads a build variable, so one release digest serves live data
// in prod and the fixture dataset in a demo env promoted from it by digest.
// src/lib/connect.ts swaps the transport; src/lib/auth/session-provider.ts
// signs the UI in as the fixture user, since no backend answers /auth/session.
import type { Transport } from "@connectrpc/connect";

export interface RuntimeFixtures {
  /** The scenario name the env declares. */
  scenario: string;
  /** Only `idempotency_level = NO_SIDE_EFFECTS` rpcs answer; writes are refused. */
  readOnly: boolean;
}

/**
 * The fixtures this environment's runtime config names, or null when it names
 * none (the live backend). Null during a server render, where there is no
 * config.js. Read-only unless the document says exactly "false", so a missing
 * or mistyped key keeps writes refused.
 */
export function runtimeFixtures(): RuntimeFixtures | null {
  if (typeof window === "undefined") return null;
  const cfg = (window as unknown as { __FORGE_CONFIG__?: Record<string, unknown> })
    .__FORGE_CONFIG__;
  const scenario = cfg?.FIXTURES_SCENARIO;
  if (typeof scenario !== "string" || scenario === "") return null;
  return { scenario, readOnly: cfg?.FIXTURES_READ_ONLY !== "false" };
}

/**
 * A Transport that loads the real one on its first call. Every Transport
 * method is already async, so a caller cannot tell the difference, and the
 * module behind `load` (the mock transport and its fixtures) stays in a lazy
 * chunk that a live environment never downloads.
 */
export function lazyTransport(load: () => Promise<Transport>): Transport {
  let loaded: Promise<Transport> | undefined;
  const get = () => (loaded ??= load());
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      return (await get()).unary(method, signal, timeoutMs, header, input, contextValues);
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      return (await get()).stream(method, signal, timeoutMs, header, input, contextValues);
    },
  };
}
