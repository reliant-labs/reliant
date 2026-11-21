// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The RPC span stage on its own: the span a call records, how it ends, what a
// failure adds, and that it costs nothing without a tracer provider. The
// parenting through the real connect-web transport and the SDK's fetch span is
// proved in interaction.test.ts.
import {
  create,
  createFileRegistry,
  type DescService,
} from "@bufbuild/protobuf";
import {
  FieldDescriptorProto_Label as Label,
  FieldDescriptorProto_Type as Type,
  FileDescriptorProtoSchema,
} from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import {
  ROOT_CONTEXT,
  SpanKind,
  SpanStatusCode,
  trace,
  type Attributes,
  type Span,
  type SpanOptions,
  type Tracer,
} from "@opentelemetry/api";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { buildRuntimeInterceptors } from "./interceptors.js";
import { resetForensics } from "./rpc-forensics.js";
import { rpcSpanInterceptor } from "./rpc-span.js";
import { resetTelemetryGate, setRpcTelemetry } from "./telemetry-gate.js";
import { traceIdFrom } from "./trace.js";

// ── A recording tracer provider ─────────────────────────────────────────

interface Recorded {
  name: string;
  kind: SpanKind | undefined;
  traceId: string;
  spanId: string;
  parentSpanId: string | undefined;
  attributes: Attributes;
  events: Array<{ name: string; attributes?: Attributes }>;
  status: SpanStatusCode | undefined;
  ended: boolean;
}

let recorded: Recorded[] = [];
let ids = 0;
const id = (n: number) => (++ids).toString(16).padStart(n, "0");

const tracer = {
  startSpan(name: string, options: SpanOptions = {}, ctx = ROOT_CONTEXT): Span {
    const parent = trace.getSpanContext(ctx);
    const r: Recorded = {
      name,
      kind: options.kind,
      traceId: parent?.traceId ?? id(32),
      spanId: id(16),
      parentSpanId: parent?.spanId,
      attributes: { ...options.attributes },
      events: [],
      status: undefined,
      ended: false,
    };
    recorded.push(r);
    const span = {
      spanContext: () => ({
        traceId: r.traceId,
        spanId: r.spanId,
        traceFlags: 1,
      }),
      setAttribute: (k: string, v: unknown) => {
        r.attributes[k] = v as never;
        return span;
      },
      setAttributes: () => span,
      addEvent: (n: string, attributes?: Attributes) => {
        r.events.push({ name: n, attributes });
        return span;
      },
      setStatus: (s: { code: SpanStatusCode }) => {
        r.status = s.code;
        return span;
      },
      end: () => {
        r.ended = true;
      },
      isRecording: () => !r.ended,
    };
    return span as unknown as Span;
  },
} as unknown as Tracer;

// ── A service with a request worth classifying ──────────────────────────

function chatService(): DescService {
  const opt = Label.OPTIONAL;
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "rpc_span_test.proto",
      package: "pkg.v1",
      syntax: "proto3",
      messageType: [
        {
          name: "Req",
          field: [
            {
              name: "chat_id",
              number: 1,
              type: Type.STRING,
              label: opt,
              jsonName: "chatId",
            },
            {
              name: "text",
              number: 2,
              type: Type.STRING,
              label: opt,
              jsonName: "text",
            },
            {
              name: "token",
              number: 3,
              type: Type.STRING,
              label: opt,
              jsonName: "token",
              options: { debugRedact: true },
            },
          ],
        },
        { name: "Res" },
      ],
      service: [
        {
          name: "ChatService",
          method: [
            {
              name: "SendMessage",
              inputType: ".pkg.v1.Req",
              outputType: ".pkg.v1.Res",
            },
            {
              name: "Watch",
              inputType: ".pkg.v1.Req",
              outputType: ".pkg.v1.Res",
              serverStreaming: true,
            },
          ],
        },
      ],
    }),
    () => undefined,
  );
  const svc = registry.getService("pkg.v1.ChatService");
  if (!svc) throw new Error("test service did not register");
  return svc;
}

const svc = chatService();

function unaryReq(message: object = {}) {
  const method = svc.methods.find((m) => m.name === "SendMessage")!;
  return {
    stream: false,
    service: svc,
    method,
    url: "http://api.test/pkg.v1.ChatService/SendMessage",
    header: new Headers(),
    message: create(method.input, message as never),
  };
}

async function* once<T>(m: T): AsyncIterable<T> {
  yield m;
}

function streamReq(message: object = {}) {
  const method = svc.methods.find((m) => m.name === "Watch")!;
  return {
    stream: true,
    service: svc,
    method,
    url: "http://api.test/pkg.v1.ChatService/Watch",
    header: new Headers(),
    message: once(create(method.input, message as never)),
  };
}

type Next = Parameters<Interceptor>[0];

function call(stage: Interceptor, next: Next, req: object): Promise<unknown> {
  return stage(next)(req as never);
}

const failWith =
  (code: Code): Next =>
  async () => {
    throw new ConnectError("boom: the user's text was here", code);
  };

const ok: Next = async () => ({ stream: false, message: {} }) as never;

beforeEach(() => {
  recorded = [];
  trace.setGlobalTracerProvider({ getTracer: () => tracer });
});

afterEach(() => {
  trace.disable();
  resetTelemetryGate();
  resetForensics();
});

describe("rpcSpanInterceptor", () => {
  it("records one CLIENT span named <rpc.service>/<rpc.method>", async () => {
    await call(rpcSpanInterceptor(), ok, unaryReq());
    expect(recorded).toEqual([
      expect.objectContaining({
        name: "pkg.v1.ChatService/SendMessage",
        kind: SpanKind.CLIENT,
        attributes: {
          "rpc.system": "connect_rpc",
          "rpc.service": "pkg.v1.ChatService",
          "rpc.method": "SendMessage",
          "forge.rpc.client": true,
        },
        status: undefined,
        events: [],
        ended: true,
      }),
    ]);
  });

  it("is the parent of the traceparent the trace stage writes", async () => {
    // buildRuntimeInterceptors' order: the span stage directly outside the
    // trace stage. With no SDK propagator, the traceparent is written from
    // the span context the call joins, which must be the RPC span.
    const stages = buildRuntimeInterceptors();
    let traceparent: string | null = null;
    const run = stages.reduceRight<Next>(
      (n, stage) => stage(n),
      async (req) => {
        traceparent = req.header.get("traceparent");
        return { stream: false, message: {} } as never;
      },
    );
    await run(unaryReq() as never);
    const rpc = recorded.find(
      (r) => r.name === "pkg.v1.ChatService/SendMessage",
    );
    expect(traceIdFrom(traceparent)).toBe(rpc?.traceId);
    expect(traceparent).toContain(`-${rpc?.spanId}-`);
  });

  it("marks a failure with its Connect code and one redacted forensics event", async () => {
    const req = unaryReq({
      chatId: "c_1",
      text: "my secret plans",
      token: "t0p",
    });
    await expect(
      call(rpcSpanInterceptor(), failWith(Code.NotFound), req),
    ).rejects.toThrow(ConnectError);
    const [span] = recorded;
    expect(span?.status).toBe(SpanStatusCode.ERROR);
    expect(span?.attributes["rpc.connect_rpc.error_code"]).toBe("not_found");
    expect(span?.events).toEqual([
      {
        name: "forge.forensics",
        attributes: {
          "forensics.reason": "error",
          "forensics.arg.chat_id": "c_1",
          "forensics.arg.text": "[len=15]",
        },
      },
    ]);
  });

  it("records forensics once per procedure and code per page session", async () => {
    const stage = rpcSpanInterceptor();
    for (const code of [Code.Internal, Code.Internal, Code.Unavailable]) {
      await call(stage, failWith(code), unaryReq({ chatId: "c" })).catch(
        () => {},
      );
    }
    expect(recorded.map((r) => r.events.length)).toEqual([1, 0, 1]);
    expect(recorded.map((r) => r.status)).toEqual([
      SpanStatusCode.ERROR,
      SpanStatusCode.ERROR,
      SpanStatusCode.ERROR,
    ]);
  });

  it("treats a cancelled call as no failure", async () => {
    await call(rpcSpanInterceptor(), failWith(Code.Canceled), unaryReq()).catch(
      () => {},
    );
    const [span] = recorded;
    expect(span?.attributes["rpc.connect_rpc.error_code"]).toBe("canceled");
    expect(span?.status).toBeUndefined();
    expect(span?.events).toEqual([]);
    expect(span?.ended).toBe(true);
  });

  it("leaves forensics out when the chain or the page turns them off", async () => {
    await call(
      rpcSpanInterceptor({ forensics: false }),
      failWith(Code.Internal),
      unaryReq({ chatId: "c" }),
    ).catch(() => {});
    setRpcTelemetry({ forensics: false });
    await call(rpcSpanInterceptor(), failWith(Code.Unknown), unaryReq()).catch(
      () => {},
    );
    expect(recorded.map((r) => r.status)).toEqual([
      SpanStatusCode.ERROR,
      SpanStatusCode.ERROR,
    ]);
    expect(recorded.flatMap((r) => r.events)).toEqual([]);
  });

  it("records nothing once telemetry is disabled", async () => {
    setRpcTelemetry({ spans: false, forensics: false });
    await call(rpcSpanInterceptor(), ok, unaryReq());
    expect(recorded).toEqual([]);
  });

  it("ends a stream's span when the caller has read it all", async () => {
    let finished = false;
    const res = (await call(
      rpcSpanInterceptor(),
      async (req) => {
        for await (const _ of req.message as AsyncIterable<unknown>) {
          // the transport sends the request
        }
        return {
          stream: true,
          message: (async function* () {
            yield {};
            yield {};
            finished = true;
          })(),
        } as never;
      },
      streamReq(),
    )) as { message: AsyncIterable<unknown> };
    expect(recorded[0]?.ended).toBe(false);
    for await (const _ of res.message) {
      // drain
    }
    expect(finished).toBe(true);
    expect(recorded[0]).toMatchObject({
      name: "pkg.v1.ChatService/Watch",
      ended: true,
      status: undefined,
    });
  });

  it("fails a stream's span on a mid-stream error, with the request it was sent", async () => {
    const res = (await call(
      rpcSpanInterceptor(),
      async (req) => {
        for await (const _ of req.message as AsyncIterable<unknown>) {
          // the transport sends the request
        }
        return {
          stream: true,
          message: (async function* () {
            yield {};
            throw new ConnectError("gone", Code.Unavailable);
          })(),
        } as never;
      },
      streamReq({ chatId: "c_9", text: "hello" }),
    )) as { message: AsyncIterable<unknown> };
    await expect(
      (async () => {
        for await (const _ of res.message) {
          // drain
        }
      })(),
    ).rejects.toThrow("gone");
    expect(recorded[0]).toMatchObject({
      status: SpanStatusCode.ERROR,
      attributes: { "rpc.connect_rpc.error_code": "unavailable" },
      events: [
        {
          name: "forge.forensics",
          attributes: {
            "forensics.reason": "error",
            "forensics.arg.chat_id": "c_9",
            "forensics.arg.text": "[len=5]",
          },
        },
      ],
      ended: true,
    });
  });

  it("ends a stream's span without error when the caller stops early", async () => {
    const res = (await call(
      rpcSpanInterceptor(),
      async () =>
        ({
          stream: true,
          message: (async function* () {
            yield {};
            yield {};
          })(),
        }) as never,
      streamReq(),
    )) as { message: AsyncIterable<unknown> };
    for await (const _ of res.message) {
      break;
    }
    expect(recorded[0]).toMatchObject({ ended: true, status: undefined });
  });
});

describe("rpcSpanInterceptor without a tracer provider", () => {
  beforeEach(() => trace.disable());

  it("records nothing and leaves the call's traceparent as it was", async () => {
    const stages = buildRuntimeInterceptors();
    let traceparent: string | null = null;
    const run = stages.reduceRight<Next>(
      (n, stage) => stage(n),
      async (req) => {
        traceparent = req.header.get("traceparent");
        throw new ConnectError("nope", Code.Internal);
      },
    );
    await expect(run(unaryReq({ chatId: "c" }) as never)).rejects.toThrow();
    expect(recorded).toEqual([]);
    expect(traceIdFrom(traceparent)).not.toBeNull();
  });
});
