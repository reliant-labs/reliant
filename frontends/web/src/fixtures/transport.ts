// Copyright (c) 2025 Reliant Labs

/**
 * The fixture transport: the ONE seam every surface takes its sample data
 * from (the public demo today; the screenshot harness, the hosted preview and
 * the component previews next). A surface renders the real app over a
 * Connect transport whose answers come from a fixture pack (src/fixtures/<pack>/),
 * and every write is refused before a handler sees it.
 *
 * The transport is forge's: `createFixtureTransport`, `defineFixturePack`,
 * `fixtureJson` and `checkFixturePack` from
 * @reliantlabs/forge-web-runtime/fixture-transport (forge#2716, web-runtime
 * 0.13.22). Composition and dispatch (every call crosses Connect's in-memory
 * router; per RPC: readOnly guard → latest layer's route or scenario handler
 * → service defaults → Unimplemented) are forge's alone; see its module notes.
 *
 * This module adds a thin RELIANT EXTENSION layer on top, built from forge's
 * public options (an interceptor and the packs' own fields), never a copy of
 * its dispatch. Each is a candidate for forge; once forge has it, its part
 * here goes:
 *
 *   1. (Gone.) `readOnly` took a classifier function (the name heuristic
 *      readOnly.ts isDemoRead). The read RPCs now declare NO_SIDE_EFFECTS,
 *      so `readOnly` is a boolean, as forge's is.
 *   2. `answers` (pack and options) and `services`: an explicit list of
 *      side-effecting RPCs a pack answers from fixtures anyway (reads that
 *      wake a sleeping machine, say). The readOnly guard lets exactly those
 *      through, and only to a handler or default that exists. Each string
 *      entry must name a method of a service the pack registers through
 *      `defaults` or `services`, or the build throws. Only a pure fixture
 *      transport may honour it: a path that can reach live traffic calls
 *      {@link assertPureFixturePack}.
 *
 * A pack that lists no `answers` goes to forge's transport unchanged, and
 * forge's own guard refuses every write under `readOnly: true`. forge's guard
 * cannot let a listed write through, so only for a pack that lists some does
 * forge serve with its guard off, and {@link readOnlyGuard}, the innermost
 * client interceptor, applies the same rule plus the list: after every caller
 * interceptor, before the router, with forge's code, message and timing (a
 * refused stream opens, and its body fails).
 *
 * The visual harness's "an unanswered read answers the empty message" is
 * harness policy, not part of this API: overlay.ts `withEmptyReads`.
 */

import type { DescMethod, DescService } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type Interceptor, type StreamResponse, type Transport } from "@connectrpc/connect";
import {
  checkFixturePack as forgeCheckFixturePack,
  createFixtureTransport as forgeCreateFixtureTransport,
  defineFixturePack as forgeDefineFixturePack,
  type CheckFixturePackOptions,
  type CreateFixtureTransportOptions as ForgeCreateFixtureTransportOptions,
  type FixtureDataMap,
  type FixtureDefaults,
  type FixturePack as ForgeFixturePack,
  type FixtureProblem,
} from "@reliantlabs/forge-web-runtime/fixture-transport";
import type { AnyServiceDefaults } from "@reliantlabs/forge-web-runtime/mock-transport";

export {
  fixtureJson,
  type CheckFixturePackOptions,
  type FixtureContext,
  type FixtureDataMap,
  type FixtureDataOf,
  type FixtureDefaults,
  type FixtureJson,
  type FixtureProblem,
  type FixtureRoutes,
  type FixtureScenario,
} from "@reliantlabs/forge-web-runtime/fixture-transport";

/**
 * RELIANT EXTENSION (2): a side-effecting RPC a pack answers from fixtures
 * anyway, as `"pkg.Service/Method"` or its descriptor. Never a pattern.
 */
export type FixtureAnswer = string | DescMethod;

/** forge's fixture pack, with the RELIANT EXTENSION (2) fields. */
export interface FixturePack<D extends FixtureDataMap = FixtureDataMap> extends ForgeFixturePack<D> {
  // `any`: a pack composes packs over any data map (forge's rule).
  readonly extends?: readonly FixturePack<any>[];
  /**
   * RELIANT EXTENSION (2): side-effecting RPCs this pack answers from
   * fixtures although `readOnly` is on. Unioned across `extends`. Only an
   * app-layer or harness pack may declare any; the public demo pack declares
   * none (src/fixtures/__tests__/demoPack.test.ts).
   */
  readonly answers?: readonly FixtureAnswer[];
  /**
   * RELIANT EXTENSION (2): services `answers` entries are resolved against,
   * besides those the pack's `defaults` already carry (a scenario map's keys
   * are only strings, and a route's registrations are forge's to read).
   */
  readonly services?: readonly DescService[];
}

/** forge's `defineFixturePack`, typed for the extension fields. */
export function defineFixturePack<D extends FixtureDataMap = FixtureDataMap>(pack: FixturePack<D>): FixturePack<D> {
  forgeDefineFixturePack(pack);
  return pack;
}

export interface CreateFixtureTransportOptions extends Omit<ForgeCreateFixtureTransportOptions, "pack"> {
  pack?: FixturePack<any>;
  /** RELIANT EXTENSION (2): as {@link FixturePack.answers}, one more layer on top. */
  answers?: readonly FixtureAnswer[];
  /** RELIANT EXTENSION (2): as {@link FixturePack.services}. */
  services?: readonly DescService[];
}

/** The options' own layer, named as forge names it in errors. */
const OPTIONS_LAYER = "createFixtureTransport";

function keyOf(method: DescMethod): string {
  return `${method.parent.typeName}/${method.name}`;
}

/** `extends` depth-first, bases before the pack, each pack once (forge's order). */
function linearize(pack: FixturePack<any>): FixturePack<any>[] {
  const out: FixturePack<any>[] = [];
  const visit = (p: FixturePack<any>, visiting: FixturePack<any>[]) => {
    if (out.includes(p)) return;
    if (visiting.includes(p)) {
      throw new Error(`fixture pack "${p.name}" extends itself: ${[...visiting, p].map((v) => v.name).join(" → ")}`);
    }
    for (const base of p.extends ?? []) visit(base, [...visiting, p]);
    out.push(p);
  };
  visit(pack, []);
  return out;
}

/** The layers' extension fields, the options' own layer last. */
function extensionLayers(options: CreateFixtureTransportOptions): Pick<FixturePack<any>, "name" | "answers" | "services" | "defaults">[] {
  const layers: Pick<FixturePack<any>, "name" | "answers" | "services" | "defaults">[] = options.pack ? linearize(options.pack) : [];
  layers.push({ name: OPTIONS_LAYER, answers: options.answers, services: options.services, defaults: options.defaults });
  return layers;
}

/** The service a `defaults` entry answers: a descriptor, or a `serviceDefaults(...)` object's. */
function serviceOf(entry: FixtureDefaults): DescService {
  return (entry as { kind?: unknown }).kind === "service" ? (entry as DescService) : (entry as AnyServiceDefaults).$service;
}

/**
 * RELIANT EXTENSION (2): every `answers` entry as a method key, and a problem
 * for each string entry naming no method of a registered service.
 */
function resolveAnswers(
  layers: readonly Pick<FixturePack<any>, "name" | "answers" | "services" | "defaults">[],
  extraServices: readonly DescService[] = [],
): { answered: Set<string>; problems: FixtureProblem[] } {
  const known = new Set<string>();
  const register = (service: DescService) => service.methods.forEach((method) => known.add(keyOf(method)));
  extraServices.forEach(register);
  for (const layer of layers) {
    layer.services?.forEach(register);
    for (const entry of layer.defaults ?? []) register(serviceOf(entry));
  }
  const answered = new Set<string>();
  const problems: FixtureProblem[] = [];
  for (const layer of layers) {
    for (const entry of layer.answers ?? []) {
      if (typeof entry !== "string") answered.add(keyOf(entry));
      else if (known.has(entry)) answered.add(entry);
      else {
        problems.push({
          pack: layer.name,
          method: entry,
          message: `answers entry "${entry}" names no method of a registered service (register its service through defaults or \`services\`)`,
        });
      }
    }
  }
  return { answered, problems };
}

/**
 * RELIANT EXTENSION (2): the side-effecting RPCs a pack (and everything it
 * extends) answers from fixtures, as `"pkg.Service/Method"` keys.
 */
export function fixturePackAnswers(pack: FixturePack<any>): string[] {
  const keys = linearize(pack).flatMap((layer) => (layer.answers ?? []).map((a) => (typeof a === "string" ? a : keyOf(a))));
  return [...new Set(keys)];
}

/**
 * RELIANT EXTENSION (2): refuse, at build time, a pack that answers writes
 * on a path that can also reach live traffic (a passthrough, a fallback to a
 * real transport). Only a pure fixture transport — one that never reaches a
 * backend — may honour `answers`; elsewhere a listed write could be sent on.
 */
export function assertPureFixturePack(pack: FixturePack<any>, where: string): void {
  const answers = fixturePackAnswers(pack);
  if (answers.length > 0) {
    throw new Error(
      `${where}: fixture pack "${pack.name}" answers writes (${answers.join(", ")}), which only a pure fixture transport may do; this path can reach a live backend`,
    );
  }
}

// ── the transport ────────────────────────────────────────────────────────

/** True when the proto declares the method free of side effects (forge's test). */
function isRead(method: DescMethod): boolean {
  return method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS;
}

/** forge's refusal: the same code and default message as its own guard. */
function refusal(key: string, message: string | undefined): ConnectError {
  return new ConnectError(message ?? `fixture-transport: ${key} is not a read (no idempotency_level = NO_SIDE_EFFECTS)`, Code.PermissionDenied);
}

/** forge's answer for a method nothing in the pack answers. */
function isUnanswered(err: unknown, key: string): boolean {
  return (
    err instanceof ConnectError &&
    err.code === Code.Unimplemented &&
    err.rawMessage === `fixture-transport: no route, scenario handler or service default for ${key}`
  );
}

/**
 * A refused stream's body: its first read fails with `err`, or with the
 * caller's own cancellation when `signal` is aborted by then (what forge's
 * in-handler refusal reports). An async generator, so it is a complete async
 * iterator: an outer interceptor may also close it unread (`return()`).
 */
async function* refusedBody(err: ConnectError, signal: AbortSignal): AsyncGenerator<never> {
  yield await Promise.reject<never>(signal.aborted ? ConnectError.from(signal.reason, Code.Canceled) : err);
}

/**
 * `body` with forge's Unimplemented for `key` (nothing answers it) turned
 * into the refusal. Delegates with `yield*`, so an outer interceptor's
 * `return()` still reaches the router's stream.
 */
async function* refusingUnanswered<T>(body: AsyncIterable<T>, key: string, message: string | undefined): AsyncGenerator<T> {
  try {
    yield* body;
  } catch (err) {
    throw isUnanswered(err, key) ? refusal(key, message) : err;
  }
}

/**
 * RELIANT EXTENSION (2): the read-only guard as the innermost client
 * interceptor, for a pack that lists `answers`. A read (the proto's
 * NO_SIDE_EFFECTS, forge's test) passes; a write the pack lists in `answers` passes to whatever answers it,
 * and is refused when nothing does; every other write is refused without
 * reaching the router. Every refusal is PermissionDenied with the configured
 * `readOnlyMessage`, as forge's guard refuses. A unary refusal throws; a
 * refused stream opens and fails on its first read (with the caller's
 * cancellation instead, once cancelled), as forge's in-handler refusal does,
 * whether the guard refuses it up front or forge's Unimplemented for an
 * unanswered listed write arrives while the body is read (a server or bidi
 * stream's usually does).
 */
function readOnlyGuard(answered: ReadonlySet<string>, message: string | undefined): Interceptor {
  return (next) => async (req) => {
    const key = keyOf(req.method);
    if (isRead(req.method)) return next(req);
    if (!answered.has(key)) {
      const err = refusal(key, message);
      if (!req.stream) throw err;
      const refused: StreamResponse = {
        stream: true,
        service: req.service,
        method: req.method,
        header: new Headers(),
        trailer: new Headers(),
        message: refusedBody(err, req.signal),
      };
      return refused;
    }
    let res;
    try {
      res = await next(req);
    } catch (err) {
      throw isUnanswered(err, key) ? refusal(key, message) : err;
    }
    if (!res.stream) return res;
    const mapped: StreamResponse = { ...res, message: refusingUnanswered(res.message, key, message) };
    return mapped;
  };
}

/**
 * Create the fixture transport: forge's, plus the extensions above. See
 * forge's module notes for the dispatch.
 */
export function createFixtureTransport(options: CreateFixtureTransportOptions = {}): Transport {
  const { answers: _answers, services: _services, ...forgeOptions } = options;
  const { answered, problems } = resolveAnswers(extensionLayers(options));
  // forge composes first, so a pack's data errors come before its answers errors.
  const transport =
    answered.size === 0 || !options.readOnly
      ? forgeCreateFixtureTransport(forgeOptions)
      : forgeCreateFixtureTransport({
          ...forgeOptions,
          readOnly: false,
          interceptors: [...(options.interceptors ?? []), readOnlyGuard(answered, options.readOnlyMessage)],
        });
  const [first] = problems;
  if (first) throw new Error(`fixture pack "${first.pack}": ${first.message}`);
  return transport;
}

// ── validation ───────────────────────────────────────────────────────────

/**
 * forge's `checkFixturePack`, plus (RELIANT EXTENSION 2) a problem for every
 * `answers` entry that names no method of a registered service (the pack's
 * `defaults` and `services`, and `options.services`).
 */
export async function checkFixturePack(pack: FixturePack<any>, options: CheckFixturePackOptions): Promise<FixtureProblem[]> {
  const problems = await forgeCheckFixturePack(pack, options);
  return [...problems, ...resolveAnswers(linearize(pack), options.services).problems];
}
