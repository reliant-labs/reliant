// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The FIXTURE transport: a production-grade data seam for demos, previews,
// screenshot harnesses and tests, built on Connect's own in-memory router
// (`createRouterTransport` from @connectrpc/connect). Where the mock
// transport (./mock-transport) hands a handler's answer straight back to the
// caller, every call here crosses a real Connect hop:
//
//   - the request and the response are SERIALIZED (binary), so a fixture
//     that could not travel over the wire fails here, not in production;
//   - the caller's interceptors run, exactly as over HTTP;
//   - handlers are standard typed `ServiceImpl`s (`router.service(Svc, {...})`),
//     the same shape a Connect server implements.
//
// Dispatch, per RPC (a FIXTURE pack composes layers, see below):
//
//   0. `readOnly: true` refuses every RPC whose proto does not declare
//      `option idempotency_level = NO_SIDE_EFFECTS;` with PermissionDenied,
//      before any handler sees it. There is no name heuristic: an
//      unannotated read is refused too (fail closed), and the fix is the
//      annotation, which is right for production as well.
//   1. The method's ANSWER: a `routes` implementation or a `scenario`
//      handler. Across composed layers the LATEST layer that answers a
//      method wins, per method; within one layer a route beats a scenario
//      handler.
//   2. The service DEFAULTS (`defaults`): a deterministic, schema-valid
//      response built from the method's descriptor (mock-values.ts).
//   3. Otherwise Unimplemented, naming the method.
//
// Imported through the "@reliantlabs/forge-web-runtime/fixture-transport"
// subpath, never the barrel, so a production bundle that only lazy-loads it
// (a demo chunk) shakes it out of the main app.
import {
  create,
  fromBinary,
  fromJson,
  isMessage,
  toBinary,
  ScalarType,
  type DescField,
  type DescMessage,
  type DescMethod,
  type DescService,
  type MessageShape,
} from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import {
  Code,
  ConnectError,
  createHandlerContext,
  createRouterTransport,
  type ConnectRouter,
  type HandlerContext,
  type Interceptor,
  type ServiceImpl,
  type Transport,
} from "@connectrpc/connect";

import type { MockScenario } from "./mock-transport.js";
import {
  isServiceDefaults,
  serviceDefaults,
  type AnyServiceDefaults,
} from "./mock-values.js";

/** One JSON fixture and the message it must decode to. See {@link fixtureJson}. */
export interface FixtureJson<Desc extends DescMessage = DescMessage> {
  readonly schema: Desc;
  /** Proto3 JSON. Unknown fields are REJECTED when the pack loads. */
  readonly json: unknown;
}

/** A pack's named JSON fixtures. */
export type FixtureDataMap = Record<string, FixtureJson>;

/** The decoded messages of a {@link FixtureDataMap}, by the same keys. */
export type FixtureDataOf<D extends FixtureDataMap> = {
  readonly [K in keyof D]: D[K] extends FixtureJson<infer S>
    ? MessageShape<S>
    : never;
};

/**
 * What a pack's handlers are built with. One context serves a whole
 * composition: its clock is the transport's, its id namespace the last one
 * a layer declared, and its data every layer's (a later layer's key wins).
 */
export interface FixtureContext<D extends FixtureDataMap = FixtureDataMap> {
  /**
   * The pack clock, in ms since the epoch. A demo passes the load time, a
   * test a fixed instant: one pack, two clocks.
   */
  now(): number;
  /**
   * The reserved id namespace of the pack ("" when no layer declares one).
   * Ids a pack mints carry it, so a request naming one is recognisably a
   * fixture's.
   */
  readonly idPrefix: string;
  /** `${idPrefix}${local}`. */
  id(local: string): string;
  /** The pack's `data`, each entry decoded with strict `fromJson`. */
  readonly data: FixtureDataOf<D>;
}

/**
 * Typed `ServiceImpl` registrations, exactly as for Connect's
 * `createRouterTransport`: `(router) => router.service(Svc, impl)`. The
 * per-registration handler options are not used.
 */
export type FixtureRoutes<D extends FixtureDataMap = FixtureDataMap> = (
  router: ConnectRouter,
  ctx: FixtureContext<D>,
) => void;

/**
 * The mock transport's scenario handler map, as a whole `MockScenario` (a
 * generated `defineScenario` file) or its `handlers` alone. Keys are
 * `${serviceTypeName}/${methodName}`; a handler takes the request (an array
 * of them for a client stream) and returns the response, or for a server
 * stream an array, iterable or async iterable of responses. A scenario's
 * `passthrough` and `setup` do not apply here: a fixture transport never
 * reaches a backend, and `setup` is `resolveActiveScenario`'s to run.
 */
export type FixtureScenario = MockScenario | MockScenario["handlers"];

/**
 * A service whose unanswered RPCs get descriptor-derived defaults: the
 * service descriptor (defaults are built with the transport's clock), or a
 * prebuilt `serviceDefaults(...)` object such as a generated
 * `src/mocks/services/*_gen.ts` export.
 */
export type FixtureDefaults = DescService | AnyServiceDefaults;

/**
 * A fixture pack: one world of sample data, composable. Define one with
 * {@link defineFixturePack} and serve it with
 * `createFixtureTransport({ pack })`.
 */
export interface FixturePack<D extends FixtureDataMap = FixtureDataMap> {
  /** Names the pack in errors and in {@link checkFixturePack}'s problems. */
  readonly name: string;
  /**
   * Packs this one builds on, applied in order before it. A later layer's
   * answer for a method replaces an earlier one's, per method; everything
   * it does not answer is inherited.
   */
  // `any`: a pack composes packs over any data map; its own `ctx.data` is
  // typed by its own map.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  readonly extends?: readonly FixturePack<any>[];
  /** The reserved id namespace, e.g. "sample." — see FixtureContext.idPrefix. */
  readonly idPrefix?: string;
  /**
   * Named JSON fixtures (`fixtureJson(Schema, json)`), decoded once when the
   * transport is built and handed to handlers as `ctx.data`. A field the
   * schema does not declare fails the load: the JSON is checked against the
   * proto for free.
   */
  readonly data?: D;
  readonly routes?: FixtureRoutes<D>;
  /** A scenario map, or a function of the pack context returning one. */
  readonly scenario?:
    | FixtureScenario
    | ((ctx: FixtureContext<D>) => FixtureScenario);
  readonly defaults?: readonly FixtureDefaults[];
}

/**
 * Declare a fixture pack. An identity function that types the pack (its
 * `ctx.data` from its own `data`) and checks its name.
 */
export function defineFixturePack<D extends FixtureDataMap = FixtureDataMap>(
  pack: FixturePack<D>,
): FixturePack<D> {
  if (typeof pack.name !== "string" || pack.name === "") {
    throw new Error("defineFixturePack: a pack needs a non-empty name");
  }
  return pack;
}

/**
 * A JSON fixture for a pack's `data`: the message schema it decodes to, and
 * the JSON (typically a `data/*.json` import). Decoding happens when the
 * pack loads, with unknown fields rejected.
 */
export function fixtureJson<Desc extends DescMessage>(
  schema: Desc,
  json: unknown,
): FixtureJson<Desc> {
  return { schema, json };
}

export interface CreateFixtureTransportOptions {
  /**
   * The fixture pack to serve. `routes`, `scenario` and `defaults` given
   * here as well form one more layer on top of it (they win).
   */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  pack?: FixturePack<any>;
  /** Typed ServiceImpls: `(router) => router.service(Svc, impl)`. */
  routes?: FixtureRoutes;
  /** An existing createMockTransport scenario (or its handlers), unchanged. */
  scenario?: FixtureScenario;
  /** Services whose unanswered RPCs get descriptor-derived defaults. */
  defaults?: readonly FixtureDefaults[];
  /**
   * `true`: refuse every RPC whose proto does not declare
   * `idempotency_level = NO_SIDE_EFFECTS`, with PermissionDenied, before any
   * handler sees it. There is deliberately no name-based alternative.
   */
  readOnly?: boolean;
  /** The message a refused RPC carries. Default names the RPC. */
  readOnlyMessage?: string;
  /**
   * The pack clock (ms since the epoch); default `Date.now`. Handlers read
   * it as `ctx.now()`; defaults are anchored at its value when the
   * transport is built.
   */
  now?: () => number;
  /** The id namespace when the pack declares none. */
  idPrefix?: string;
  /** Client interceptors, run on every call as over HTTP. */
  interceptors?: Interceptor[];
}

// ── composition ──────────────────────────────────────────────────────────

/** A layer, as the composer reads it: a pack, or the options' own layer. */
interface Layer {
  name: string;
  idPrefix?: string;
  data?: FixtureDataMap;
  routes?: FixtureRoutes;
  scenario?: FixtureScenario | ((ctx: FixtureContext) => FixtureScenario);
  defaults?: readonly FixtureDefaults[];
}

type RouteImpl = (req: unknown, ctx: HandlerContext) => unknown;
type ScenarioHandler = (req: never) => unknown;

/** What answers one method, and the layer it came from. */
type Answer =
  | { kind: "route"; pack: string; impl: RouteImpl; method: DescMethod }
  | { kind: "scenario"; pack: string; handler: ScenarioHandler };

type DefaultHandler = (req: unknown) => unknown;

interface Composition {
  ctx: FixtureContext;
  answers: Map<string, Answer>;
  defaults: Map<string, DefaultHandler>;
}

/** A problem a composition hit, before any RPC ran. */
interface LoadProblem {
  pack: string;
  data?: string;
  message: string;
}

/** Apply `extends` depth-first: bases before the pack, each pack once. */
function linearize(pack: FixturePack<FixtureDataMap>): Layer[] {
  const out: Layer[] = [];
  const done = new Set<FixturePack<FixtureDataMap>>();
  const visiting: FixturePack<FixtureDataMap>[] = [];
  const visit = (p: FixturePack<FixtureDataMap>) => {
    if (done.has(p)) return;
    if (visiting.includes(p)) {
      const chain = [...visiting, p].map((v) => v.name).join(" → ");
      throw new Error(`fixture pack "${p.name}" extends itself: ${chain}`);
    }
    visiting.push(p);
    for (const base of p.extends ?? []) visit(base);
    visiting.pop();
    done.add(p);
    out.push(p as Layer);
  };
  visit(pack);
  return out;
}

function keyOf(method: DescMethod): string {
  return `${method.parent.typeName}/${method.name}`;
}

function scenarioHandlers(
  scenario: FixtureScenario,
): Record<string, ScenarioHandler | undefined> {
  const handlers = (scenario as Partial<MockScenario>).handlers;
  return typeof (scenario as Partial<MockScenario>).name === "string" &&
    typeof handlers === "object" &&
    handlers !== null
    ? handlers
    : (scenario as MockScenario["handlers"]);
}

/**
 * A ConnectRouter that records what is registered instead of serving it,
 * so a method's route can be merged with the other layers' answers.
 */
function recordingRouter(
  pack: string,
  answers: Map<string, Answer>,
): ConnectRouter {
  const router: ConnectRouter = {
    handlers: [],
    service(service, implementation) {
      for (const method of service.methods) {
        const impl = (implementation as Record<string, unknown>)[
          method.localName
        ];
        if (typeof impl !== "function") continue;
        answers.set(keyOf(method), {
          kind: "route",
          pack,
          impl: (impl as RouteImpl).bind(implementation),
          method,
        });
      }
      return router;
    },
    rpc(method, impl) {
      answers.set(keyOf(method), {
        kind: "route",
        pack,
        impl: impl as RouteImpl,
        method,
      });
      return router;
    },
  };
  return router;
}

/**
 * Compose the layers into one answer table. Data that does not decode is
 * thrown — or, with `problems`, recorded (checkFixturePack) and skipped.
 */
function compose(
  layers: readonly Layer[],
  now: () => number,
  fallbackIdPrefix: string,
  problems?: LoadProblem[],
): Composition {
  let idPrefix = fallbackIdPrefix;
  const data: Record<string, unknown> = {};
  for (const layer of layers) {
    if (layer.idPrefix !== undefined) idPrefix = layer.idPrefix;
    for (const [name, entry] of Object.entries(layer.data ?? {})) {
      try {
        data[name] = fromJson(entry.schema, entry.json as never, {
          ignoreUnknownFields: false,
        });
      } catch (err) {
        const message = `data "${name}" is not a valid ${entry.schema.typeName}: ${errorText(err)}`;
        if (!problems) {
          throw new Error(`fixture pack "${layer.name}": ${message}`);
        }
        problems.push({ pack: layer.name, data: name, message });
      }
    }
  }
  const prefix = idPrefix;
  const ctx: FixtureContext = {
    now,
    idPrefix: prefix,
    id: (local) => `${prefix}${local}`,
    data: data as FixtureContext["data"],
  };

  const answers = new Map<string, Answer>();
  const defaults = new Map<string, DefaultHandler>();
  for (const layer of layers) {
    if (layer.scenario) {
      const scenario =
        typeof layer.scenario === "function"
          ? layer.scenario(ctx)
          : layer.scenario;
      for (const [key, handler] of Object.entries(scenarioHandlers(scenario))) {
        if (handler) {
          answers.set(key, { kind: "scenario", pack: layer.name, handler });
        }
      }
    }
    // After the scenario: within one layer, a route wins.
    layer.routes?.(recordingRouter(layer.name, answers), ctx);
    for (const entry of layer.defaults ?? []) {
      const built = isServiceDefaults(entry)
        ? entry
        : serviceDefaults(entry as DescService, { now: now() });
      const service = built.$service;
      for (const method of service.methods) {
        const handler = (built as unknown as Record<string, DefaultHandler>)[
          method.localName
        ];
        if (handler) defaults.set(keyOf(method), handler);
      }
    }
  }
  return { ctx, answers, defaults };
}

function layersOf(options: CreateFixtureTransportOptions): Layer[] {
  const layers = options.pack ? linearize(options.pack) : [];
  if (options.routes || options.scenario || options.defaults) {
    layers.push({
      name: "createFixtureTransport",
      routes: options.routes,
      scenario: options.scenario,
      defaults: options.defaults,
    });
  }
  return layers;
}

// ── the transport ────────────────────────────────────────────────────────

/** True when the proto declares the method free of side effects. */
function isRead(method: DescMethod): boolean {
  return method.idempotency === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS;
}

/** A handler's stream answer (array, iterable, async iterable, promise of either, or one value). */
async function* asStream(value: unknown): AsyncIterable<unknown> {
  const resolved = await value;
  if (resolved == null) return;
  if (
    typeof (resolved as AsyncIterable<unknown>)[Symbol.asyncIterator] ===
    "function"
  ) {
    yield* resolved as AsyncIterable<unknown>;
  } else if (
    typeof (resolved as Iterable<unknown>)[Symbol.iterator] === "function" &&
    !isMessage(resolved)
  ) {
    yield* resolved as Iterable<unknown>;
  } else {
    yield resolved;
  }
}

async function collect(requests: AsyncIterable<unknown>): Promise<unknown[]> {
  const out: unknown[] = [];
  for await (const r of requests) out.push(r);
  return out;
}

/** A client stream's requests, as a scenario handler takes them (one, or an array). */
function oneOrMany(requests: unknown[]): unknown {
  return requests.length <= 1 ? requests[0] : requests;
}

/**
 * Build the `ServiceImpl` that serves every method of `service` from a
 * composition. Registered on Connect's router, so whatever answers a call
 * is serialized on the way in and out.
 */
function serviceImpl(
  service: DescService,
  comp: Composition,
  options: CreateFixtureTransportOptions,
): Record<string, unknown> {
  const impl: Record<string, unknown> = {};
  for (const method of service.methods) {
    const key = keyOf(method);
    const guard = () => {
      if (options.readOnly && !isRead(method)) {
        throw new ConnectError(
          options.readOnlyMessage ??
            `fixture-transport: ${key} is not a read (no idempotency_level = NO_SIDE_EFFECTS)`,
          Code.PermissionDenied,
        );
      }
    };
    const unimplemented = () =>
      new ConnectError(
        `fixture-transport: no route, scenario handler or service default for ${key}`,
        Code.Unimplemented,
      );
    switch (method.methodKind) {
      case "unary":
        impl[method.localName] = async (req: unknown, ctx: HandlerContext) => {
          guard();
          const answer = comp.answers.get(key);
          if (answer?.kind === "route") return answer.impl(req, ctx);
          if (answer) return answer.handler(req as never);
          const fallback = comp.defaults.get(key);
          if (fallback) return fallback(req);
          throw unimplemented();
        };
        break;
      case "server_streaming":
        impl[method.localName] = async function* (
          req: unknown,
          ctx: HandlerContext,
        ) {
          guard();
          const answer = comp.answers.get(key);
          if (answer?.kind === "route") {
            yield* asStream(answer.impl(req, ctx));
            return;
          }
          const handler = answer ? answer.handler : comp.defaults.get(key);
          if (!handler) throw unimplemented();
          yield* asStream(handler(req as never));
        };
        break;
      case "client_streaming":
        impl[method.localName] = async (
          reqs: AsyncIterable<unknown>,
          ctx: HandlerContext,
        ) => {
          guard();
          const answer = comp.answers.get(key);
          if (answer?.kind === "route") return answer.impl(reqs, ctx);
          const handler = answer ? answer.handler : comp.defaults.get(key);
          if (!handler) throw unimplemented();
          const all = await collect(reqs);
          return handler((answer ? oneOrMany(all) : all) as never);
        };
        break;
      case "bidi_streaming":
        impl[method.localName] = async function* (
          reqs: AsyncIterable<unknown>,
          ctx: HandlerContext,
        ) {
          guard();
          const answer = comp.answers.get(key);
          if (answer?.kind === "route") {
            yield* asStream(answer.impl(reqs, ctx));
            return;
          }
          const handler = answer ? answer.handler : comp.defaults.get(key);
          if (!handler) throw unimplemented();
          const all = await collect(reqs);
          yield* asStream(handler((answer ? oneOrMany(all) : all) as never));
        };
        break;
    }
  }
  return impl;
}

/**
 * Create the fixture transport. See the dispatch notes at the top of this
 * module; each service is mounted on its own in-memory Connect router the
 * first time it is called, so a scenario may answer a service the transport
 * was never told about.
 */
export function createFixtureTransport(
  options: CreateFixtureTransportOptions = {},
): Transport {
  const comp = compose(
    layersOf(options),
    options.now ?? Date.now,
    options.idPrefix ?? "",
  );
  const routers = new Map<string, Transport>();
  const routerFor = (service: DescService): Transport => {
    let t = routers.get(service.typeName);
    if (!t) {
      const impl = serviceImpl(service, comp, options);
      t = createRouterTransport(
        (router) =>
          router.service(service, impl as Partial<ServiceImpl<DescService>>),
        { transport: { interceptors: options.interceptors ?? [] } },
      );
      routers.set(service.typeName, t);
    }
    return t;
  };
  const transport: Transport = {
    unary(method, signal, timeoutMs, header, input, contextValues) {
      return routerFor(method.parent).unary(
        method,
        signal,
        timeoutMs,
        header,
        input,
        contextValues,
      );
    },
    stream(method, signal, timeoutMs, header, input, contextValues) {
      return routerFor(method.parent).stream(
        method,
        signal,
        timeoutMs,
        header,
        input,
        contextValues,
      );
    },
  };
  return transport;
}

// ── validation ───────────────────────────────────────────────────────────

/** One thing wrong with a pack, as {@link checkFixturePack} reports it. */
export interface FixtureProblem {
  /** The pack (layer) the problem is in. */
  pack: string;
  /** The method, `${serviceTypeName}/${methodName}`, when it is a handler's. */
  method?: string;
  /** The `data` key, when it is a JSON fixture's. */
  data?: string;
  message: string;
}

export interface CheckFixturePackOptions {
  /**
   * Every service the pack's scenario handlers answer. A handler for a
   * service not listed cannot be checked and is reported. (Routes carry
   * their own descriptors.)
   */
  services: readonly DescService[];
  /** The clock handlers are built with; default a fixed instant. */
  now?: () => number;
  /**
   * The request each handler is called with, by method key, as an init
   * shape. Default: the empty request.
   */
  requests?: Readonly<Record<string, unknown>>;
  /** Messages read from a stream answer before it is cut off. Default 100. */
  maxStreamMessages?: number;
}

/** 2026-01-01T00:00:00Z: checks are deterministic unless a clock is given. */
const CHECK_NOW = Date.UTC(2026, 0, 1);

/**
 * Check a fixture pack against its proto, for a project's tests:
 *
 *   - every `data` JSON decodes with strict `fromJson` (no unknown field);
 *   - every route and scenario handler key names a real method;
 *   - every handler's output (each message of a stream) names only fields
 *     its message declares — `create()` and the router would silently drop a
 *     misspelled one — and survives a toBinary/fromBinary round trip.
 *
 * Each handler is called once, with `options.requests[key]` or the empty
 * request. A handler that throws a ConnectError has answered (NotFound for
 * an unknown id is an answer); any other throw is a problem. Returns the
 * problems, empty when the pack is sound: `expect(await
 * checkFixturePack(pack, { services })).toEqual([])`.
 *
 * protovalidate rules are not evaluated (the runtime carries no CEL engine).
 */
export async function checkFixturePack(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  pack: FixturePack<any>,
  options: CheckFixturePackOptions,
): Promise<FixtureProblem[]> {
  const problems: FixtureProblem[] = [];
  const load: LoadProblem[] = [];
  const comp = compose(
    linearize(pack),
    options.now ?? (() => CHECK_NOW),
    "",
    load,
  );
  problems.push(...load);

  const methods = new Map<string, DescMethod>();
  for (const service of options.services) {
    for (const method of service.methods) methods.set(keyOf(method), method);
  }
  const limit = options.maxStreamMessages ?? 100;

  for (const [key, answer] of comp.answers) {
    const method = answer.kind === "route" ? answer.method : methods.get(key);
    const problem = (message: string) =>
      problems.push({ pack: answer.pack, method: key, message });
    if (!method) {
      const service = key.slice(0, key.lastIndexOf("/"));
      problem(
        options.services.some((s) => s.typeName === service)
          ? `no method ${key.slice(service.length + 1)} on ${service}`
          : `cannot check: ${service} is not in options.services`,
      );
      continue;
    }
    let outputs: unknown[];
    try {
      outputs = await invoke(method, answer, options.requests?.[key], limit);
    } catch (err) {
      if (err instanceof ConnectError) continue;
      problem(`the handler threw: ${errorText(err)}`);
      continue;
    }
    const streams =
      method.methodKind === "server_streaming" ||
      method.methodKind === "bidi_streaming";
    outputs.forEach((output, i) => {
      const where = streams ? `message ${i}` : "response";
      for (const message of messageProblems(method.output, output, where)) {
        problem(message);
      }
    });
  }
  return problems;
}

/** Call one answer the way the router would, and collect what it returns. */
async function invoke(
  method: DescMethod,
  answer: Answer,
  init: unknown,
  limit: number,
): Promise<unknown[]> {
  const req = create(method.input, (init ?? {}) as never);
  const clientStreams =
    method.methodKind === "client_streaming" ||
    method.methodKind === "bidi_streaming";
  const serverStreams =
    method.methodKind === "server_streaming" ||
    method.methodKind === "bidi_streaming";
  let result: unknown;
  if (answer.kind === "route") {
    const ctx = createHandlerContext({
      service: method.parent,
      method,
      protocolName: "fixture-check",
      requestMethod: "POST",
      url: `/${keyOf(method)}`,
    });
    result = answer.impl(clientStreams ? asStream([req]) : req, ctx);
  } else {
    result = answer.handler(req as never);
  }
  if (!serverStreams) return [await result];
  const out: unknown[] = [];
  for await (const m of asStream(result)) {
    out.push(m);
    if (out.length >= limit) break;
  }
  return out;
}

/** Wrapper and other well-known types take unboxed or JSON-shaped values: not walked. */
function isWellKnown(schema: DescMessage): boolean {
  return schema.typeName.startsWith("google.protobuf.");
}

/** Everything wrong with one handler output, as messages naming the field path. */
function messageProblems(
  schema: DescMessage,
  value: unknown,
  where: string,
): string[] {
  const out: string[] = [];
  shape(schema, value, where, out);
  if (out.length > 0) return out;
  try {
    const message = isMessage(value, schema)
      ? value
      : create(schema, (value ?? {}) as never);
    fromBinary(schema, toBinary(schema, message));
  } catch (err) {
    out.push(`${where} does not serialize: ${errorText(err)}`);
  }
  return out;
}

/** Walk an init shape against its schema, recording names it does not declare. */
function shape(
  schema: DescMessage,
  value: unknown,
  path: string,
  out: string[],
): void {
  if (value == null || isWellKnown(schema)) return;
  if (isMessage(value)) {
    if (value.$typeName !== schema.typeName) {
      out.push(`${path} is a ${value.$typeName}, not a ${schema.typeName}`);
    }
    return;
  }
  if (typeof value !== "object" || Array.isArray(value)) {
    out.push(`${path} is not a ${schema.typeName} (got ${typeof value})`);
    return;
  }
  for (const [name, v] of Object.entries(value)) {
    if (name === "$typeName") {
      if (v !== schema.typeName) {
        out.push(`${path} is a ${String(v)}, not a ${schema.typeName}`);
      }
      continue;
    }
    const at = `${path}.${name}`;
    const oneof = schema.oneofs.find((o) => o.localName === name);
    if (oneof) {
      if (v == null) continue;
      const { case: kase, value: inner } = v as {
        case?: string;
        value?: unknown;
      };
      if (kase === undefined) continue;
      const member = oneof.fields.find((f) => f.localName === kase);
      if (!member) {
        out.push(`${at}: ${String(kase)} is not a case of oneof ${oneof.name}`);
      } else if (member.fieldKind === "message") {
        shape(member.message, inner, `${at}.${kase}`, out);
      } else if (inner != null) {
        single(member, inner, `${at}.${kase}`, out);
      }
      continue;
    }
    const field = schema.field[name];
    if (!field) {
      out.push(`${at}: ${schema.typeName} has no field "${name}"`);
      continue;
    }
    if (field.oneof) {
      out.push(
        `${at}: "${name}" is a case of oneof ${field.oneof.name}; set it as ${field.oneof.localName}: { case: "${name}", value }`,
      );
      continue;
    }
    if (v == null) continue;
    switch (field.fieldKind) {
      case "message":
        shape(field.message, v, at, out);
        break;
      case "scalar":
      case "enum":
        single(field, v, at, out);
        break;
      case "list":
        if (!Array.isArray(v)) {
          out.push(`${at}: a repeated field takes an array`);
          break;
        }
        v.forEach((item, i) =>
          field.listKind === "message"
            ? shape(field.message, item, `${at}[${i}]`, out)
            : single(field, item, `${at}[${i}]`, out),
        );
        break;
      case "map":
        for (const [k, item] of Object.entries(v as object)) {
          if (field.mapKind === "message") {
            shape(field.message, item, `${at}[${k}]`, out);
          } else {
            single(field, item, `${at}[${k}]`, out);
          }
        }
        break;
    }
  }
}

/**
 * A scalar or enum value of the JS type protobuf-es expects. `create()`
 * stores a wrong one as given and `toBinary` coerces some of them (a number
 * into a string field encodes as its digits), so the type is checked here.
 */
function single(field: DescField, v: unknown, at: string, out: string[]) {
  const scalar = scalarOf(field);
  if (scalar === undefined) return;
  const want = expectedType(scalar);
  const got = v instanceof Uint8Array ? "Uint8Array" : typeof v;
  if (!want.includes(got)) {
    out.push(`${at}: expected ${want.join(" or ")}, got ${got}`);
  }
}

/** The scalar type of a field's (or its elements') values; an enum is an int32. */
function scalarOf(field: DescField): ScalarType | undefined {
  switch (field.fieldKind) {
    case "scalar":
      return field.scalar;
    case "enum":
      return ScalarType.INT32;
    case "list":
      if (field.listKind === "scalar") return field.scalar;
      return field.listKind === "enum" ? ScalarType.INT32 : undefined;
    case "map":
      if (field.mapKind === "scalar") return field.scalar;
      return field.mapKind === "enum" ? ScalarType.INT32 : undefined;
    default:
      return undefined;
  }
}

function expectedType(scalar: ScalarType): string[] {
  switch (scalar) {
    case ScalarType.STRING:
      return ["string"];
    case ScalarType.BOOL:
      return ["boolean"];
    case ScalarType.BYTES:
      return ["Uint8Array"];
    case ScalarType.INT64:
    case ScalarType.UINT64:
    case ScalarType.SINT64:
    case ScalarType.FIXED64:
    case ScalarType.SFIXED64:
      // bigint, or a string under jstype=JS_STRING; toBinary parses a number.
      return ["bigint", "string", "number"];
    default:
      return ["number"];
  }
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
