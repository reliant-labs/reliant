// Copyright (c) 2025 Reliant Labs

/**
 * The fixture transport's contract (forge#2716's, which transport.ts serves):
 * every answer crosses Connect's own in-memory router (serialized,
 * interceptors run), the dispatch ladder (readOnly guard → latest layer's
 * route or scenario handler → service defaults → Unimplemented), pack
 * composition, strict JSON data and checkFixturePack. Then the reliant
 * extension (`answers`) and the harness
 * policy in overlay.ts (withEmptyReads, pending, fail).
 *
 * Schemas are real generated services: StorageBrowserService has NO_SIDE_EFFECTS
 * reads (ListBrowsableBuckets) and one write (PresignObjectDownload);
 * DeployService has annotated reads (GetTenant) and writes (CreateTenant);
 * UserService/GetCurrentUser is an unannotated read (it writes on read);
 * ToolsDaemonService/ConnectDaemon is an unannotated stream;
 * StreamingService/StreamUserUpdates is an annotated server stream.
 */

import { create, createFileRegistry, isMessage, type DescMessage } from "@bufbuild/protobuf";
import type { GenService } from "@bufbuild/protobuf/codegenv2";
import { FileDescriptorProtoSchema, MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient, type Interceptor, type StreamResponse, type Transport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { ListObjectsRequestSchema, ListObjectsResponseSchema, StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { ToolsDaemonService } from "@/gen/reliant/v1/tools_daemon_pb";

import { fail, pending, withEmptyReads } from "../overlay";
import { assertPureFixturePack, checkFixturePack, createFixtureTransport, defineFixturePack, fixtureJson, fixturePackAnswers, type FixtureContext } from "../transport";

const FIXED_NOW = Date.parse("2026-11-18T15:30:00Z");
const LIST = `${StorageBrowserService.typeName}/ListObjects`;
const BUCKETS = `${StorageBrowserService.typeName}/ListBrowsableBuckets`;
const PRESIGN = `${StorageBrowserService.typeName}/PresignObjectDownload`;
const CREATE_TENANT = `${DeployService.typeName}/CreateTenant`;
const USER_UPDATES = `${StreamingService.typeName}/StreamUserUpdates`;
const CONNECT_DAEMON = `${ToolsDaemonService.typeName}/ConnectDaemon`;
const MESSAGE = "Changes are turned off.";

const storage = (t: Transport) => createClient(StorageBrowserService, t);
const deploy = (t: Transport) => createClient(DeployService, t);
const streaming = (t: Transport) => createClient(StreamingService, t);

/**
 * A server stream that declares no idempotency, so `readOnly: true` takes it
 * for a write. Every generated server stream is annotated NO_SIDE_EFFECTS, so
 * the tests that need an unannotated one build it here.
 */
const TickService = createFileRegistry(
  create(FileDescriptorProtoSchema, {
    name: "fixture_test/v1/tick.proto",
    package: "fixture_test.v1",
    syntax: "proto3",
    messageType: [{ name: "TickRequest" }, { name: "Tick" }],
    service: [{ name: "TickService", method: [{ name: "Ticks", inputType: ".fixture_test.v1.TickRequest", outputType: ".fixture_test.v1.Tick", serverStreaming: true }] }],
  }),
  () => undefined,
).getService("fixture_test.v1.TickService") as GenService<{ ticks: { methodKind: "server_streaming"; input: DescMessage; output: DescMessage } }>;
const TICKS = `${TickService.typeName}/Ticks`;
const ticks = (t: Transport) => createClient(TickService, t).ticks({});
/** Options that list a write in `answers`, so the extension's guard (not forge's) is the one that refuses. */
const EXTENSION_PATH = { services: [StorageBrowserService], answers: [PRESIGN], scenario: { [PRESIGN]: () => ({}) } };

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (err) {
    return ConnectError.from(err).code;
  }
}

async function drain<T>(stream: AsyncIterable<T>): Promise<T[]> {
  const out: T[] = [];
  for await (const m of stream) out.push(m);
  return out;
}

/**
 * Abort with a Canceled reason. (A bare abort() raises jsdom's DOMException,
 * which Connect does not recognise across realms and reports as Unknown; a
 * browser's reports Canceled.)
 */
const cancel = (abort: AbortController) => abort.abort(new ConnectError("the caller went away", Code.Canceled));

/** Whether `p` is still unsettled after a few macrotasks. */
async function stillPending(p: Promise<unknown>): Promise<boolean> {
  const marker = Symbol("pending");
  return (await Promise.race([p.then(() => null, () => null), new Promise((r) => setTimeout(() => r(marker), 30))])) === marker;
}

it("the schemas say what these tests assume", () => {
  const NO_SIDE_EFFECTS = MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS;
  // ListBrowsableBuckets writes no audit row; ListObjects does, so control-plane#3523 unmarked it.
  expect(StorageBrowserService.method.listBrowsableBuckets.idempotency).toBe(NO_SIDE_EFFECTS);
  expect(StorageBrowserService.method.presignObjectDownload.idempotency).not.toBe(NO_SIDE_EFFECTS);
  expect(DeployService.method.getTenant.idempotency).toBe(NO_SIDE_EFFECTS);
  expect(UserService.method.getCurrentUser.idempotency).not.toBe(NO_SIDE_EFFECTS);
  expect(ToolsDaemonService.method.connectDaemon.idempotency).not.toBe(NO_SIDE_EFFECTS);
  expect(StreamingService.method.streamUserUpdates.idempotency).toBe(NO_SIDE_EFFECTS);
});

describe("createFixtureTransport: routes", () => {
  it("serves a typed route through a real serialization round trip, with the pack context", async () => {
    const seen: unknown[] = [];
    const transport = createFixtureTransport({
      now: () => FIXED_NOW,
      routes: (router, ctx) =>
        router.service(StorageBrowserService, {
          listObjects(req) {
            seen.push(req);
            return { objects: [{ key: `${req.bucket}/a`, size: 3n, updated: { seconds: BigInt(ctx.now() / 1000) } }] };
          },
        }),
    });
    const res = await storage(transport).listObjects({ bucket: "b" });
    expect(res.objects.map((o) => [o.key, o.size])).toEqual([["b/a", 3n]]);
    expect(Number(res.objects[0]?.updated?.seconds) * 1000).toBe(FIXED_NOW);
    // The handler got a decoded message, not the caller's object.
    expect(isMessage(seen[0], ListObjectsRequestSchema)).toBe(true);
  });

  it("fails an answer that cannot be serialized, here rather than after the swap", async () => {
    const transport = createFixtureTransport({
      routes: (router) => router.service(StorageBrowserService, { presignObjectDownload: () => ({ url: "u", expiresAt: { seconds: 1n, nanos: 0.5 } }) }),
    });
    await expect(storage(transport).presignObjectDownload({})).rejects.toThrow(/nanos/);
  });

  it("runs the caller's interceptors on every call, refused ones included", async () => {
    const calls: string[] = [];
    const tag: Interceptor = (next) => async (req) => {
      calls.push(req.method.name);
      req.header.set("x-fixture", "yes");
      return next(req);
    };
    let header: string | null = null;
    const transport = createFixtureTransport({
      interceptors: [tag],
      readOnly: true,
      routes: (router) =>
        router.service(StorageBrowserService, {
          listBrowsableBuckets(_req, ctx) {
            header = ctx.requestHeader.get("x-fixture");
            return {};
          },
        }),
    });
    await storage(transport).listBrowsableBuckets({});
    expect(await code(storage(transport).presignObjectDownload({}))).toBe(Code.PermissionDenied);
    expect(calls).toEqual(["ListBrowsableBuckets", "PresignObjectDownload"]);
    expect(header).toBe("yes");
  });
});

describe("createFixtureTransport: server streams stay open until aborted", () => {
  it("through a route, which sees the abort on ctx.signal", async () => {
    let aborted = false;
    const transport = createFixtureTransport({
      routes: (router) =>
        router.service(StreamingService, {
          async *streamUserUpdates(_req, ctx) {
            yield { event: { case: "heartbeat", value: {} } };
            ctx.signal.addEventListener("abort", () => (aborted = true));
            await pending(ctx.signal);
          },
        }),
    });
    const abort = new AbortController();
    const it = streaming(transport).streamUserUpdates({}, { signal: abort.signal })[Symbol.asyncIterator]();
    expect((await it.next()).value?.event.case).toBe("heartbeat");
    const next = it.next();
    expect(await stillPending(next)).toBe(true);
    cancel(abort);
    expect(await code(next)).toBe(Code.Canceled);
    await vi.waitFor(() => expect(aborted).toBe(true));
  });

  it("through a scenario handler", async () => {
    const transport = createFixtureTransport({
      scenario: {
        [USER_UPDATES]: async function* () {
          yield { event: { case: "heartbeat", value: {} } };
          await new Promise(() => {});
        },
      },
    });
    const abort = new AbortController();
    const it = streaming(transport).streamUserUpdates({}, { signal: abort.signal })[Symbol.asyncIterator]();
    expect((await it.next()).value?.event.case).toBe("heartbeat");
    const next = it.next();
    expect(await stillPending(next)).toBe(true);
    cancel(abort);
    expect(await code(next)).toBe(Code.Canceled);
  });

  it("and a finite stream simply ends", async () => {
    const transport = createFixtureTransport({ scenario: { [USER_UPDATES]: () => [{ event: { case: "heartbeat", value: {} } }, {}] } });
    expect((await drain(streaming(transport).streamUserUpdates({}))).length).toBe(2);
  });
});

describe("createFixtureTransport: bidi routes", () => {
  it("responds before requests end and propagates cancellation to the route context", async () => {
    let releaseRequests!: () => void;
    const requestsDone = new Promise<void>((resolve) => { releaseRequests = resolve; });
    let observeAbort!: () => void;
    const aborted = new Promise<void>((resolve) => { observeAbort = resolve; });
    const transport = createFixtureTransport({
      routes: (router) => router.service(ToolsDaemonService, {
        async *connectDaemon(requests, ctx) {
          ctx.signal.addEventListener("abort", observeAbort, { once: true });
          for await (const request of requests) {
            expect(isMessage(request, ToolsDaemonService.method.connectDaemon.input)).toBe(true);
            yield {};
            await pending(ctx.signal);
          }
        },
      }),
    });
    async function* requests() {
      yield {};
      await requestsDone;
    }
    const abort = new AbortController();
    const iterator = createClient(ToolsDaemonService, transport)
      .connectDaemon(requests(), { signal: abort.signal })[Symbol.asyncIterator]();
    try {
      // This cannot resolve if route input is collected before dispatch.
      expect((await iterator.next()).done).toBe(false);
      const next = iterator.next();
      cancel(abort);
      expect(await code(next)).toBe(Code.Canceled);
      await aborted;
    } finally {
      cancel(abort);
      releaseRequests();
    }
  });
});

describe("createFixtureTransport: scenario and defaults", () => {
  it("accepts a createMockTransport scenario unchanged, and a bare handler map", async () => {
    const whole = createFixtureTransport({ scenario: { name: "existing", handlers: { [LIST]: (req: { bucket: string }) => ({ prefixes: [req.bucket] }) } } });
    expect((await storage(whole).listObjects({ bucket: "x" })).prefixes).toEqual(["x"]);
    const bare = createFixtureTransport({ scenario: { [LIST]: () => create(ListObjectsResponseSchema, { prefixes: ["bare"] }) } });
    expect((await storage(bare).listObjects({})).prefixes).toEqual(["bare"]);
  });

  it("within one layer, a route beats a scenario handler", async () => {
    const transport = createFixtureTransport({
      scenario: { [LIST]: () => ({ prefixes: ["scenario"] }) },
      routes: (router) => router.service(StorageBrowserService, { listObjects: () => ({ prefixes: ["route"] }) }),
    });
    expect((await storage(transport).listObjects({})).prefixes).toEqual(["route"]);
  });

  it("answers everything else from the service defaults, then Unimplemented", async () => {
    const transport = createFixtureTransport({ defaults: [StorageBrowserService], now: () => FIXED_NOW });
    expect(Array.isArray((await storage(transport).listBrowsableBuckets({})).buckets)).toBe(true);
    expect(await code(deploy(transport).getTenant({}))).toBe(Code.Unimplemented);
  });
});

describe("createFixtureTransport: readOnly", () => {
  it("true: passes an annotated read and refuses everything else, before any handler", async () => {
    const write = vi.fn(() => ({}));
    const transport = createFixtureTransport({
      readOnly: true,
      readOnlyMessage: MESSAGE,
      scenario: { [BUCKETS]: () => ({ buckets: [{ name: "p" }] }), [PRESIGN]: write, [`${ToolsDaemonService.typeName}/ConnectDaemon`]: () => [{}] },
    });
    expect((await storage(transport).listBrowsableBuckets({})).buckets[0]?.name).toBe("p");
    const refused = await storage(transport).presignObjectDownload({}).catch((e: unknown) => e);
    expect(ConnectError.from(refused).code).toBe(Code.PermissionDenied);
    expect(ConnectError.from(refused).rawMessage).toBe(MESSAGE);
    expect(write).not.toHaveBeenCalled();
    // An unannotated stream is refused too: no name heuristic.
    expect(await code(drain(createClient(ToolsDaemonService, transport).connectDaemon((async function* () {})())))).toBe(Code.PermissionDenied);
  });

  it("true: an unannotated read is refused whatever its verb (no name heuristic)", async () => {
    const transport = createFixtureTransport({ defaults: [DeployService, UserService], readOnly: true, readOnlyMessage: MESSAGE });
    await expect(deploy(transport).getTenant({})).resolves.toBeDefined();
    await expect(createClient(UserService, transport).getCurrentUser({})).rejects.toSatisfy((e) => e instanceof ConnectError && e.code === Code.PermissionDenied && e.rawMessage === MESSAGE);
    await expect(deploy(transport).createTenant({})).rejects.toSatisfy((e) => e instanceof ConnectError && e.code === Code.PermissionDenied && e.rawMessage === MESSAGE);
  });

  it("refuses a stream the way forge's own guard does, on the extension's path too: the call opens, its first read fails", async () => {
    const connect = ToolsDaemonService.method.connectDaemon;
    const scenario = { [`${ToolsDaemonService.typeName}/ConnectDaemon`]: () => [{}] };
    const requests = async function* () {
      yield create(connect.input);
    };
    // forge's guard (no answers), then the extension's (a pack that lists some).
    for (const transport of [createFixtureTransport({ readOnly: true, scenario }), createFixtureTransport({ readOnly: true, ...EXTENSION_PATH, scenario: { ...EXTENSION_PATH.scenario, ...scenario } })]) {
      const res = await transport.stream(connect, new AbortController().signal, undefined, undefined, requests(), undefined);
      expect(await code(drain(res.message))).toBe(Code.PermissionDenied);
    }
  });

  it("a refused stream is a well-formed stream response, and honours the caller's cancellation", async () => {
    const connect = ToolsDaemonService.method.connectDaemon;
    let seen: StreamResponse | undefined;
    // A caller interceptor sits outside the guard, so it sees the guard's own response.
    const capture: Interceptor = (next) => async (req) => {
      const res = await next(req);
      if (res.stream) seen = res;
      return res;
    };
    const transport = createFixtureTransport({ readOnly: true, readOnlyMessage: MESSAGE, interceptors: [capture], ...EXTENSION_PATH, scenario: { ...EXTENSION_PATH.scenario, [CONNECT_DAEMON]: () => [{}] } });
    const open = (signal = new AbortController().signal) => transport.stream(connect, signal, undefined, undefined, (async function* () {})(), undefined);
    await open();
    expect(seen?.stream).toBe(true);
    expect(seen?.header).toBeInstanceOf(Headers);
    expect(seen?.trailer).toBeInstanceOf(Headers);
    // A complete async iterator: an interceptor may close the body unread.
    const body = seen!.message[Symbol.asyncIterator]();
    await expect(body.return!()).resolves.toEqual({ done: true, value: undefined });
    await expect(body.next()).resolves.toEqual({ done: true, value: undefined });
    // Read, it fails with the configured refusal.
    const refused = ConnectError.from(await drain((await open()).message).catch((e: unknown) => e));
    expect([refused.code, refused.rawMessage]).toEqual([Code.PermissionDenied, MESSAGE]);
    // Cancelled before it is read, the caller sees its own cancellation, as on forge's own guard.
    const native = createFixtureTransport({ readOnly: true, readOnlyMessage: MESSAGE, scenario: { [CONNECT_DAEMON]: () => [{}] } });
    for (const t of [transport, native]) {
      const abort = new AbortController();
      const res = await t.stream(connect, abort.signal, undefined, undefined, (async function* () {})(), undefined);
      cancel(abort);
      const cancelled = ConnectError.from(await drain(res.message).catch((e: unknown) => e));
      expect([cancelled.code, cancelled.rawMessage]).toEqual([Code.Canceled, "the caller went away"]);
    }
  });
});

describe("fixture packs", () => {
  const base = defineFixturePack({
    name: "base",
    idPrefix: "sample.",
    scenario: (ctx) => ({ [LIST]: () => ({ prefixes: [ctx.id("p1")] }), [BUCKETS]: () => ({ buckets: [{ name: "base" }] }) }),
  });

  it("composes extends: the later layer wins per method, the rest is inherited, the options' layer on top", async () => {
    const child = defineFixturePack({
      name: "child",
      extends: [base],
      routes: (router) => router.service(StorageBrowserService, { listBrowsableBuckets: () => ({ buckets: [{ name: "child" }] }) }),
    });
    const c = storage(createFixtureTransport({ pack: child }));
    expect((await c.listBrowsableBuckets({})).buckets[0]?.name).toBe("child");
    expect((await c.listObjects({})).prefixes).toEqual(["sample.p1"]);
    const top = storage(createFixtureTransport({ pack: child, scenario: { [BUCKETS]: () => ({ buckets: [{ name: "options" }] }) } }));
    expect((await top.listBrowsableBuckets({})).buckets[0]?.name).toBe("options");
  });

  it("hands handlers the transport's clock and the pack's id namespace", async () => {
    const seen: FixtureContext[] = [];
    const pack = defineFixturePack({ name: "ctx", extends: [base], routes: (_router, ctx) => void seen.push(ctx) });
    createFixtureTransport({ pack, now: () => FIXED_NOW });
    expect(seen[0]?.now()).toBe(FIXED_NOW);
    expect(seen[0]?.id("x")).toBe("sample.x");
  });

  it("refuses a pack that extends itself", () => {
    const a = { name: "a", extends: [] as never[] };
    const b = defineFixturePack({ name: "b", extends: [a] });
    (a.extends as unknown[]).push(b);
    expect(() => createFixtureTransport({ pack: b })).toThrow(/extends itself/);
    expect(() => defineFixturePack({ name: "" })).toThrow(/non-empty name/);
  });

  it("decodes data with strict fromJson and hands it to handlers", async () => {
    const pack = defineFixturePack({
      name: "data",
      data: { objects: fixtureJson(ListObjectsResponseSchema, { objects: [{ key: "j1", size: "4", updated: "2026-11-18T15:30:00Z" }] }) },
      routes: (router, ctx) => router.service(StorageBrowserService, { listObjects: () => ctx.data.objects }),
    });
    const res = await storage(createFixtureTransport({ pack })).listObjects({});
    expect(res.objects[0]?.size).toBe(4n);
    expect(Number(res.objects[0]?.updated?.seconds) * 1000).toBe(FIXED_NOW);
  });

  it("rejects data JSON with a field the proto does not declare, naming the pack, the key and the field", () => {
    const pack = defineFixturePack({ name: "drifted", data: { objects: fixtureJson(ListObjectsResponseSchema, { objects: [{ key: "j1", sizeBytes: 4 }] }) } });
    expect(() => createFixtureTransport({ pack })).toThrow(/fixture pack "drifted": data "objects" is not a valid controlplane\.v1\.ListObjectsResponse.*sizeBytes/);
  });
});

describe("answers: writes a pack answers from fixtures anyway (reliant extension)", () => {
  const presign = { [PRESIGN]: () => ({ url: "https://fixture.invalid/a" }) };

  it("lets a listed write through to its handler, and still refuses an unlisted one", async () => {
    const pack = defineFixturePack({ name: "app", scenario: presign, defaults: [DeployService], services: [StorageBrowserService], answers: [PRESIGN] });
    const transport = createFixtureTransport({ pack, readOnly: true, readOnlyMessage: MESSAGE });
    expect((await storage(transport).presignObjectDownload({})).url).toBe("https://fixture.invalid/a");
    expect(await code(deploy(transport).createTenant({}))).toBe(Code.PermissionDenied);
  });

  it("refuses a listed write nothing answers", async () => {
    const pack = defineFixturePack({ name: "app", services: [DeployService, TickService], answers: [CREATE_TENANT, TICKS] });
    const transport = createFixtureTransport({ pack, readOnly: true });
    expect(await code(deploy(transport).createTenant({}))).toBe(Code.PermissionDenied);
    // A stream too, when its first read fails (forge answers it Unimplemented).
    expect(await code(drain(ticks(transport)))).toBe(Code.PermissionDenied);
  });

  it("refuses a listed unary write nothing answers with the configured message", async () => {
    const pack = defineFixturePack({ name: "app", services: [DeployService], answers: [CREATE_TENANT] });
    const refused = ConnectError.from(await deploy(createFixtureTransport({ pack, readOnly: true, readOnlyMessage: MESSAGE })).createTenant({}).catch((e: unknown) => e));
    expect([refused.code, refused.rawMessage]).toEqual([Code.PermissionDenied, MESSAGE]);
    // Unconfigured, it carries forge's own refusal message.
    const plain = ConnectError.from(await deploy(createFixtureTransport({ pack, readOnly: true })).createTenant({}).catch((e: unknown) => e));
    expect(plain.rawMessage).toBe(`fixture-transport: ${CREATE_TENANT} is not a read (no idempotency_level = NO_SIDE_EFFECTS)`);
  });

  it("refuses a listed streamed write nothing answers with the configured message: the call opens, reading it fails", async () => {
    const pack = defineFixturePack({ name: "app", services: [TickService, ToolsDaemonService], answers: [TICKS, CONNECT_DAEMON] });
    const requests = async function* () {
      yield create(ToolsDaemonService.method.connectDaemon.input);
    };
    const cases: { name: string; transport: Transport; read: (t: Transport) => AsyncIterable<unknown> }[] = [
      // A server stream and a bidi stream, both unannotated, so writes to readOnly: true.
      { name: "server stream", transport: createFixtureTransport({ pack, readOnly: true, readOnlyMessage: MESSAGE }), read: ticks },
      { name: "bidi", transport: createFixtureTransport({ pack, readOnly: true, readOnlyMessage: MESSAGE }), read: (t) => createClient(ToolsDaemonService, t).connectDaemon(requests()) },
    ];
    for (const { name, transport, read } of cases) {
      const refused = ConnectError.from(await drain(read(transport)).catch((e: unknown) => e));
      expect([name, refused.code, refused.rawMessage]).toEqual([name, Code.PermissionDenied, MESSAGE]);
    }
  });

  it("a listed streamed write that is answered passes the caller's cancellation through to its route", async () => {
    let aborted = false;
    const pack = defineFixturePack({
      name: "app",
      services: [TickService],
      answers: [TICKS],
      routes: (router) =>
        router.service(TickService, {
          async *ticks(_req, ctx) {
            yield {};
            ctx.signal.addEventListener("abort", () => (aborted = true));
            await pending(ctx.signal);
          },
        }),
    });
    const abort = new AbortController();
    const it = createClient(TickService, createFixtureTransport({ pack, readOnly: true })).ticks({}, { signal: abort.signal })[Symbol.asyncIterator]();
    expect((await it.next()).done).toBe(false);
    const next = it.next();
    expect(await stillPending(next)).toBe(true);
    cancel(abort);
    expect(await code(next)).toBe(Code.Canceled);
    await vi.waitFor(() => expect(aborted).toBe(true));
  });

  it("is unioned across extends, and takes a descriptor too", async () => {
    const base = defineFixturePack({ name: "base", scenario: presign, defaults: [DeployService], answers: [StorageBrowserService.method.presignObjectDownload] });
    const child = defineFixturePack({ name: "child", extends: [base], answers: [CREATE_TENANT] });
    expect(fixturePackAnswers(child)).toEqual([PRESIGN, CREATE_TENANT]);
    const transport = createFixtureTransport({ pack: child, readOnly: true });
    await expect(storage(transport).presignObjectDownload({})).resolves.toBeDefined();
    await expect(deploy(transport).createTenant({})).resolves.toBeDefined();
  });

  it("refuses, at build, an entry that names no method of a registered service", () => {
    const typo = defineFixturePack({ name: "typo", scenario: presign, defaults: [StorageBrowserService], answers: [`${StorageBrowserService.typeName}/PresignObjectDownlaod`] });
    expect(() => createFixtureTransport({ pack: typo, readOnly: true })).toThrow(/fixture pack "typo": answers entry "controlplane\.v1\.StorageBrowserService\/PresignObjectDownlaod" names no method/);
    // A scenario key alone registers nothing: its service must be declared.
    const unregistered = defineFixturePack({ name: "bare", scenario: presign, answers: [PRESIGN] });
    expect(() => createFixtureTransport({ pack: unregistered })).toThrow(/answers entry/);
    expect(() => createFixtureTransport({ pack: unregistered, services: [StorageBrowserService] })).not.toThrow();
  });

  it("is refused on any path that can reach live traffic", () => {
    const app = defineFixturePack({ name: "app", extends: [defineFixturePack({ name: "layer", services: [DeployService], answers: [CREATE_TENANT] })] });
    expect(() => assertPureFixturePack(app, "somewhere({ passthrough })")).toThrow(/somewhere\(\{ passthrough \}\): fixture pack "app" answers writes \(controlplane\.v1\.DeployService\/CreateTenant\)/);
    expect(() => assertPureFixturePack(defineFixturePack({ name: "pure" }), "x")).not.toThrow();
  });
});

describe("checkFixturePack", () => {
  const services = [StorageBrowserService, StreamingService];

  it("finds nothing wrong with a sound pack", async () => {
    const pack = defineFixturePack({
      name: "sound",
      data: { objects: fixtureJson(ListObjectsResponseSchema, { prefixes: ["a"] }) },
      scenario: {
        [BUCKETS]: () => {
          throw new ConnectError("no such bucket", Code.NotFound);
        },
        [USER_UPDATES]: () => [{ event: { case: "heartbeat", value: {} } }],
      },
      routes: (router, ctx) => router.service(StorageBrowserService, { listObjects: () => ctx.data.objects }),
      answers: [PRESIGN],
    });
    expect(await checkFixturePack(pack, { services })).toEqual([]);
  });

  it("names the method of each bad handler, and a bad answers entry", async () => {
    const pack = defineFixturePack({
      name: "bad",
      scenario: {
        [LIST]: () => ({ objects: [{ kee: "x" }] }),
        [USER_UPDATES]: () => [{}, { event: { case: "nope", value: {} } }],
        [`${StorageBrowserService.typeName}/ListObjets`]: () => ({}),
      },
      answers: ["controlplane.v1.StorageBrowserService/Presign"],
    });
    const problems = await checkFixturePack(pack, { services });
    const byMethod = (m: string) => problems.filter((p) => p.method === m).map((p) => p.message);
    expect(byMethod(LIST)).toEqual(['response.objects[0].kee: controlplane.v1.StorageObject has no field "kee"']);
    expect(byMethod(USER_UPDATES)).toEqual(["message 1.event: nope is not a case of oneof event"]);
    expect(byMethod(`${StorageBrowserService.typeName}/ListObjets`)).toEqual(["no method ListObjets on controlplane.v1.StorageBrowserService"]);
    expect(byMethod("controlplane.v1.StorageBrowserService/Presign")[0]).toMatch(/names no method/);
  });
});

describe("overlay.ts (harness policy)", () => {
  it("withEmptyReads: an unanswered read answers the empty response; off by default", async () => {
    const pack = defineFixturePack({ name: "p", scenario: { [BUCKETS]: () => ({ buckets: [{ name: "pack" }] }) } });
    expect(await code(deploy(createFixtureTransport({ pack, readOnly: true })).getTenant({}))).toBe(Code.Unimplemented);

    const transport = createFixtureTransport(withEmptyReads({ pack, readOnly: true, readOnlyMessage: MESSAGE }, [StorageBrowserService, DeployService, StreamingService]));
    expect(await deploy(transport).getTenant({})).toEqual(create(DeployService.method.getTenant.output));
    // The pack still wins, and a write is still refused, never answered empty.
    expect((await storage(transport).listBrowsableBuckets({})).buckets[0]?.name).toBe("pack");
    expect(await code(storage(transport).presignObjectDownload({}))).toBe(Code.PermissionDenied);
    // An unanswered stream read answers one empty message, then is open and quiet until aborted.
    const abort = new AbortController();
    const it = streaming(transport).streamUserUpdates({}, { signal: abort.signal })[Symbol.asyncIterator]();
    expect((await it.next()).value?.event.case).toBeUndefined();
    const next = it.next();
    expect(await stillPending(next)).toBe(true);
    cancel(abort);
    expect(await code(next)).toBe(Code.Canceled);
  });

  it("withEmptyReads: an unanswered stream read sends its header, so it is open rather than in flight", async () => {
    const transport = createFixtureTransport(withEmptyReads({ readOnly: true }, [StreamingService]));
    const abort = new AbortController();
    const onHeader = vi.fn();
    const it = streaming(transport).streamUserUpdates({}, { signal: abort.signal, onHeader })[Symbol.asyncIterator]();
    const first = it.next();
    expect(await stillPending(first)).toBe(false);
    expect(onHeader).toHaveBeenCalledTimes(1);
    // The same header arrives on the raw transport: the call settles as an open stream.
    const raw = transport.stream(StreamingService.method.streamUserUpdates, abort.signal, undefined, undefined, (async function* () {
      yield create(StreamingService.method.streamUserUpdates.input);
    })(), undefined);
    expect(await stillPending(raw)).toBe(false);
    expect((await raw).header).toBeInstanceOf(Headers);
    cancel(abort);
  });

  it("withEmptyReads: the pack's own defaults win over the empty answer", async () => {
    const pack = defineFixturePack({ name: "p", defaults: [StorageBrowserService] });
    const transport = createFixtureTransport(withEmptyReads({ pack, readOnly: true, now: () => FIXED_NOW }, [StorageBrowserService]));
    expect((await storage(transport).listBrowsableBuckets({})).buckets.length).toBeGreaterThan(0);
  });

  it("fail answers with the code; pending stays in flight until aborted", async () => {
    const transport = createFixtureTransport({
      scenario: { [BUCKETS]: fail(Code.Unimplemented) },
      routes: (router) => router.service(StorageBrowserService, { listObjects: (_req, ctx) => pending(ctx.signal) }),
    });
    expect(await code(storage(transport).listBrowsableBuckets({}))).toBe(Code.Unimplemented);
    const abort = new AbortController();
    const call = storage(transport).listObjects({}, { signal: abort.signal });
    expect(await stillPending(call)).toBe(true);
    cancel(abort);
    expect(await code(call)).toBe(Code.Canceled);
  });
});
