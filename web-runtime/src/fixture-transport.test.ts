// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The fixture transport's contract: every answer crosses Connect's own
// in-memory router (serialized, interceptors run), the dispatch ladder
// (readOnly guard → latest layer's route or scenario handler → service
// defaults → Unimplemented), pack composition, strict JSON data, and the
// validation helper a project's tests call.
//
// The schemas are real protoc-gen-es output (testdata/mockgen): TelemetryService
// has two NO_SIDE_EFFECTS reads (ListTraces, GetTrace), an unannotated unary
// (GetRuleShowcase) and one rpc of each stream kind.
import { create, isMessage, toBinary } from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  createClient,
  type Interceptor,
  type Transport,
} from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import {
  checkFixturePack,
  createFixtureTransport,
  defineFixturePack,
  fixtureJson,
  type FixtureContext,
} from "./fixture-transport.js";
import type { MockScenario } from "./mock-transport.js";
import { serviceDefaults } from "./mock-values.js";
import { BillingService } from "./testdata/mockgen/gen/acme/v1/billing_pb.js";
import {
  GetTraceResponseSchema,
  ListTracesRequestSchema,
  ListTracesResponseSchema,
  RuleShowcaseSchema,
  TelemetryService,
  TraceStatus,
} from "./testdata/mockgen/gen/sample/v1/telemetry_pb.js";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);
const LIST = "sample.v1.TelemetryService/ListTraces";
const GET = "sample.v1.TelemetryService/GetTrace";
const WATCH = "sample.v1.TelemetryService/Watch";
const UPLOAD = "sample.v1.TelemetryService/Upload";

function client(transport: Transport) {
  return createClient(TelemetryService, transport);
}

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

describe("createFixtureTransport: routes", () => {
  it("serves a unary route through a real serialization round trip", async () => {
    const seen: unknown[] = [];
    const transport = createFixtureTransport({
      routes: (router) =>
        router.service(TelemetryService, {
          listTraces(req) {
            seen.push(req);
            return {
              traces: [{ traceId: `t-${req.projectId}`, spanCount: 3 }],
              totalCount: 1n,
            };
          },
        }),
    });
    const res = await client(transport).listTraces({ projectId: "p1" });
    expect(res.traces.map((t) => t.traceId)).toEqual(["t-p1"]);
    expect(res.totalCount).toBe(1n);
    // The handler got a decoded message, not the caller's object.
    expect(isMessage(seen[0], ListTracesRequestSchema)).toBe(true);
  });

  it("fails a response that cannot be serialized", async () => {
    const transport = createFixtureTransport({
      routes: (router) =>
        router.service(TelemetryService, {
          listTraces: () => ({ totalCount: "many" as unknown as bigint }),
        }),
    });
    await expect(client(transport).listTraces({})).rejects.toThrow(
      /total_count|totalCount|BigInt/,
    );
  });

  it("serves a server stream", async () => {
    const transport = createFixtureTransport({
      routes: (router) =>
        router.service(TelemetryService, {
          async *watch(req) {
            yield { id: `${req.projectId}-1`, status: TraceStatus.OK };
            yield { id: `${req.projectId}-2`, status: TraceStatus.ERROR };
          },
        }),
    });
    const events = await drain(client(transport).watch({ projectId: "p" }));
    expect(events.map((e) => [e.id, e.status])).toEqual([
      ["p-1", TraceStatus.OK],
      ["p-2", TraceStatus.ERROR],
    ]);
  });

  it("serves promised server and bidi streams from async routes", async () => {
    const transport = createFixtureTransport({
      routes: (router) =>
        router.service(TelemetryService, {
          // Connect's types require a direct iterable; tolerate promised ones
          // from JavaScript handlers too.
          // @ts-expect-error deliberately exercise a promised iterable
          async watch(req) {
            return (async function* () {
              yield { id: `${req.projectId}-1` };
              yield { id: `${req.projectId}-2` };
            })();
          },
          // @ts-expect-error deliberately exercise a promised iterable
          async chat(reqs) {
            return (async function* () {
              for await (const req of reqs) yield { id: req.projectId };
            })();
          },
        }),
    });
    const c = client(transport);
    expect((await drain(c.watch({ projectId: "p" }))).map((e) => e.id)).toEqual(
      ["p-1", "p-2"],
    );
    async function* requests() {
      yield { projectId: "first" };
      yield { projectId: "second" };
    }
    expect((await drain(c.chat(requests()))).map((e) => e.id)).toEqual([
      "first",
      "second",
    ]);
  });

  it("runs the caller's interceptors on every call", async () => {
    const calls: string[] = [];
    const tag: Interceptor = (next) => async (req) => {
      calls.push(req.method.name);
      req.header.set("x-fixture", "yes");
      return next(req);
    };
    let header: string | null = null;
    const transport = createFixtureTransport({
      interceptors: [tag],
      routes: (router) =>
        router.service(TelemetryService, {
          getTrace(req, ctx) {
            header = ctx.requestHeader.get("x-fixture");
            return { traceId: req.traceId };
          },
        }),
      defaults: [TelemetryService],
    });
    await client(transport).getTrace({ traceId: "a" });
    await drain(client(transport).watch({}));
    expect(calls).toEqual(["GetTrace", "Watch"]);
    expect(header).toBe("yes");
  });
});

describe("createFixtureTransport: scenario and defaults", () => {
  it("accepts a createMockTransport scenario unchanged", async () => {
    const scenario: MockScenario = {
      name: "existing",
      handlers: {
        [GET]: (req: { traceId: string }) =>
          create(GetTraceResponseSchema, { traceId: req.traceId }),
        [WATCH]: () => [{ id: "a" }, { id: "b" }],
        [UPLOAD]: (reqs: { data: Uint8Array }[]) => ({
          bytes: BigInt(reqs.reduce((n, r) => n + r.data.length, 0)),
        }),
      },
    };
    const c = client(createFixtureTransport({ scenario }));
    expect((await c.getTrace({ traceId: "x" })).traceId).toBe("x");
    expect((await drain(c.watch({}))).map((e) => e.id)).toEqual(["a", "b"]);
    async function* chunks() {
      yield { data: new Uint8Array(3) };
      yield { data: new Uint8Array(4) };
    }
    expect((await c.upload(chunks())).bytes).toBe(7n);
  });

  it("accepts a bare handler map, and a route beats a scenario handler", async () => {
    const c = client(
      createFixtureTransport({
        scenario: { [GET]: () => ({ traceId: "scenario" }) },
        routes: (router) =>
          router.service(TelemetryService, {
            getTrace: () => ({ traceId: "route" }),
          }),
      }),
    );
    expect((await c.getTrace({})).traceId).toBe("route");
  });

  it("answers everything else from the service defaults, then Unimplemented", async () => {
    const transport = createFixtureTransport({
      defaults: [TelemetryService],
      now: () => FIXED_NOW,
    });
    const res = await client(transport).getRuleShowcase({});
    const expected = serviceDefaults(TelemetryService, {
      now: FIXED_NOW,
    }).getRuleShowcase({});
    // Byte-equal, not equals(): the float field `ratio` is rounded to 32 bits
    // on the wire, which is the serialization this transport exists to do.
    expect(toBinary(RuleShowcaseSchema, res)).toEqual(
      toBinary(RuleShowcaseSchema, expected),
    );
    expect((await drain(client(transport).watch({}))).length).toBe(3);

    const billing = createClient(BillingService, transport);
    expect(await code(billing.getInvoice({}))).toBe(Code.Unimplemented);
  });
});

describe("createFixtureTransport: readOnly", () => {
  it("passes an annotated read and refuses everything else, before any handler", async () => {
    const write = vi.fn(() => create(RuleShowcaseSchema));
    const transport = createFixtureTransport({
      readOnly: true,
      readOnlyMessage: "Sample data is read-only",
      scenario: {
        [LIST]: () => ({ totalCount: 2n }),
        "sample.v1.TelemetryService/GetRuleShowcase": write,
        [WATCH]: () => [{ id: "a" }],
      },
    });
    const c = client(transport);
    expect((await c.listTraces({})).totalCount).toBe(2n);

    const refused = await c.getRuleShowcase({}).catch((e: unknown) => e);
    expect(ConnectError.from(refused).code).toBe(Code.PermissionDenied);
    expect(ConnectError.from(refused).rawMessage).toBe(
      "Sample data is read-only",
    );
    expect(write).not.toHaveBeenCalled();
    // An unannotated stream is refused too: no name heuristic.
    expect(await code(drain(c.watch({})))).toBe(Code.PermissionDenied);
  });
});

describe("fixture packs", () => {
  const base = defineFixturePack({
    name: "base",
    idPrefix: "sample.",
    scenario: (ctx) => ({
      [LIST]: () => ({ traces: [{ traceId: ctx.id("t1") }] }),
      [GET]: () => ({ traceId: "base" }),
    }),
  });

  it("composes extends: the later layer wins per method, the rest is inherited", async () => {
    const child = defineFixturePack({
      name: "child",
      extends: [base],
      routes: (router) =>
        router.service(TelemetryService, {
          getTrace: () => ({ traceId: "child" }),
        }),
    });
    const c = client(createFixtureTransport({ pack: child }));
    expect((await c.getTrace({})).traceId).toBe("child");
    expect((await c.listTraces({})).traces[0]?.traceId).toBe("sample.t1");

    // The options' own layer sits on top of the pack.
    const top = client(
      createFixtureTransport({
        pack: child,
        scenario: { [GET]: () => ({ traceId: "options" }) },
      }),
    );
    expect((await top.getTrace({})).traceId).toBe("options");
  });

  it("hands handlers the transport's clock and the pack's id namespace", async () => {
    const seen: FixtureContext[] = [];
    const pack = defineFixturePack({
      name: "clock",
      extends: [base],
      routes: (router, ctx) => {
        seen.push(ctx);
        router.service(TelemetryService, {
          watch: async function* () {
            yield {
              id: ctx.id("e1"),
              observedAt: {
                seconds: BigInt(Math.floor(ctx.now() / 1000)),
                nanos: 0,
              },
            };
          },
        });
      },
    });
    const c = client(createFixtureTransport({ pack, now: () => FIXED_NOW }));
    const [event] = await drain(c.watch({}));
    expect(event?.id).toBe("sample.e1");
    expect(Number(event?.observedAt?.seconds) * 1000).toBe(FIXED_NOW);
    expect(seen[0]?.idPrefix).toBe("sample.");
  });

  it("refuses a pack that extends itself", () => {
    const a = { name: "a", extends: [] as never[] };
    const b = defineFixturePack({ name: "b", extends: [a] });
    (a.extends as unknown[]).push(b);
    expect(() => createFixtureTransport({ pack: b })).toThrow(/extends itself/);
  });

  it("decodes data with strict fromJson and hands it to handlers typed", async () => {
    const pack = defineFixturePack({
      name: "data",
      data: {
        traces: fixtureJson(ListTracesResponseSchema, {
          traces: [{ traceId: "j1", status: "TRACE_STATUS_SLOW" }],
          totalCount: "1",
        }),
      },
      routes: (router, ctx) =>
        router.service(TelemetryService, {
          listTraces: () => ctx.data.traces,
        }),
    });
    const res = await client(createFixtureTransport({ pack })).listTraces({});
    expect(res.traces[0]?.status).toBe(TraceStatus.SLOW);
    expect(res.totalCount).toBe(1n);
  });

  it("rejects data JSON with a field the proto does not declare", () => {
    const pack = defineFixturePack({
      name: "drifted",
      data: {
        traces: fixtureJson(ListTracesResponseSchema, {
          traces: [{ traceId: "j1", spanTotal: 4 }],
        }),
      },
    });
    expect(() => createFixtureTransport({ pack })).toThrow(
      /fixture pack "drifted": data "traces" is not a valid sample\.v1\.ListTracesResponse.*spanTotal/,
    );
  });
});

describe("checkFixturePack", () => {
  const services = [TelemetryService];

  it("finds nothing wrong with valid fixtures", async () => {
    const pack = defineFixturePack({
      name: "sound",
      data: {
        traces: fixtureJson(ListTracesResponseSchema, { totalCount: "2" }),
      },
      scenario: {
        [GET]: () => {
          throw new ConnectError("no such trace", Code.NotFound);
        },
        [WATCH]: () => [{ id: "a" }, { id: "b" }],
        [UPLOAD]: () => ({ bytes: 1n }),
      },
      routes: (router, ctx) =>
        router.service(TelemetryService, {
          listTraces: () => ctx.data.traces,
          async *chat() {
            yield { id: "c" };
          },
        }),
    });
    expect(await checkFixturePack(pack, { services })).toEqual([]);
  });

  it("names the method of each bad handler", async () => {
    const base = defineFixturePack({
      name: "base",
      scenario: {
        // A misspelled field: create() and the router would drop it silently.
        [LIST]: () => ({ traces: [{ traceID: "x" }] }),
      },
    });
    const pack = defineFixturePack({
      name: "bad",
      extends: [base],
      data: {
        broken: fixtureJson(GetTraceResponseSchema, { trace: "x" }),
      },
      scenario: {
        [GET]: () => ({ spans: [{ error: { message: "boom" } }] }),
        [WATCH]: () => [{ id: "ok" }, { id: 7 }],
        "sample.v1.TelemetryService/GetTraec": () => ({}),
        "acme.v1.BillingService/GetInvoice": () => ({}),
      },
      routes: (router) =>
        router.service(TelemetryService, {
          getRuleShowcase: () => {
            throw new TypeError("cannot read properties of undefined");
          },
        }),
    });
    const problems = await checkFixturePack(pack, { services });
    const byMethod = (m: string) =>
      problems.filter((p) => p.method === m).map((p) => p.message);

    expect(byMethod(LIST)).toEqual([
      'response.traces[0].traceID: sample.v1.TraceSummary has no field "traceID"',
    ]);
    expect(problems.find((p) => p.method === LIST)?.pack).toBe("base");
    expect(byMethod(GET)[0]).toMatch(
      /response\.spans\[0\]\.error: "error" is a case of oneof outcome/,
    );
    expect(byMethod(WATCH)).toEqual([
      "message 1.id: expected string, got number",
    ]);
    expect(byMethod("sample.v1.TelemetryService/GetTraec")).toEqual([
      "no method GetTraec on sample.v1.TelemetryService",
    ]);
    expect(byMethod("acme.v1.BillingService/GetInvoice")).toEqual([
      "cannot check: acme.v1.BillingService is not in options.services",
    ]);
    expect(byMethod("sample.v1.TelemetryService/GetRuleShowcase")).toEqual([
      "the handler threw: cannot read properties of undefined",
    ]);
    expect(problems.find((p) => p.data === "broken")).toMatchObject({
      pack: "bad",
      message: expect.stringMatching(
        /^data "broken" is not a valid sample\.v1\.GetTraceResponse/,
      ),
    });
  });

  it("flags a handler returning another message type", async () => {
    const pack = defineFixturePack({
      name: "wrong-type",
      scenario: { [GET]: () => create(RuleShowcaseSchema) },
    });
    expect(await checkFixturePack(pack, { services })).toEqual([
      {
        pack: "wrong-type",
        method: GET,
        message:
          "response is a sample.v1.RuleShowcase, not a sample.v1.GetTraceResponse",
      },
    ]);
  });
});
