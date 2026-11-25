// Copyright (c) 2026 Reliant Labs

/**
 * The pack's ids on the wire, minted with a prefix: the forge tour's sample
 * (`sample_`, Forge/Sample/sampleApp.ts SAMPLE_ID_PREFIX) tells the shop's
 * story with every id it answers starting with it, so an id that leaks out of
 * the sample's scope is recognised and refused by the live transport
 * (lib/sampleGuard.ts) instead of reaching a backend. The public demo mints
 * none (no prefix, no wrapper: its wire is unchanged).
 *
 * The story keeps its own ids (`env-prod`, `rel-v1.8.4`, …); this is the
 * boundary between it and the wire, one generic walk over each message
 * (lib/protoStrings.ts), never per-method code:
 *
 *   answer   every id field (`id`, `…_id`, `…_ids`, lib/protoStrings.ts
 *            isIdField) that is set gets the prefix;
 *   request  every string that starts with the prefix loses it, before
 *            anything in the pack (its scope check included) reads it.
 *
 * The app's NAME is not rewritten here, either way: the pack tells the story
 * for it (shopPack({ app }), handlers/scope.ts PackScope), and the sample's
 * name starts with the prefix too (`sample_shop`), so it is one of the `kept`
 * values: answered and read as it is, in an id field or not.
 */

import { clone, create, isMessage, type DescMessage, type DescMethod, type DescService, type Message } from "@bufbuild/protobuf";

import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { isIdField, mapStrings } from "@/lib/protoStrings";

/** `value` as a message of `desc` the caller owns: a copy, never the caller's or the pack's own object. */
function owned(desc: DescMessage, value: unknown): Message {
  return isMessage(value) ? clone(desc, value as Message) : create(desc, value as never);
}

function isIterable(value: unknown): value is Iterable<unknown> {
  return value != null && typeof value === "object" && !isMessage(value) && typeof (value as Iterable<unknown>)[Symbol.iterator] === "function";
}

function isAsyncIterable(value: unknown): value is AsyncIterable<unknown> {
  return value != null && typeof value === "object" && typeof (value as AsyncIterable<unknown>)[Symbol.asyncIterator] === "function";
}

async function* eachOf(values: AsyncIterable<unknown>, map: (value: unknown) => unknown): AsyncIterable<unknown> {
  for await (const value of values) yield map(value);
}

/**
 * `handlers` whose answers carry every id with `prefix`, and whose requests
 * arrive with it removed; a `kept` value is never rewritten. Handlers of
 * methods not in `services` pass as they are. An empty prefix returns
 * `handlers` itself.
 */
export function withIdPrefix(handlers: MockHandlers, services: readonly DescService[], prefix: string, kept: ReadonlySet<string> = new Set()): MockHandlers {
  if (!prefix) return handlers;
  const methods = new Map<string, DescMethod>(services.flatMap((s) => s.methods.map((m): [string, DescMethod] => [`${s.typeName}/${m.name}`, m])));
  const unprefixed = (desc: DescMessage) => (req: unknown) => {
    if (req == null || typeof req !== "object") return req;
    const msg = owned(desc, req);
    mapStrings(desc, msg, (_field, value) => (value.startsWith(prefix) && !kept.has(value) ? value.slice(prefix.length) : value));
    return msg;
  };
  const prefixed = (desc: DescMessage) => (res: unknown) => {
    if (res == null || typeof res !== "object") return res;
    const msg = owned(desc, res);
    mapStrings(desc, msg, (field, value) => (value !== "" && isIdField(field.name) && !value.startsWith(prefix) && !kept.has(value) ? prefix + value : value));
    return msg;
  };
  const out: MockHandlers = {};
  for (const [key, handler] of Object.entries(handlers)) {
    const method = methods.get(key);
    if (!method) {
      out[key] = handler;
      continue;
    }
    const request = unprefixed(method.input);
    const answer = prefixed(method.output);
    out[key] = async (req: unknown) => {
      const res = await handler(Array.isArray(req) ? req.map(request) : request(req));
      if (isAsyncIterable(res)) return eachOf(res, answer);
      if (isIterable(res)) return [...res].map(answer);
      return answer(res);
    };
  }
  return out;
}
