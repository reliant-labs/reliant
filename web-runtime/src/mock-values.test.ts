// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The service defaults' contract: what each field kind becomes, that every
// protovalidate rule the engine reads holds, that the same request gives the
// same answer, how a scenario overrides, and how the transport serves them.
//
// The schemas are REAL protoc-gen-es output (testdata/mockgen, from
// proto/sample/v1/telemetry.proto and a vendored proto/acme/v1/billing.proto),
// so the protovalidate rules arrive exactly as an app's do: as option bytes
// inside the embedded file descriptor.
import { create, isMessage, toBinary, toJson } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
  Code,
  ConnectError,
  createClient,
  type Transport,
} from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { createMockTransport, type MockScenario } from "./mock-transport.js";
import {
  isServiceDefaults,
  mockFactory,
  mockService,
  mockValue,
  patch,
  serviceDefaults,
} from "./mock-values.js";
import {
  BillingService,
  GetInvoiceResponseSchema,
} from "./testdata/mockgen/gen/acme/v1/billing_pb.js";
import {
  GetTraceResponseSchema,
  ContradictoryEnumSchema,
  EchoProbeResponseSchema,
  FoldedBoundaryPatternSchema,
  ImpossibleBytesSchema,
  LookaheadPatternSchema,
  PatternProbeSchema,
  SpacePatternSchema,
  UndeclaredEnumConstSchema,
  ZeroOnlyEnumSchema,
  ProbeService,
  UncheapPatternSchema,
  UnsatisfiablePatternSchema,
  ListTracesRequestSchema,
  ListTracesResponseSchema,
  Region,
  RuleShowcaseSchema,
  SpanSchema,
  TelemetryService,
  TraceStatus,
  TraceSummarySchema,
  UploadSummarySchema,
  WatchEventSchema,
} from "./testdata/mockgen/gen/sample/v1/telemetry_pb.js";

const NOW = Date.UTC(2026, 10, 6, 12, 0, 0);
const opts = { now: NOW };
const defaults = serviceDefaults(TelemetryService, opts);

const scenario = (handlers: MockScenario["handlers"] = {}): MockScenario => ({
  name: "test",
  handlers,
});

describe("value generation by field kind", () => {
  const list = defaults.listTraces({ projectId: "proj-1" });
  const trace = list.traces[0]!;
  const spans = defaults.getTrace({ traceId: "f".repeat(32) }).spans;

  it("builds a complete message of the method's output type", () => {
    expect(isMessage(list, ListTracesResponseSchema)).toBe(true);
    // Round-trips the wire: every value is representable in its field.
    expect(() => toBinary(ListTracesResponseSchema, list)).not.toThrow();
  });

  it("gives ids their conventional shapes", () => {
    expect(trace.traceId).toMatch(/^[0-9a-f]{32}$/);
    for (const s of spans) {
      expect(s.spanId).toMatch(/^[0-9a-f]{16}$/);
      expect(s.parentSpanId).toMatch(/^[0-9a-f]{16}$/);
    }
    const upload = defaults.upload([]);
    expect(upload.uploadId).toMatch(/^upload-[0-9a-f]{8}$/);
    const events = defaults.watch({});
    expect(events[0]!.id).toMatch(/^watch-event-[0-9a-f]{8}$/);
    expect(new Set(list.traces.map((t) => t.traceId)).size).toBe(
      list.traces.length,
    );
  });

  it("puts timestamps in the recent past, newest first, start before end", () => {
    const starts = list.traces.map((t) =>
      timestampDate(t.startTime!).getTime(),
    );
    for (const [i, t] of list.traces.entries()) {
      const start = starts[i]!;
      const end = timestampDate(t.endTime!).getTime();
      expect(start).toBeLessThanOrEqual(end);
      expect(end).toBeLessThanOrEqual(NOW);
      expect(NOW - start).toBeLessThan(24 * 3600_000);
    }
    expect([...starts].sort((a, b) => b - a)).toEqual(starts);
    // An int64 *_unix_nano is an epoch in nanoseconds.
    const nanos = spans[0]!.events[0]!.timeUnixNano;
    expect(typeof nanos).toBe("bigint");
    expect(Number(nanos / 1_000_000n)).toBeLessThanOrEqual(NOW);
    expect(NOW - Number(nanos / 1_000_000n)).toBeLessThan(24 * 3600_000);
  });

  it("names things by their message and list position", () => {
    expect(trace.rootSpanName).toBe("Root span 1");
    expect(list.traces[1]!.rootSpanName).toBe("Root span 2");
    expect(spans[0]!.name).toBe("Span 1");
    expect(trace.rootService).toBe("root-service-1");
    expect(trace.services).toEqual(
      trace.services.map((_, i) => `service-${i + 1}`),
    );
  });

  it("uses example.com for emails and links", () => {
    const invoice = serviceDefaults(BillingService, opts).getInvoice(
      {},
    ).invoice!;
    expect(invoice.customerEmail).toMatch(/^user-\d+@example\.com$/);
    const rules = defaults.getRuleShowcase({});
    expect(rules.homepage).toMatch(/^https:\/\/example\.com\//);
  });

  it("cycles enums across a list and skips UNSPECIFIED", () => {
    const statuses = list.traces.map((t) => t.status);
    expect(statuses).not.toContain(TraceStatus.UNSPECIFIED);
    // Three defined values: any three consecutive items show all of them.
    expect(new Set(statuses.slice(0, 3)).size).toBe(3);
  });

  it("sizes repeated fields 3–10 and maps 3–5", () => {
    const sample = mockValue(TraceSummarySchema, { ...opts, seed: "x" });
    expect(Object.keys(sample.attributes).length).toBeGreaterThanOrEqual(3);
    expect(Object.keys(sample.attributes).length).toBeLessThanOrEqual(5);
    for (const s of spans) {
      expect(s.events.length).toBeGreaterThanOrEqual(3);
      expect(s.events.length).toBeLessThanOrEqual(10);
    }
    // int64 map values are bigints, like any int64.
    const counters = Object.values(spans[0]!.events[0]!.counters);
    expect(counters.every((v) => typeof v === "bigint")).toBe(true);
  });

  it("handles Duration, Struct, wrappers and oneofs", () => {
    for (const s of spans) {
      expect(typeof s.duration!.seconds).toBe("bigint");
      // Struct is a JsonObject in a field.
      expect(Object.keys(s.attributes!).length).toBeGreaterThan(0);
      expect(typeof Object.values(s.attributes!)[0]).toBe("string");
      // StringValue is unboxed to a string.
      expect(typeof s.peerService).toBe("string");
      // Exactly one oneof member is set.
      expect(["error", "statusMessage", "httpStatus"]).toContain(
        s.outcome.case,
      );
    }
    expect(new Set(spans.map((s) => s.outcome.case)).size).toBeGreaterThan(1);
    expect(list.red!.p95).toBeDefined();
  });

  it("nests a recursive message once, then stops", () => {
    const span = mockValue(SpanSchema, opts);
    const depth = (s: typeof span): number =>
      1 + Math.max(0, ...s.children.map(depth));
    expect(depth(span)).toBeLessThanOrEqual(3);
  });

  it("never mints a next page token, and counts the list it sits beside", () => {
    expect(list.nextPageToken).toBe("");
    expect(list.totalCount).toBe(BigInt(list.traces.length));
  });
});

describe("protovalidate rules", () => {
  const r = defaults.getRuleShowcase({});

  it.each([
    ["pattern", () => expect(r.sku).toMatch(/^[A-Z]{3}-[0-9]{4}$/)],
    [
      "email",
      () => expect(r.contactEmail).toMatch(/^[^@\s]+@[^@\s]+\.[a-z]+$/),
    ],
    ["uri", () => expect(() => new URL(r.homepage)).not.toThrow()],
    [
      "uuid",
      () =>
        expect(r.requestUuid).toMatch(
          /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
        ),
    ],
    [
      "string in",
      () => expect(["free", "pro", "enterprise"]).toContain(r.tier),
    ],
    [
      "min_len/max_len",
      () => {
        expect(r.code.length).toBeGreaterThanOrEqual(12);
        expect(r.code.length).toBeLessThanOrEqual(16);
      },
    ],
    ["max_len", () => expect(r.short.length).toBeLessThanOrEqual(4)],
    ["prefix/suffix", () => expect(r.tag).toMatch(/^tag_.*_v1$/)],
    [
      "int32 gt/lt",
      () => {
        expect(r.bounded).toBeGreaterThan(1000);
        expect(r.bounded).toBeLessThan(1010);
      },
    ],
    ["int64 const", () => expect(r.fixed).toBe(42n)],
    ["uint64 in", () => expect([7n, 11n, 13n]).toContain(r.choice)],
    [
      "float gte/lte",
      () => {
        expect(r.ratio).toBeGreaterThanOrEqual(0.25);
        expect(r.ratio).toBeLessThanOrEqual(0.5);
      },
    ],
    ["bool const", () => expect(r.must).toBe(true)],
    ["bytes len", () => expect(r.digest.length).toBe(32)],
    ["bytes min_len above the default", () => expect(r.salt.length).toBe(24)],
    [
      "bytes min_len/max_len",
      () => {
        expect(r.nonce.length).toBeGreaterThanOrEqual(4);
        expect(r.nonce.length).toBeLessThanOrEqual(8);
      },
    ],
    [
      "int32 map values gte/lte",
      () => {
        const vs = Object.values(r.quotas);
        expect(vs.length).toBeGreaterThan(0);
        for (const v of vs) {
          expect(typeof v).toBe("number");
          expect(v).toBeGreaterThanOrEqual(500);
          expect(v).toBeLessThanOrEqual(510);
        }
      },
    ],
    [
      "enum map values in",
      () => {
        const vs = Object.values(r.homeRegions);
        expect(vs.length).toBeGreaterThan(0);
        expect(vs.every((v) => v === Region.EU)).toBe(true);
      },
    ],
    [
      "message map values",
      () => {
        const vs = Object.values(r.errorsByCode);
        expect(vs.length).toBeGreaterThan(0);
        for (const v of vs) {
          expect(isMessage(v)).toBe(true);
          expect(v.message).not.toBe("");
        }
      },
    ],
    ["min_items/max_items", () => expect(r.labels).toHaveLength(12)],
    [
      "min_pairs/max_pairs",
      () => {
        const n = Object.keys(r.pairs).length;
        expect(n).toBeGreaterThanOrEqual(1);
        expect(n).toBeLessThanOrEqual(2);
      },
    ],
    [
      "duration gte/lte",
      () => {
        const s = Number(r.timeout!.seconds) + r.timeout!.nanos / 1e9;
        expect(s).toBeGreaterThanOrEqual(1);
        expect(s).toBeLessThanOrEqual(5);
      },
    ],
    [
      "timestamp gt_now",
      () => expect(timestampDate(r.expiresAt!).getTime()).toBeGreaterThan(NOW),
    ],
    ["enum not_in", () => expect(r.region).toBe(Region.SA)],
    ["hostname", () => expect(r.host).toMatch(/^[a-z0-9.-]+\.[a-z]+$/)],
    ["ipv4", () => expect(r.addr).toMatch(/^(\d{1,3}\.){3}\d{1,3}$/)],
    [
      "repeated items",
      () => {
        for (const s of r.scores) {
          expect(s).toBeGreaterThanOrEqual(90);
          expect(s).toBeLessThanOrEqual(99);
        }
      },
    ],
  ])("%s", (_name, check) => check());

  it("refuses bytes bounds no value satisfies, naming the field", () => {
    expect(() => mockValue(ImpossibleBytesSchema, opts)).toThrow(
      /sample\.v1\.ImpossibleBytes\.blob has bytes rules no value satisfies/,
    );
  });

  it("applies enum `in` on list items", () => {
    const traces = defaults.listTraces({}).traces;
    for (const t of traces) expect([Region.US, Region.EU]).toContain(t.region);
  });

  it("applies repeated min/max_items and span_count's range on every item", () => {
    const traces = defaults.listTraces({}).traces;
    expect(traces.length).toBeGreaterThanOrEqual(1);
    expect(traces.length).toBeLessThanOrEqual(50);
    for (const t of traces) {
      expect(t.services.length).toBeLessThanOrEqual(3);
      expect(t.spanCount).toBeGreaterThanOrEqual(1);
      expect(t.spanCount).toBeLessThanOrEqual(200);
    }
  });
});

describe("determinism", () => {
  const bytes = (m: Parameters<typeof toBinary>[1]) =>
    toBinary(ListTracesResponseSchema, m as never);

  it("answers the same request with the same message", () => {
    const a = defaults.listTraces({ projectId: "p" });
    const b = serviceDefaults(TelemetryService, opts).listTraces(
      create(ListTracesRequestSchema, { projectId: "p" }),
    );
    expect(bytes(b)).toEqual(bytes(a));
  });

  it("answers a different request differently", () => {
    const a = defaults.listTraces({ projectId: "p" });
    const b = defaults.listTraces({ projectId: "q" });
    expect(bytes(b)).not.toEqual(bytes(a));
  });

  it("does not depend on call order", () => {
    const first = defaults.listTraces({ pageSize: 7 });
    defaults.getTrace({});
    defaults.listTraces({ pageSize: 8 });
    expect(bytes(defaults.listTraces({ pageSize: 7 }))).toEqual(bytes(first));
  });

  it("memoizes the plain defaults per service", () => {
    expect(serviceDefaults(TelemetryService)).toBe(
      serviceDefaults(TelemetryService),
    );
    expect(isServiceDefaults(defaults)).toBe(true);
    expect(isServiceDefaults({ listTraces: () => 1 })).toBe(false);
    // $service is not enumerable: the object spreads as its handlers.
    expect(Object.keys(defaults)).toEqual(
      TelemetryService.methods.map((m) => m.localName),
    );
  });

  it("echoes the request's own fields, but not a bare id onto list items", () => {
    const res = defaults.listTraces({
      projectId: "proj-42",
      status: TraceStatus.SLOW,
    });
    for (const t of res.traces) {
      expect(t.projectId).toBe("proj-42");
      expect(t.status).toBe(TraceStatus.SLOW);
    }
    expect(defaults.getTrace({ traceId: "a".repeat(32) }).traceId).toBe(
      "a".repeat(32),
    );
    const billing = serviceDefaults(BillingService, opts);
    expect(billing.getInvoice({ id: "inv-7" }).invoice!.id).toBe("inv-7");
  });
});

describe("echo meets the response's rules", () => {
  const probe = serviceDefaults(ProbeService, opts);

  it("echoes a request value only when the response field's rules accept it", () => {
    const ok = probe.echoProbe({
      tier: "pro",
      sku: "ABC-1234",
      bounded: 1005,
      region: Region.EU,
    });
    expect(ok).toMatchObject({
      tier: "pro",
      sku: "ABC-1234",
      bounded: 1005,
      region: Region.EU,
    });

    const bad = probe.echoProbe({
      tier: "platinum",
      sku: "nope",
      bounded: 7,
      region: Region.US,
      code: "other",
    });
    expect(["free", "pro", "enterprise"]).toContain(bad.tier);
    expect(bad.sku).toMatch(/^[A-Z]{3}-[0-9]{4}$/);
    expect(bad.bounded).toBeGreaterThan(1000);
    expect(bad.bounded).toBeLessThan(1010);
    expect([Region.EU, Region.APAC]).toContain(bad.region);
    expect(bad.code).toBe("fixed");
  });

  // Checked against Go's regexp (RE2): `(?i)^\w+$` matches the Kelvin sign
  // and the long s, as JavaScript's /^\w+$/iu does, and rejects "a-b".
  it("reads \\w under (?i) as RE2 does: case-folded", () => {
    expect(probe.echoProbe({ word: "\u212A\u017F" }).word).toBe("\u212A\u017F");
    expect(probe.echoProbe({ word: "a-b" }).word).toMatch(/^[A-Za-z0-9_]+$/);
  });

  it("keeps the oneof case the request selected, with its value when the rules allow", () => {
    const host = probe.echoProbe({
      target: { case: "host", value: "api.example.com" },
    });
    expect(host.target).toEqual({ case: "host", value: "api.example.com" });

    const status = probe.echoProbe({
      target: { case: "httpStatus", value: 9999 },
    });
    expect(status.target.case).toBe("httpStatus");
    const code = status.target.value as number;
    expect(code).toBeGreaterThanOrEqual(100);
    expect(code).toBeLessThanOrEqual(599);

    const badHost = probe.echoProbe({
      target: { case: "host", value: "not a host!" },
    });
    expect(badHost.target.case).toBe("host");
    expect(badHost.target.value).toMatch(/^[a-z0-9.-]+$/);
  });

  it("checks numeric echoes exactly, in the response field's own type", () => {
    // 2^53 + 1: Number() rounds it to 2^53, which lte 2^53 would accept.
    const past = probe.echoProbe({ big: 9007199254740993n });
    expect(past.big).not.toBe(9007199254740993n);
    expect(past.big).toBeLessThanOrEqual(9007199254740992n);
    expect(past.big).not.toBe(9007199254740992n);

    // Below -2^53, inside int64 and the rules: echoed digit for digit.
    const low = -(2n ** 62n) - 1n;
    expect(probe.echoProbe({ big: low }).big).toBe(low);

    // A uint64 past int32's range does not echo into an int32.
    const huge = probe.echoProbe({ huge: 2n ** 40n }).huge;
    expect(huge).toBeLessThanOrEqual(2147483647);
    expect(probe.echoProbe({ huge: 42n }).huge).toBe(42);

    // A double a float field cannot hold exactly is not echoed; one it can is.
    expect(probe.echoProbe({ ratio: 0.1 }).ratio).not.toBe(0.1);
    expect(probe.echoProbe({ ratio: 0.5 }).ratio).toBe(0.5);
  });

  it("chooses by seed when the requested case is not one of the response's members", () => {
    const res = probe.echoProbe({
      target: { case: "unknownCase", value: "x" },
    });
    expect(["host", "httpStatus", "other"]).toContain(res.target.case);
    expect(
      probe.echoProbe({ target: { case: "unknownCase", value: "x" } }),
    ).toEqual(res);
  });
});

describe("patterns the cheap generator cannot build", () => {
  it("meets a pattern and max_len together, a leading (?i), and word boundaries", () => {
    const v = mockValue(PatternProbeSchema, opts);
    expect(v.shortWord).toMatch(/^[a-z]{1,3}$/);
    expect(v.ci).toMatch(/^ab[0-9]$/i);
    expect(v.boundary).toBe("foo");
    expect(mockValue(PatternProbeSchema, opts)).toEqual(v);
  });

  it("refuses rules no value meets, naming the field", () => {
    expect(() => mockValue(UnsatisfiablePatternSchema, opts)).toThrow(
      /sample\.v1\.UnsatisfiablePattern\.word has string rules no generated value satisfies \(pattern "\^\[a-z\]\{5\}\$", max_len 3\)/,
    );
  });

  it("refuses a pattern it cannot build rather than emit a value that does not match", () => {
    expect(() => mockValue(UncheapPatternSchema, opts)).toThrow(
      /sample\.v1\.UncheapPattern\.word has string rules/,
    );
  });

  it("refuses a construct RE2 rejects even though JavaScript accepts it", () => {
    expect(() => mockValue(LookaheadPatternSchema, opts)).toThrow(
      /sample\.v1\.LookaheadPattern\.word has string rules/,
    );
  });

  // Checked against Go's regexp (RE2): `(?i)^\b\w$` rejects "\u212A" (its
  // \b is ASCII), while JavaScript's /^\b\w$/iu accepts it.
  it("refuses a word boundary under (?i), which RE2 and JavaScript read differently", () => {
    expect(() => mockValue(FoldedBoundaryPatternSchema, opts)).toThrow(
      /sample\.v1\.FoldedBoundaryPattern\.word has string rules/,
    );
  });

  it("reads \\s as RE2 does: ASCII whitespace, not a no-break space", () => {
    const v = mockValue(SpacePatternSchema, opts);
    expect(v.gap).toMatch(/^a[\t\n\f\r ]b$/);
    expect(mockValue(SpacePatternSchema, opts)).toEqual(v);
  });
});

describe("enum rules", () => {
  it("chooses the zero value when the rules allow only it", () => {
    expect(mockValue(ZeroOnlyEnumSchema, opts).region).toBe(Region.UNSPECIFIED);
  });

  it("refuses rules no declared value meets, naming the field", () => {
    expect(() => mockValue(UndeclaredEnumConstSchema, opts)).toThrow(
      /sample\.v1\.UndeclaredEnumConst\.region \(sample\.v1\.Region\) has enum rules no declared value satisfies \(const 99\)/,
    );
    expect(() => mockValue(ContradictoryEnumSchema, opts)).toThrow(
      /sample\.v1\.ContradictoryEnum\.region \(sample\.v1\.Region\) has enum rules no declared value satisfies \(const 2, not_in \[2\]\)/,
    );
  });
});

describe("overrides", () => {
  const client = (transport: Transport) =>
    createClient(TelemetryService, transport);

  it("mockService hands a typed override the defaults to patch", async () => {
    const transport = createMockTransport({
      scenario: scenario(
        mockService(
          TelemetryService,
          {
            listTraces: (req, d) => {
              const base = d.listTraces(req);
              return patch(base, {
                traces: base.traces.slice(0, 2),
                red: { errorRate: 0.5 },
              });
            },
          },
          opts,
        ),
      ),
      services: [defaults],
    });
    const res = await client(transport).listTraces({ projectId: "p" });
    const base = defaults.listTraces({ projectId: "p" });
    expect(res.traces).toHaveLength(2);
    expect(res.traces[0]!.traceId).toBe(base.traces[0]!.traceId);
    // A nested message merges: errorRate changed, the rest kept.
    expect(res.red!.errorRate).toBe(0.5);
    expect(res.red!.requestCount).toBe(base.red!.requestCount);
    // Not overridden: the default answers.
    const t = await client(transport).getTrace({});
    expect(toJson(GetTraceResponseSchema, t)).toEqual(
      toJson(GetTraceResponseSchema, defaults.getTrace({})),
    );
  });

  it("completes a plain-object override against the response schema", async () => {
    const transport = createMockTransport({
      scenario: scenario(
        mockService(TelemetryService, {
          listTraces: () => ({ nextPageToken: "x" }),
        }),
      ),
      services: [defaults],
    });
    const res = await client(transport).listTraces({});
    expect(res.nextPageToken).toBe("x");
    expect(res.traces).toEqual([]);
  });

  it("types overrides by the service: unknown RPCs and wrong fields fail tsc", () => {
    mockService(TelemetryService, {
      // @ts-expect-error — no such RPC on TelemetryService
      listTrace: () => ({}),
    });
    mockService(TelemetryService, {
      // @ts-expect-error — trace_id is not a ListTracesResponse field
      listTraces: () => ({ traceId: "x" }),
    });
    mockService(TelemetryService, {
      listTraces: (req) => {
        // The request is the typed message.
        const id: string = req.projectId;
        return { traces: [{ traceId: id }] };
      },
    });
  });

  it("a factory reshapes one message type wherever it is built", () => {
    const withFactory = serviceDefaults(TelemetryService, {
      ...opts,
      factories: [
        mockFactory(TraceSummarySchema, ({ index }) => ({
          rootService: index % 2 ? "web" : "api",
        })),
      ],
    });
    const traces = withFactory.listTraces({}).traces;
    expect(traces.map((t) => t.rootService).slice(0, 3)).toEqual([
      "api",
      "web",
      "api",
    ]);
    // Everything the factory did not set is the default.
    const plain = defaults.listTraces({}).traces;
    expect(traces[0]!.traceId).toBe(plain[0]!.traceId);
    // mockService with factories answers every RPC with them.
    const handlers = mockService(TelemetryService, {}, withFactoryOpts());
    expect(Object.keys(handlers)).toContain("sample.v1.TelemetryService/Watch");
  });

  it("patch returns a new message and leaves the default alone", () => {
    const base = defaults.listTraces({});
    const next = patch(base, { traces: [{ traceId: "t" }] });
    expect(isMessage(next, ListTracesResponseSchema)).toBe(true);
    expect(next.traces[0]!.traceId).toBe("t");
    // A partial list item is completed with zero values.
    expect(next.traces[0]!.services).toEqual([]);
    expect(base.traces.length).toBeGreaterThan(1);
    expect(next.red).toEqual(base.red);
  });
});

function withFactoryOpts() {
  return {
    factories: [mockFactory(WatchEventSchema, () => ({ id: "fixed" }))],
  };
}

describe("the transport serves defaults", () => {
  it("answers every RPC of a listed service, vendored ones included", async () => {
    const billing = serviceDefaults(BillingService, opts);
    const transport = createMockTransport({
      scenario: scenario(),
      services: [defaults, billing],
    });
    const res = await createClient(BillingService, transport).getInvoice({
      id: "inv-1",
    });
    expect(toJson(GetInvoiceResponseSchema, res)).toEqual(
      toJson(GetInvoiceResponseSchema, billing.getInvoice({ id: "inv-1" })),
    );
  });

  it("ranks below a scenario handler", async () => {
    const transport = createMockTransport({
      scenario: scenario({
        "sample.v1.TelemetryService/ListTraces": () => ({ nextPageToken: "s" }),
      }),
      services: [defaults],
    });
    const res = await createClient(TelemetryService, transport).listTraces({});
    expect(res.nextPageToken).toBe("s");
  });

  it("ranks below hybrid passthrough", async () => {
    const backend = createMockTransport({
      scenario: scenario({
        "sample.v1.TelemetryService/ListTraces": () => ({
          nextPageToken: "live",
        }),
      }),
    });
    const transport = createMockTransport({
      scenario: { ...scenario(), passthrough: true },
      services: [defaults],
      fallback: backend,
    });
    const res = await createClient(TelemetryService, transport).listTraces({});
    expect(res.nextPageToken).toBe("live");
  });

  it('answers from the defaults when passthrough "first" fails', async () => {
    const down: Transport = {
      unary: () => Promise.reject(new ConnectError("down", Code.Unavailable)),
      stream: () => Promise.reject(new ConnectError("down", Code.Unavailable)),
    };
    const transport = createMockTransport({
      scenario: { ...scenario(), passthrough: "first" },
      services: [defaults],
      fallback: down,
    });
    const res = await createClient(TelemetryService, transport).listTraces({});
    expect(res.traces.length).toBeGreaterThan(0);
  });

  it("still says Unimplemented for a service it was not given", async () => {
    const transport = createMockTransport({ scenario: scenario() });
    await expect(
      createClient(TelemetryService, transport).listTraces({}),
    ).rejects.toMatchObject({ code: Code.Unimplemented });
  });

  it("streams a short deterministic server stream", async () => {
    const transport = createMockTransport({
      scenario: scenario(),
      services: [defaults],
    });
    const got = [];
    for await (const ev of createClient(TelemetryService, transport).watch({
      projectId: "p",
    })) {
      got.push(toJson(WatchEventSchema, ev));
    }
    expect(got).toHaveLength(3);
    expect(got).toEqual(
      defaults
        .watch({ projectId: "p" })
        .map((m) => toJson(WatchEventSchema, m)),
    );
  });

  it("answers a client stream with one message and a bidi stream with three", async () => {
    const transport = createMockTransport({
      scenario: scenario(),
      services: [defaults],
    });
    const c = createClient(TelemetryService, transport);
    async function* chunks() {
      yield { data: new Uint8Array([1]) };
      yield { data: new Uint8Array([2]) };
    }
    const summary = await c.upload(chunks());
    expect(isMessage(summary, UploadSummarySchema)).toBe(true);
    const replies = [];
    async function* asks() {
      yield { projectId: "p" };
    }
    for await (const ev of c.chat(asks())) replies.push(ev);
    expect(replies).toHaveLength(3);
  });
});

describe("golden", () => {
  // The defaults for the sample contract, pinned: a change here changes what
  // every app's mocked pages show, so it is a reviewed change, not drift.
  it("matches the sample contract's pinned defaults", async () => {
    const golden = {
      listTraces: toJson(
        ListTracesResponseSchema,
        defaults.listTraces({ projectId: "proj-1" }),
      ),
      getTrace: toJson(GetTraceResponseSchema, defaults.getTrace({})),
      getRuleShowcase: toJson(RuleShowcaseSchema, defaults.getRuleShowcase({})),
      watch: defaults.watch({}).map((m) => toJson(WatchEventSchema, m)),
      getInvoice: toJson(
        GetInvoiceResponseSchema,
        serviceDefaults(BillingService, opts).getInvoice({}),
      ),
    };
    await expect(JSON.stringify(golden, null, 2) + "\n").toMatchFileSnapshot(
      "./testdata/mockgen/defaults.golden.json",
    );
  });
});
