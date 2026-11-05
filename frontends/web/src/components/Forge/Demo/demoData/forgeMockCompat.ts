// Copyright (c) 2025 Reliant Labs

/**
 * TEMPORARY: forge's mock transport with the options the demo needs, ahead of
 * the web-runtime release that has them. REPLACE ON THE NEXT
 * @reliantlabs/forge-web-runtime BUMP with the engine's own options, and
 * delete this file:
 *
 *   createMockTransport({ scenario, fallback, readOnly, passthroughTimeoutMs, onPassthroughFallback })
 *   MockScenario.passthrough: "first"
 *
 * (forge PR "web-runtime/mock-transport: runtime scenario selection,
 * passthrough-first with fallback, read-only guard"; the gaps it closes are in
 * research/2026-11-05-console-health/forge-mock-friction.md.) This mirrors
 * that API exactly, wrapping the released engine, so the swap is an import.
 *
 *   readOnly        every RPC that is not a read is refused PermissionDenied
 *                   BEFORE any handler, fixture or backend sees it.
 *   "first"         the real backend answers first; when it fails (or takes
 *                   longer than passthroughTimeoutMs) the scenario answers
 *                   instead and onPassthroughFallback is told. The caller's
 *                   own abort is never masked.
 */

import type { DescMethod } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { createMockTransport as createEngineTransport, type MockScenario as EngineScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

export interface MockScenario extends Omit<EngineScenario, "passthrough"> {
  /** `true`: unmatched RPCs go to the backend (the engine's hybrid mode). `"first"`: every RPC does, the scenario answering when it fails. */
  passthrough?: boolean | "first";
}

export interface CreateMockTransportOptions {
  scenario: MockScenario;
  fallback?: Transport;
  /** Refuse every RPC that is not a read. `true` reads the proto's idempotency (NO_SIDE_EFFECTS); a function decides itself. */
  readOnly?: boolean | ((method: DescMethod) => boolean);
  /** The message a refused write carries. */
  readOnlyMessage?: string;
  /** With passthrough "first": the forwarded call's deadline, ms. */
  passthroughTimeoutMs?: number;
  /** With passthrough "first": told each time the backend failed and the scenario answered. */
  onPassthroughFallback?: (key: string, error: ConnectError) => void;
}

/** MethodIdempotency.NoSideEffects in @bufbuild/protobuf. */
const NO_SIDE_EFFECTS = 1;

const keyOf = (method: DescMethod) => `${method.parent.typeName}/${method.name}`;

export function createMockTransport(options: CreateMockTransportOptions): Transport {
  const { scenario, fallback, readOnly, passthroughTimeoutMs, onPassthroughFallback } = options;
  const first = scenario.passthrough === "first" && fallback != null;
  const engine = createEngineTransport({
    scenario: { ...scenario, passthrough: scenario.passthrough === true },
    fallback,
  });
  const isRead = typeof readOnly === "function" ? readOnly : (method: DescMethod) => (method as { idempotency?: number }).idempotency === NO_SIDE_EFFECTS;
  const guard = (method: DescMethod) => {
    if (readOnly && !isRead(method)) {
      throw new ConnectError(options.readOnlyMessage ?? `mock-transport: ${keyOf(method)} is not a read`, Code.PermissionDenied);
    }
  };

  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      guard(method);
      if (first) {
        try {
          return await fallback!.unary(method, signal, passthroughTimeoutMs ?? timeoutMs, header, input, contextValues);
        } catch (err) {
          if (signal?.aborted) throw err;
          onPassthroughFallback?.(keyOf(method), ConnectError.from(err));
        }
      }
      return engine.unary(method, signal, timeoutMs, header, input, contextValues);
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      guard(method);
      if (first) {
        try {
          return await fallback!.stream(method, signal, passthroughTimeoutMs ?? timeoutMs, header, input, contextValues);
        } catch (err) {
          if (signal?.aborted) throw err;
          onPassthroughFallback?.(keyOf(method), ConnectError.from(err));
        }
      }
      return engine.stream(method, signal, timeoutMs, header, input, contextValues);
    },
  };
}
