// Copyright (c) 2025 Reliant Labs

/**
 * Helpers for the packs and overlays the screenshot harness and the hosted
 * preview compose over the shop pack. HARNESS POLICY, not part of forge's
 * fixture-transport API (./transport.ts mirrors that): everything here is
 * built from that API's public options alone, so it keeps working, unchanged,
 * when transport.ts becomes a re-export of forge's.
 *
 *   withEmptyReads(options, services)  an unanswered READ of these services
 *                                      answers the empty response (the old
 *                                      visual shim's `{}`), instead of
 *                                      Unimplemented. Off unless called.
 *   pending(signal)                    a call that never answers until aborted
 *   fail(code, message)                a call that answers with an error
 */

import { create, type DescMethod, type DescService } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import type { AnyServiceDefaults } from "@reliantlabs/forge-web-runtime/mock-transport";

import { defineFixturePack, type CreateFixtureTransportOptions } from "./transport";

/**
 * A promise that never resolves, and rejects with Canceled when `signal`
 * aborts: a call that stays in flight (`async () => pending(ctx.signal)`), or
 * the tail of a server stream that stays open with no reconnect
 * (`yield* frames; await pending(ctx.signal)`).
 */
export function pending(signal?: AbortSignal): Promise<never> {
  return new Promise<never>((_, reject) => {
    if (!signal) return;
    const abort = () => reject(new ConnectError("fixture: the call was aborted", Code.Canceled));
    if (signal.aborted) abort();
    else signal.addEventListener("abort", abort, { once: true });
  });
}

/** A handler that answers with `code` (`fail(Code.Unimplemented)`), as a backend's error would. */
export function fail(code: Code, message = `fixture: ${Code[code]}`): () => never {
  return () => {
    throw new ConnectError(message, code);
  };
}

const isRead = (method: DescMethod) => method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS;

/**
 * A server stream that answers ONE empty message and then stays open (no
 * reconnect timer) until the caller aborts and the router drops it. The one
 * message is what makes it open: createRouterTransport sends a stream's
 * header only once the handler yields its first message, so a stream that
 * yields nothing never answers its header, and a settle probe counts it in
 * flight forever.
 */
const openEmptyStream = (method: DescMethod) =>
  async function* (): AsyncIterable<unknown> {
    yield create(method.output);
    await new Promise<never>(() => {});
  };

/**
 * `options` with one more rung at the bottom of its dispatch: a READ of one
 * of `services` that no route, scenario handler or service default answers
 * gets the empty response (a server stream: one empty message, then it stays
 * open until aborted)
 * instead of Unimplemented. Harness policy for the visual harness and the
 * hosted preview, where a read nobody wrote a fixture for should render as
 * "nothing there" rather than an error toast; the public demo does not use it.
 *
 * "A read" is what `options.readOnly` lets through (NO_SIDE_EFFECTS, or the
 * deprecated classifier's verdict), so a write is never answered empty: with
 * readOnly on it is refused first, and with readOnly off it stays
 * Unimplemented. Built as defaults in a base layer under the pack, so every
 * route, scenario handler and service default the pack declares wins over it.
 */
export function withEmptyReads(options: CreateFixtureTransportOptions, services: readonly DescService[]): CreateFixtureTransportOptions {
  const reads = typeof options.readOnly === "function" ? options.readOnly : isRead;
  const empty: AnyServiceDefaults[] = services.map((service) => {
    const defaults: Record<string, unknown> = { $service: service };
    for (const method of service.methods) {
      if (!reads(method)) continue;
      const streams = method.methodKind === "server_streaming" || method.methodKind === "bidi_streaming";
      defaults[method.localName] = streams ? openEmptyStream(method) : () => create(method.output);
    }
    return defaults as unknown as AnyServiceDefaults;
  });
  const base = defineFixturePack({ name: "emptyReads", defaults: empty });
  const pack = options.pack ? defineFixturePack({ name: options.pack.name, extends: [base, options.pack] }) : base;
  return { ...options, pack };
}
