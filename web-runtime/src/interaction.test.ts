// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// interaction(): one root span per user-perceived operation, and the RPCs made
// while it is open as its children.
//
// The parenting tests drive the REAL connect-web transport through the
// runtime's interceptor stack, over a fetch that behaves the way the HyperDX
// SDK's patched fetch does (@opentelemetry/instrumentation-fetch,
// _patchConstructor): it opens its CLIENT span from context.active() at the
// instant fetch() is called, then writes that span's traceparent over the
// request's. That instant is the whole question — connect-web's unary path
// calls fetch() synchronously inside next(), its server-stream path only after
// awaiting the request body — so the transport is the real one, not a stand-in.
import {
  create,
  createFileRegistry,
  type DescService,
} from "@bufbuild/protobuf";
import { FileDescriptorProtoSchema } from "@bufbuild/protobuf/wkt";
import { createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  context,
  createContextKey,
  propagation,
  ROOT_CONTEXT,
  SpanKind,
  SpanStatusCode,
  trace,
  type Attributes,
  type Context,
  type ContextManager,
  type Span,
  type SpanContext,
  type SpanOptions,
  type TextMapPropagator,
  type Tracer,
  type TracerProvider,
} from "@opentelemetry/api";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import { installFetchContext } from "./interaction-context.js";
import {
  currentInteraction,
  interaction,
  resetInteractions,
} from "./interaction.js";
import {
  authInterceptor,
  buildRuntimeInterceptors,
  errorNormalizeInterceptor,
  interactionContextInterceptor,
  traceInterceptor,
} from "./interceptors.js";
import {
  registerTelemetryStartup,
  resetTelemetryGate,
} from "./telemetry-gate.js";
import { disableTelemetry, resetTrackState } from "./track.js";
import { injectTraceContext, traceIdFrom, TRACEPARENT_RE } from "./trace.js";

// ── A recording SDK: what HyperDX registers, reduced to what is asserted ──

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

const hex = (n: number) =>
  Array.from({ length: n }, () =>
    Math.floor(Math.random() * 16).toString(16),
  ).join("");

let recorded: Recorded[] = [];

function recordingSpan(r: Recorded): Span {
  const sc: SpanContext = {
    traceId: r.traceId,
    spanId: r.spanId,
    traceFlags: 1,
  };
  const span = {
    spanContext: () => sc,
    setAttribute: (k: string, v: unknown) => {
      r.attributes[k] = v as never;
      return span;
    },
    setAttributes: (a: Attributes) => {
      Object.assign(r.attributes, a);
      return span;
    },
    addEvent: (name: string, attributes?: Attributes) => {
      r.events.push({ name, attributes });
      return span;
    },
    addLink: () => span,
    addLinks: () => span,
    setStatus: (s: { code: SpanStatusCode }) => {
      r.status = s.code;
      return span;
    },
    updateName: () => span,
    end: () => {
      r.ended = true;
    },
    isRecording: () => !r.ended,
    recordException: () => undefined,
  };
  return span as unknown as Span;
}

const tracer = {
  startSpan(name: string, options: SpanOptions = {}, ctx = context.active()) {
    const parent = options.root ? undefined : trace.getSpanContext(ctx);
    const r: Recorded = {
      name,
      kind: options.kind,
      traceId: parent?.traceId ?? hex(32),
      spanId: hex(16),
      parentSpanId: parent?.spanId,
      attributes: { ...options.attributes },
      events: [],
      status: undefined,
      ended: false,
    };
    recorded.push(r);
    return recordingSpan(r);
  },
} as unknown as Tracer;

const provider: TracerProvider = { getTracer: () => tracer };

// OpenTelemetry's StackContextManager, which is what the SDK's context manager
// is with async patching off (its default): the context lives exactly as long
// as the synchronous extent of with().
class StackContextManager implements ContextManager {
  private current: Context = ROOT_CONTEXT;
  active() {
    return this.current;
  }
  with<A extends unknown[], F extends (...args: A) => ReturnType<F>>(
    ctx: Context,
    fn: F,
    thisArg?: ThisParameterType<F>,
    ...args: A
  ): ReturnType<F> {
    const previous = this.current;
    this.current = ctx;
    try {
      return fn.call(thisArg, ...args);
    } finally {
      this.current = previous;
    }
  }
  bind<T>(_ctx: Context, target: T): T {
    return target;
  }
  enable() {
    return this;
  }
  disable() {
    this.current = ROOT_CONTEXT;
    return this;
  }
}

const w3c: TextMapPropagator = {
  inject(ctx, carrier, setter) {
    const sc = trace.getSpanContext(ctx);
    if (sc && trace.isSpanContextValid(sc)) {
      setter.set(carrier, "traceparent", `00-${sc.traceId}-${sc.spanId}-01`);
    }
  },
  extract: (ctx) => ctx,
  fields: () => ["traceparent"],
};

function registerSdk(): void {
  trace.setGlobalTracerProvider(provider);
  context.setGlobalContextManager(new StackContextManager());
  propagation.setGlobalPropagator(w3c);
}

function unregisterSdk(): void {
  trace.disable();
  context.disable();
  propagation.disable();
}

const spanNamed = (name: string) => recorded.filter((r) => r.name === name);
const parentOf = (tp: string | null | undefined) => tp?.split("-")[2];

// ── The page: visibility and animation frames, driven by hand ────────────

let visibility: "visible" | "hidden" = "visible";
let visibilityListeners: Array<() => void> = [];
let frames: Array<() => void> = [];

function hidePage(): void {
  visibility = "hidden";
  for (const fn of visibilityListeners) fn();
}

function paintFrame(): void {
  const due = frames;
  frames = [];
  for (const fn of due) fn();
}

beforeEach(() => {
  recorded = [];
  visibility = "visible";
  visibilityListeners = [];
  frames = [];
  vi.stubGlobal("document", {
    get visibilityState() {
      return visibility;
    },
    addEventListener: (type: string, fn: () => void) => {
      if (type === "visibilitychange") visibilityListeners.push(fn);
    },
  });
  vi.stubGlobal("requestAnimationFrame", (fn: () => void) => {
    frames.push(fn);
    return frames.length;
  });
});

afterEach(() => {
  resetInteractions();
  resetTrackState();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

// Run the trace interceptor alone and return the traceparent it wrote.
async function rpcTraceparent(): Promise<string | null> {
  const req = { header: new Headers(), stream: false };
  let sent: string | null = null;
  const next = async (r: { header: Headers }) => {
    sent = r.header.get("traceparent");
    return { message: {} };
  };
  const run = traceInterceptor() as unknown as (
    n: typeof next,
  ) => (r: typeof req) => Promise<unknown>;
  await run(next)(req);
  return sent;
}

// What injectTraceContext writes for a request made right here,
// synchronously — the only place a caller's active span is still active. (The
// trace interceptor awaits the telemetry gate first, which a stack context
// manager does not survive.)
function syncTraceparent(): string | null {
  const header = new Headers();
  injectTraceContext(header);
  return header.get("traceparent");
}

describe("interaction() with telemetry off", () => {
  it("still has a real trace id, and every RPC while open shares it", async () => {
    const op = interaction("chat.open", { "chat.id": "c1" });
    expect(op.traceId).toMatch(/^[0-9a-f]{32}$/);
    expect(op.traceId).not.toBe("0".repeat(32));

    const a = await rpcTraceparent();
    const b = await rpcTraceparent();
    expect(a).toMatch(TRACEPARENT_RE);
    expect(traceIdFrom(a)).toBe(op.traceId);
    expect(traceIdFrom(b)).toBe(op.traceId);
    // Both name the interaction as their parent, so the server's spans hang
    // off one (unexported) root rather than one trace per call.
    expect(parentOf(a)).toBe(parentOf(b));

    op.end();
    const after = await rpcTraceparent();
    expect(traceIdFrom(after)).not.toBe(op.traceId);
  });

  it("supersedes the open interaction of the same scope, not of another", () => {
    const first = interaction("chat.open");
    const side = interaction("panel.load", {}, { scope: "panel" });
    const second = interaction("chat.open");
    expect(first.ended).toBe(true);
    expect(side.ended).toBe(false);
    expect(second.ended).toBe(false);
    expect(currentInteraction()).toBe(second);
    expect(currentInteraction("panel")).toBe(side);
  });

  it("never throws, whatever it is handed", async () => {
    const op = interaction(undefined as unknown as string, null as never, {
      timeoutMs: Number.NaN,
      scope: 7 as unknown as string,
    });
    expect(op.scope).toBe("foreground");
    op.setAttributes(undefined as never);
    op.mark(undefined as never, { bad: undefined, worse: null });
    op.endAfterPaint();
    op.end({ outcome: undefined });
    expect(op.run(() => 42)).toBe(42);
    expect(await rpcTraceparent()).toMatch(TRACEPARENT_RE);
  });
});

describe("interaction() with an SDK", () => {
  beforeAll(registerSdk);
  afterAll(unregisterSdk);

  it("is a root INTERNAL span named for the interaction", () => {
    const click = tracer.startSpan("click");
    const op = context.with(trace.setSpan(context.active(), click), () =>
      interaction("chat.open", { "chat.id": "c1", skipped: undefined }),
    );
    op.end();
    const [span] = spanNamed("chat.open");
    expect(span?.kind).toBe(SpanKind.INTERNAL);
    expect(span?.parentSpanId).toBeUndefined();
    expect(span?.traceId).toBe(op.traceId);
    expect(span?.attributes).toEqual({
      "chat.id": "c1",
      "interaction.name": "chat.open",
      "interaction.scope": "foreground",
      "interaction.outcome": "ok",
    });
    expect(span?.ended).toBe(true);
  });

  it("ends once: the first end wins", () => {
    const op = interaction("save");
    op.end({ outcome: "error", "save.kind": "draft" });
    op.end({ outcome: "ok" });
    op.endAfterPaint({ outcome: "ok" });
    const [span] = spanNamed("save");
    expect(span?.attributes["interaction.outcome"]).toBe("error");
    expect(span?.attributes["save.kind"]).toBe("draft");
    expect(span?.status).toBe(SpanStatusCode.ERROR);
  });

  it("ends a superseded interaction as superseded", () => {
    interaction("chat.open", { "chat.id": "a" });
    interaction("chat.open", { "chat.id": "b" });
    const [a, b] = spanNamed("chat.open");
    expect(a?.ended).toBe(true);
    expect(a?.attributes["interaction.outcome"]).toBe("superseded");
    expect(b?.ended).toBe(false);
  });

  it("endAfterPaint ends after the second animation frame", () => {
    const op = interaction("chat.open");
    op.endAfterPaint({ outcome: "ok" });
    expect(op.ended).toBe(true);
    const [span] = spanNamed("chat.open");
    expect(span?.ended).toBe(false);
    paintFrame(); // the frame that paints the commit
    expect(span?.ended).toBe(false);
    paintFrame(); // the frame after it
    expect(span?.ended).toBe(true);
    expect(span?.attributes["interaction.paint"]).toBe("painted");
    expect(span?.attributes["interaction.outcome"]).toBe("ok");
  });

  it("endAfterPaint ends at once when no frame will paint", () => {
    vi.stubGlobal("requestAnimationFrame", undefined);
    interaction("no-raf").endAfterPaint();
    expect(spanNamed("no-raf")[0]?.attributes["interaction.paint"]).toBe(
      "skipped",
    );

    vi.stubGlobal("requestAnimationFrame", (fn: () => void) => frames.push(fn));
    visibility = "hidden";
    interaction("hidden-at-end").endAfterPaint();
    const [hidden] = spanNamed("hidden-at-end");
    expect(hidden?.ended).toBe(true);
    expect(hidden?.attributes["interaction.paint"]).toBe("skipped");
    expect(hidden?.attributes["page.hidden"]).toBe(true);
  });

  it("stamps page.hidden when the page hides while open, and stops waiting for a paint", () => {
    const open = interaction("chat.open");
    const painting = interaction("other", {}, { scope: "other" });
    painting.endAfterPaint();
    hidePage();
    const [o] = spanNamed("chat.open");
    const [p] = spanNamed("other");
    expect(o?.attributes["page.hidden"]).toBe(true);
    expect(o?.ended).toBe(false);
    expect(p?.ended).toBe(true);
    expect(p?.attributes["interaction.paint"]).toBe("skipped");
    open.end();
  });

  it("times out", () => {
    vi.useFakeTimers();
    interaction("slow", {}, { timeoutMs: 1_000 });
    vi.advanceTimersByTime(999);
    expect(spanNamed("slow")[0]?.ended).toBe(false);
    vi.advanceTimersByTime(1);
    expect(spanNamed("slow")[0]?.ended).toBe(true);
    expect(spanNamed("slow")[0]?.attributes["interaction.outcome"]).toBe(
      "timeout",
    );

    interaction("default-timeout");
    vi.advanceTimersByTime(59_999);
    expect(spanNamed("default-timeout")[0]?.ended).toBe(false);
    vi.advanceTimersByTime(1);
    expect(spanNamed("default-timeout")[0]?.ended).toBe(true);
  });

  it("marks phases as span events and takes attributes while open", () => {
    const op = interaction("chat.open");
    op.mark("snapshot_received", { "chat.messages": 12 });
    op.setAttributes({ "chat.messages": 12 });
    op.end();
    op.mark("too_late");
    const [span] = spanNamed("chat.open");
    expect(span?.events).toEqual([
      { name: "snapshot_received", attributes: { "chat.messages": 12 } },
    ]);
    expect(span?.attributes["chat.messages"]).toBe(12);
  });

  it("keeps an RPC in the interaction open when it was made, however long the telemetry gate holds it", async () => {
    // The interceptor waits for telemetry startup before it calls next().
    // The context must be chosen when the call ENTERS, not after that wait:
    // by then the user may have started another interaction.
    let finish!: () => void;
    registerTelemetryStartup(new Promise<void>((r) => (finish = r)));
    try {
      const older = interaction("chat.open", { "chat.id": "a" });
      const pending = rpcTraceparent();
      const newer = interaction("chat.open", { "chat.id": "b" });
      finish();
      const tp = await pending;
      expect(traceIdFrom(tp)).toBe(older.traceId);
      expect(traceIdFrom(tp)).not.toBe(newer.traceId);
      expect(parentOf(tp)).toBe(spanNamed("chat.open")[0]?.spanId);
    } finally {
      resetTelemetryGate();
    }
  });

  it("parents an RPC with no active span on the interaction", () => {
    const op = interaction("chat.open");
    const tp = syncTraceparent();
    expect(traceIdFrom(tp)).toBe(op.traceId);
    expect(parentOf(tp)).toBe(spanNamed("chat.open")[0]?.spanId);
  });

  it("leaves an RPC under a span the caller made active (#1428's action-to-RPC contract)", () => {
    interaction("chat.open");
    const action = tracer.startSpan("checkout.submit", {}, ROOT_CONTEXT);
    const tp = context.with(trace.setSpan(context.active(), action), () =>
      syncTraceparent(),
    );
    expect(parentOf(tp)).toBe(spanNamed("checkout.submit")[0]?.spanId);
  });

  it("keeps the caller's other context values when it picks the interaction", async () => {
    const op = interaction("chat.open");
    const key = createContextKey("caller");
    const seen: unknown[] = [];
    const interceptors = buildRuntimeInterceptors({
      getToken: async () => null,
    });
    const next: Parameters<(typeof interceptors)[number]>[0] = async (req) => {
      seen.push(context.active().getValue(key));
      seen.push(trace.getSpanContext(context.active())?.traceId);
      seen.push(traceIdFrom(req.header.get("traceparent")));
      return {} as never;
    };
    const run = interceptors.reduceRight(
      (call, interceptor) => interceptor(call),
      next,
    );
    await context.with(ROOT_CONTEXT.setValue(key, "one"), () =>
      run({ header: new Headers(), stream: false } as never),
    );
    expect(seen).toEqual(["one", op.traceId, op.traceId]);
  });

  it("lets a span inside the interaction's trace stay the more specific parent", () => {
    const older = interaction("chat.open");
    interaction("panel.load", {}, { scope: "panel" });
    const tp = older.run(() => {
      const child = tracer.startSpan("decode");
      return context.with(trace.setSpan(context.active(), child), () =>
        syncTraceparent(),
      );
    });
    const decode = spanNamed("decode")[0];
    expect(decode?.parentSpanId).toBe(spanNamed("chat.open")[0]?.spanId);
    expect(parentOf(tp)).toBe(decode?.spanId);
  });

  it("records nothing after disableTelemetry(), but still shares a trace id", async () => {
    disableTelemetry();
    const op = interaction("chat.open");
    expect(spanNamed("chat.open")).toEqual([]);
    expect(traceIdFrom(await rpcTraceparent())).toBe(op.traceId);
  });
});

// ── The real transport: unary and server-stream fetch spans ──────────────

function testService(): DescService {
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "interaction_test.proto",
      package: "interaction.v1",
      syntax: "proto3",
      messageType: [{ name: "Req" }, { name: "Res" }],
      service: [
        {
          name: "ChatService",
          method: [
            {
              name: "GetChat",
              inputType: ".interaction.v1.Req",
              outputType: ".interaction.v1.Res",
            },
            {
              name: "StreamChat",
              inputType: ".interaction.v1.Req",
              outputType: ".interaction.v1.Res",
              serverStreaming: true,
            },
          ],
        },
      ],
    }),
    () => undefined,
  );
  const svc = registry.getService("interaction.v1.ChatService");
  if (!svc) throw new Error("test service did not register");
  return svc;
}

function envelope(flags: number, json: string): Uint8Array {
  const data = new TextEncoder().encode(json);
  const out = new Uint8Array(5 + data.length);
  out[0] = flags;
  new DataView(out.buffer).setUint32(1, data.length);
  out.set(data, 5);
  return out;
}

function respond(url: string): Response {
  if (url.endsWith("/StreamChat")) {
    const body = new Uint8Array([...envelope(0, "{}"), ...envelope(2, "{}")]);
    return new Response(body, {
      headers: { "content-type": "application/connect+json" },
    });
  }
  return new Response("{}", {
    headers: { "content-type": "application/json" },
  });
}

interface FetchSpan {
  url: string;
  parentSpanId: string | undefined;
  traceId: string;
  sentTraceparent: string | null;
}

// The SDK's patched fetch, as @opentelemetry/instrumentation-fetch 0.219
// writes it: span from context.active() NOW, then its own traceparent.
function sdkFetch(seen: FetchSpan[]): typeof fetch {
  return ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const span = tracer.startSpan("HTTP POST", { kind: SpanKind.CLIENT });
    const r = recorded[recorded.length - 1]!;
    return context.with(trace.setSpan(context.active(), span), () => {
      const headers = new Headers(init?.headers);
      propagation.inject(context.active(), headers, {
        set: (h, k, v) => h.set(k, String(v)),
      });
      seen.push({
        url,
        parentSpanId: r.parentSpanId,
        traceId: r.traceId,
        sentTraceparent: headers.get("traceparent"),
      });
      return Promise.resolve(respond(url)).finally(() => span.end());
    });
  }) as typeof fetch;
}

describe("the browser fetch spans of RPCs made while an interaction is open", () => {
  beforeAll(registerSdk);
  afterAll(unregisterSdk);

  function chatClient(
    shim: boolean,
    seen: FetchSpan[],
    interceptors: Interceptor[] = buildRuntimeInterceptors({
      getToken: async () => "token",
    }),
  ) {
    vi.stubGlobal("fetch", sdkFetch(seen));
    if (shim) installFetchContext();
    // A runtime-built descriptor carries no generated method types. An async
    // getToken puts a real await in front of the trace interceptor, as an
    // app's auth provider does.
    return createClient(
      testService(),
      createConnectTransport({
        baseUrl: "http://api.test",
        interceptors,
      }),
    ) as unknown as {
      getChat(req: object): Promise<unknown>;
      streamChat(req: object): AsyncIterable<unknown>;
    };
  }

  async function callBoth(client: ReturnType<typeof chatClient>) {
    await client.getChat({});
    for await (const _ of client.streamChat({})) {
      // drain
    }
  }

  async function openChat(shim: boolean): Promise<{
    op: ReturnType<typeof interaction>;
    seen: FetchSpan[];
  }> {
    const seen: FetchSpan[] = [];
    const client = chatClient(shim, seen);
    const op = interaction("chat.open", { "chat.id": "c1" });
    await callBoth(client);
    return { op, seen };
  }

  it("of a span the caller had active stay that span's children", async () => {
    // #1428's contract, now through the real transport and the stream path:
    // the caller's span survives the auth await AND connect-web's.
    const seen: FetchSpan[] = [];
    const client = chatClient(true, seen);
    interaction("chat.open");
    const action = tracer.startSpan("chat.refresh", {}, ROOT_CONTEXT);
    const actionId = spanNamed("chat.refresh")[0]!.spanId;
    await context.with(trace.setSpan(context.active(), action), () =>
      Promise.all([
        client.getChat({}),
        (async () => {
          for await (const _ of client.streamChat({})) {
            // drain
          }
        })(),
      ]),
    );
    expect(seen.map((s) => s.parentSpanId)).toEqual([actionId, actionId]);
  });

  it("are its children, unary and server stream alike", async () => {
    const { op, seen } = await openChat(true);
    const root = spanNamed("chat.open")[0]!;
    expect(seen.map((s) => s.url)).toEqual([
      "http://api.test/interaction.v1.ChatService/GetChat",
      "http://api.test/interaction.v1.ChatService/StreamChat",
    ]);
    for (const s of seen) {
      expect(s.traceId, s.url).toBe(op.traceId);
      expect(s.parentSpanId, s.url).toBe(root.spanId);
      // ...and the server is handed the fetch span, which it parents under.
      expect(traceIdFrom(s.sentTraceparent), s.url).toBe(op.traceId);
    }
    op.end();
  });

  it("lose the stream without the fetch shim (why telemetry.ts installs it)", async () => {
    const { op, seen } = await openChat(false);
    const root = spanNamed("chat.open")[0]!;
    const [unary, stream] = seen;
    // connect-web calls fetch() synchronously for a unary call...
    expect(unary?.parentSpanId).toBe(root.spanId);
    // ...but awaits the request body of a stream first, by which time a
    // stack context manager has dropped the interaction.
    expect(stream?.parentSpanId).toBeUndefined();
    expect(stream?.traceId).not.toBe(op.traceId);
    op.end();
  });

  // ── Call entry: the choice is made when the call starts ────────────────

  // A gate the test opens by hand: an auth provider still fetching a token, an
  // app interceptor waiting on something of its own.
  function gate(): { wait: () => Promise<void>; open: () => void } {
    let open!: () => void;
    const opened = new Promise<void>((r) => (open = r));
    return { wait: () => opened, open };
  }

  const awaiting =
    (g: { wait: () => Promise<void> }): Interceptor =>
    (next) =>
    async (req) => {
      await g.wait();
      return next(req);
    };

  // Start a unary and a stream call under the open interaction, start a newer
  // one while `held` keeps both calls waiting, then let them go.
  async function callsHeldAcrossANewInteraction(
    interceptors: Interceptor[],
    held: { open: () => void },
  ): Promise<{ older: string; newer: string; seen: FetchSpan[] }> {
    const seen: FetchSpan[] = [];
    const client = chatClient(true, seen, interceptors);
    const older = interaction("chat.open", { "chat.id": "a" });
    const unary = client.getChat({});
    const stream = (async () => {
      for await (const _ of client.streamChat({})) {
        // drain
      }
    })();
    // The user opens another chat while both calls wait.
    const newer = interaction("chat.open", { "chat.id": "b" });
    held.open();
    await Promise.all([unary, stream]);
    newer.end();
    return { older: older.traceId, newer: newer.traceId, seen };
  }

  it("enters the outermost interceptor synchronously inside the client call", () => {
    // The premise of choosing at call entry: connect-es reaches the first
    // interceptor with no await between the client method and it.
    const entered: string[] = [];
    const probe: Interceptor = (next) => (req) => {
      entered.push(req.method.name);
      return next(req);
    };
    const client = chatClient(true, [], [probe]);
    const unary = client.getChat({});
    const stream = client.streamChat({});
    expect(entered).toEqual(["GetChat", "StreamChat"]);
    return Promise.all([
      unary,
      (async () => {
        for await (const _ of stream) {
          // drain
        }
      })(),
    ]);
  });

  it("keeps a call in the interaction it started under while auth awaits a token (the default chain)", async () => {
    const token = gate();
    const { older, newer, seen } = await callsHeldAcrossANewInteraction(
      buildRuntimeInterceptors({
        getToken: async () => {
          await token.wait();
          return "token";
        },
      }),
      token,
    );
    expect(seen).toHaveLength(2);
    for (const s of seen) {
      expect(s.traceId, s.url).toBe(older);
      expect(s.traceId, s.url).not.toBe(newer);
      expect(traceIdFrom(s.sentTraceparent), s.url).toBe(older);
    }
  });

  it("keeps it there through an app's awaiting stages inside the runtime's outermost one (reliant's chain shape)", async () => {
    // error-normalize → (app stage that awaits) → auth (awaits) → trace
    const app = gate();
    const { older, seen } = await callsHeldAcrossANewInteraction(
      [
        errorNormalizeInterceptor({}),
        awaiting(app),
        authInterceptor({ getToken: async () => null }),
        traceInterceptor(),
      ],
      app,
    );
    expect(seen.map((s) => s.traceId)).toEqual([older, older]);
  });

  it("needs interactionContextInterceptor() first when the app's own awaiting stage is outermost", async () => {
    const app = gate();
    const { older, seen } = await callsHeldAcrossANewInteraction(
      [interactionContextInterceptor(), awaiting(app), traceInterceptor()],
      app,
    );
    expect(seen.map((s) => s.traceId)).toEqual([older, older]);

    // Without it, the first runtime stage the call reaches is after the
    // app's await, and the choice is made there.
    const late = gate();
    const after = await callsHeldAcrossANewInteraction(
      [awaiting(late), traceInterceptor()],
      late,
    );
    expect(after.seen.map((s) => s.traceId)).toEqual([
      after.newer,
      after.newer,
    ]);
  });
});
