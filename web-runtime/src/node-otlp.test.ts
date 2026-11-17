import { describe, expect, it, vi } from "vitest";

import {
  BatchQueue,
  encodeAttributes,
  exportSignal,
  type ExporterOptions,
  type FetchLike,
  MAX_ATTRIBUTES,
  MAX_RETRY_DELAY_MS,
  tracesPayload,
  unixNanos,
} from "./node-otlp.js";

describe("OTLP/JSON encoding", () => {
  it("encodes each attribute type as OTLP/JSON spells it", () => {
    expect(
      encodeAttributes({
        s: "x",
        i: 42,
        d: 1.5,
        b: true,
        a: ["p", "q"],
        skip: undefined,
        inf: Number.POSITIVE_INFINITY,
      }),
    ).toEqual([
      { key: "s", value: { stringValue: "x" } },
      // int64 is a decimal STRING in OTLP/JSON.
      { key: "i", value: { intValue: "42" } },
      { key: "d", value: { doubleValue: 1.5 } },
      { key: "b", value: { boolValue: true } },
      {
        key: "a",
        value: {
          arrayValue: { values: [{ stringValue: "p" }, { stringValue: "q" }] },
        },
      },
    ]);
  });

  it("bounds the attribute count", () => {
    const many = Object.fromEntries(
      Array.from({ length: 500 }, (_, i) => [`k${i}`, i]),
    );
    expect(encodeAttributes(many)).toHaveLength(MAX_ATTRIBUTES);
  });

  it("writes nanosecond timestamps as exact decimal strings", () => {
    expect(unixNanos(1_700_000_000_123.5)).toBe("1700000000123500000");
    expect(unixNanos(1_700_000_000_123)).toBe("1700000000123000000");
    expect(unixNanos(0)).toBe("0");
  });

  it("groups spans by scope and keeps the scope off the span", () => {
    const span = (name: string, scope: string) => ({
      traceId: "a".repeat(32),
      spanId: "b".repeat(16),
      name,
      kind: 1,
      startTimeUnixNano: "1",
      endTimeUnixNano: "2",
      attributes: [],
      events: [],
      links: [],
      status: { code: 0 },
      scope: { name: scope },
    });
    const body = tracesPayload(
      [],
      [span("one", "x"), span("two", "y"), span("three", "x")],
    );
    const scopes = body.resourceSpans[0]!.scopeSpans;
    expect(
      scopes.map((s) => [s.scope.name, s.spans.map((sp) => sp.name)]),
    ).toEqual([
      ["x", ["one", "three"]],
      ["y", ["two"]],
    ]);
    expect(scopes[0]!.spans[0]).not.toHaveProperty("scope");
  });
});

function response(status: number, retryAfter?: string) {
  return {
    status,
    headers: {
      get: (n: string) => (n === "retry-after" ? (retryAfter ?? null) : null),
    },
  };
}

function options(
  fetch: FetchLike,
  extra: Partial<ExporterOptions> = {},
): ExporterOptions {
  return {
    endpoint: "https://ingest.test/k/pk_x",
    headers: { origin: "app://bundle" },
    fetch,
    timeoutMs: 1000,
    retries: 2,
    sleep: async () => {},
    ...extra,
  };
}

describe("exportSignal", () => {
  it("posts JSON to <endpoint>/v1/<signal> with the configured headers", async () => {
    const fetch = vi.fn<FetchLike>(async () => response(200));
    expect(
      await exportSignal(options(fetch), "logs", { resourceLogs: [] }),
    ).toBe(true);
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("https://ingest.test/k/pk_x/v1/logs");
    expect(init.method).toBe("POST");
    expect(init.headers).toEqual({
      origin: "app://bundle",
      "content-type": "application/json",
    });
    expect(JSON.parse(init.body)).toEqual({ resourceLogs: [] });
  });

  it("retries a throttled or unavailable ingest, honouring Retry-After within a bound", async () => {
    const waits: number[] = [];
    const answers = [response(429, "1"), response(503, "600"), response(200)];
    const fetch = vi.fn<FetchLike>(async () => answers.shift()!);
    const ok = await exportSignal(
      options(fetch, { sleep: async (ms) => void waits.push(ms) }),
      "traces",
      {},
    );
    expect(ok).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(waits).toEqual([1000, MAX_RETRY_DELAY_MS]);
  });

  it("drops a batch the ingest refuses, without retrying, and says so", async () => {
    const onError = vi.fn();
    const fetch = vi.fn<FetchLike>(async () => response(403));
    expect(await exportSignal(options(fetch, { onError }), "logs", {})).toBe(
      false,
    );
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0]![0].message).toContain("HTTP 403");
  });

  it("never rejects on a network error", async () => {
    const fetch = vi.fn<FetchLike>(async () => {
      throw new Error("ECONNREFUSED");
    });
    expect(await exportSignal(options(fetch), "logs", {})).toBe(false);
    expect(fetch).toHaveBeenCalledTimes(3);
  });
});

describe("BatchQueue", () => {
  it("exports when a batch fills and on flush, and drops past the bound", async () => {
    const sent: number[][] = [];
    const q = new BatchQueue<number>({
      maxQueue: 5,
      maxBatch: 3,
      delayMs: 60_000,
      send: async (items) => void sent.push(items),
    });
    for (let i = 0; i < 3; i++) q.push(i);
    expect(sent).toEqual([[0, 1, 2]]);
    q.push(3);
    await q.flush();
    expect(sent).toEqual([[0, 1, 2], [3]]);

    const blocked = new BatchQueue<number>({
      maxQueue: 2,
      maxBatch: 10,
      delayMs: 60_000,
      send: async () => {},
    });
    blocked.push(1);
    blocked.push(2);
    blocked.push(3);
    expect(blocked.dropped).toBe(1);
    await blocked.flush();
  });

  it("exports after the delay", async () => {
    vi.useFakeTimers();
    try {
      const sent: string[][] = [];
      const q = new BatchQueue<string>({
        maxQueue: 10,
        maxBatch: 10,
        delayMs: 1000,
        send: async (i) => void sent.push(i),
      });
      q.push("a");
      expect(sent).toEqual([]);
      await vi.advanceTimersByTimeAsync(1000);
      expect(sent).toEqual([["a"]]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("flush waits for an export already in flight", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    let done = false;
    const q = new BatchQueue<number>({
      maxQueue: 10,
      maxBatch: 1,
      delayMs: 60_000,
      send: async () => {
        await gate;
        done = true;
      },
    });
    q.push(1);
    const flushed = q.flush();
    release();
    await flushed;
    expect(done).toBe(true);
  });
});
