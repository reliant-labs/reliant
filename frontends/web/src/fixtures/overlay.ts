// Copyright (c) 2025 Reliant Labs

/**
 * Helpers for the packs and overlays the screenshot harness and the hosted
 * preview compose over the shop pack. HARNESS POLICY, not part of forge's
 * fixture-transport API (./transport.ts serves forge's, plus reliant's
 * extensions): everything here is built from that API's public options
 * alone, so it keeps working as the extensions move into forge.
 *
 *   withEmptyReads(options, services)  an unanswered READ of these services
 *                                      answers the empty response (the old
 *                                      visual shim's `{}`), instead of
 *                                      Unimplemented. Off unless called.
 *   pending(signal)                    a call that never answers until aborted
 *   fail(code, message)                a call that answers with an error
 *
 * Overlay layers, for a named state a pack does not have (an outage, an
 * empty org, a read that never comes back), put over the pack with `extends`:
 *
 *   answering(name, methods, answer)   these methods answer `answer(method, req, ctx)`
 *   refining(name, method, pack, fn)   this method answers the pack's answer, changed by `fn`
 *   failing(name, methods, code, msg)  these methods fail with `code`
 *   emptying(name, methods)            these methods answer the empty message
 *   stalling(name, methods)            these methods never answer (loading)
 *   latency(ms, methods?)              an interceptor that holds each answer `ms`
 *   logged(tag)                        an interceptor that logs each call
 */

import { create, type DescMethod, type DescService, type MessageShape } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type HandlerContext, type Interceptor } from "@connectrpc/connect";
import type { AnyServiceDefaults } from "@reliantlabs/forge-web-runtime/mock-transport";

import { defineFixturePack, type CreateFixtureTransportOptions, type FixturePack } from "./transport";

/** What an overlay helper applies to: whole services, single methods, or a mix. */
export type FixtureMethods = DescService | DescMethod | readonly (DescService | DescMethod)[];

/** `methods`, as the methods they name (a service names all of its own). */
export function methodsOf(methods: FixtureMethods): DescMethod[] {
  const list = (Array.isArray(methods) ? methods : [methods]) as (DescService | DescMethod)[];
  return list.flatMap((entry) => (entry.kind === "service" ? entry.methods : [entry]));
}

const streams = (method: DescMethod) => method.methodKind === "server_streaming" || method.methodKind === "bidi_streaming";

/**
 * An overlay layer in which every one of `methods` answers
 * `answer(method, request, ctx)` (a server stream: the one message it
 * returns, then it ends), over whatever the pack below it answers. A write
 * among them is listed in the layer's `answers`, so the read-only guard lets
 * it reach this answer: an overlay is harness policy, never the public demo
 * pack (src/fixtures/__tests__/demoPack.test.ts).
 */
export function answering(
  name: string,
  methods: FixtureMethods,
  answer: (method: DescMethod, request: unknown, ctx: HandlerContext) => unknown,
): FixturePack {
  const list = methodsOf(methods);
  return defineFixturePack({
    name,
    answers: list,
    routes: (router) => {
      for (const method of list) {
        const impl = streams(method)
          ? async function* (request: unknown, ctx: HandlerContext) {
              yield await answer(method, request, ctx);
            }
          : (request: unknown, ctx: HandlerContext) => answer(method, request, ctx);
        router.rpc(method, impl as never);
      }
    },
  });
}

/**
 * An overlay layer in which every one of `methods` fails with `code` (a
 * backend's error), carrying `metadata` (forge's `x-forge-error-reason`, say).
 */
export function failing(name: string, methods: FixtureMethods, code: Code, message = `fixture: ${Code[code]}`, metadata?: HeadersInit): FixturePack {
  return answering(name, methods, () => {
    throw new ConnectError(message, code, metadata);
  });
}

/**
 * An overlay layer in which `method` answers what the pack's own handler for
 * it (`handlers`, a scenario map keyed "pkg.Service/Method") answers, changed
 * by `refine`: the story's answer with one fact turned (prod held on billing,
 * a policy set to deny). `refine` gets the decoded response message, and may
 * change it in place or return a new one.
 *
 * `handlers` may be a function, called at the first answer: building a pack's
 * handler map can read the pack clock, which no module may do at import (a
 * harness pins the clock after importing its overlays; shop/shopStory.ts).
 */
export function refining<M extends DescMethod>(
  name: string,
  method: M,
  handlers: Record<string, unknown> | (() => Record<string, unknown>),
  refine: (answer: MessageShape<M["output"]>, request: MessageShape<M["input"]>) => MessageShape<M["output"]> | void,
): FixturePack {
  const key = `${method.parent.typeName}/${method.name}`;
  const handlerOf = () => {
    const handler = (typeof handlers === "function" ? handlers() : handlers)[key];
    if (typeof handler !== "function") throw new ConnectError(`refining(${name}): the pack has no handler for ${key}`, Code.Unimplemented);
    return handler as (req: unknown) => unknown;
  };
  return answering(name, method, async (_method, request) => {
    const answer = create(method.output, (await handlerOf()(request)) as never) as MessageShape<M["output"]>;
    return refine(answer, request as MessageShape<M["input"]>) ?? answer;
  });
}

/** An overlay layer in which every one of `methods` answers its empty response message. */
export function emptying(name: string, methods: FixtureMethods): FixturePack {
  return answering(name, methods, (method) => create(method.output));
}

/** An overlay layer in which every one of `methods` stays in flight until aborted: a loading state that never settles. */
export function stalling(name: string, methods: FixtureMethods): FixturePack {
  return answering(name, methods, (_method, _request, ctx) => pending(ctx.signal));
}

/**
 * An interceptor that logs each call to the console (`[tag] Service/Method
 * {request}`, and the error code a refused or failed one answered), so a
 * screenshot can be checked against what was asked. Pass it as the
 * transport's `interceptors`.
 */
export function logged(tag: string): Interceptor {
  return (next) => async (req) => {
    const name = `${req.method.parent.typeName}/${req.method.name}`;
    const body = req.stream ? "(stream)" : JSON.stringify(req.message, (key, value: unknown) => (key === "$typeName" ? undefined : typeof value === "bigint" ? String(value) : value));
    try {
      const res = await next(req);
      console.info(`[${tag}] ${name} ${body}`);
      return res;
    } catch (err) {
      console.info(`[${tag}] ${name} ${body} → ${Code[ConnectError.from(err).code]}`);
      throw err;
    }
  };
}

/**
 * An interceptor that holds each answer `ms` before the caller sees it (only
 * `methods`' answers, when given): slow answers, for the loading states
 * between. Pass it as the transport's `interceptors`.
 */
export function latency(ms: number, methods?: FixtureMethods): Interceptor {
  const only = methods ? new Set(methodsOf(methods).map((method) => `${method.parent.typeName}/${method.name}`)) : null;
  return (next) => async (req) => {
    if (only && !only.has(`${req.method.parent.typeName}/${req.method.name}`)) return next(req);
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(resolve, ms);
      req.signal.addEventListener("abort", () => {
        clearTimeout(timer);
        reject(new ConnectError("fixture: the call was aborted", Code.Canceled));
      }, { once: true });
    });
    return next(req);
  };
}

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
 * "A read" is an rpc whose proto declares NO_SIDE_EFFECTS, what
 * `options.readOnly` lets through, so a write is never answered empty: with
 * readOnly on it is refused first, and with readOnly off it stays
 * Unimplemented. Built as defaults in a base layer under the pack, so every
 * route, scenario handler and service default the pack declares wins over it.
 */
export function withEmptyReads(options: CreateFixtureTransportOptions, services: readonly DescService[]): CreateFixtureTransportOptions {
  const empty: AnyServiceDefaults[] = services.map((service) => {
    const defaults: Record<string, unknown> = { $service: service };
    for (const method of service.methods) {
      if (!isRead(method)) continue;
      const streams = method.methodKind === "server_streaming" || method.methodKind === "bidi_streaming";
      defaults[method.localName] = streams ? openEmptyStream(method) : () => create(method.output);
    }
    return defaults as unknown as AnyServiceDefaults;
  });
  const base = defineFixturePack({ name: "emptyReads", defaults: empty });
  const pack = options.pack ? defineFixturePack({ name: options.pack.name, extends: [base, options.pack] }) : base;
  return { ...options, pack };
}
