// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// A failed RPC is in the dev log. Before this, a scaffolded app's failed
// mutation left one `[browser:debug] [event] toast:show {"message":"HTTP 500"}`
// line in .forge/logs/<env>/frontend_<name>.log: no error line, no procedure,
// no trace id. The trace id was only in the server's `rpc failed` line, so the
// one grep that should join browser to server found nothing (forge bug
// a7db1ac2). These tests drive the runtime's own interceptor chain with dev
// logging installed and read what would have been POSTed to /__forge/log.
//
// No jsdom (see devlog.test.ts): window/document/fetch are stubbed.
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installDevLogging, uninstallDevLogging } from "./devlog.js";
import { ConnectClientError } from "./errors.js";
import { buildRuntimeInterceptors } from "./interceptors.js";
import {
  formatRpcFailure,
  logRpcFailure,
  setRpcFailureSink,
} from "./rpc-failure-log.js";
import { TRACERESPONSE_HEADER, traceIdFrom } from "./trace.js";

interface Entry {
  level: string;
  msg: string;
}

const SERVER_TRACE = "6cdcc2b8d409e05528b82b2687a58af2";
const PROCEDURE = "/services.customers.v1.CustomersService/ArchiveCustomer";

async function microtasks(): Promise<void> {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

/** Every entry the dev log would have POSTed, in order. */
function stubDevLogEndpoint(): () => Entry[] {
  const target = { addEventListener() {}, removeEventListener() {} };
  vi.stubGlobal("window", target);
  vi.stubGlobal("document", { ...target, visibilityState: "visible" });
  const entries: Entry[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: string, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body)) as { entries: Entry[] };
      entries.push(...body.entries);
      return Promise.resolve(
        new Response(null, { status: 204, headers: { "X-Forge-Devlog": "2" } }),
      );
    }),
  );
  return () => entries;
}

/**
 * Run one unary call through the runtime chain, with `fail` standing in for
 * the network: it sees the request (traceparent included) and throws.
 */
async function failedCall(
  fail: (req: { header: Headers }) => never,
): Promise<{ caught: unknown; sentTrace: string | null }> {
  const chain = buildRuntimeInterceptors();
  let sentTrace: string | null = null;
  let call = async (req: { header: Headers }) => {
    sentTrace = traceIdFrom(req.header.get("traceparent"));
    return fail(req);
  };
  for (const ic of [...chain].reverse()) {
    call = ic(call as never) as never;
  }
  const caught = await call({
    header: new Headers(),
    stream: false,
    url: `http://localhost:3000/_api${PROCEDURE}`,
    service: { typeName: "services.customers.v1.CustomersService" },
    method: { name: "ArchiveCustomer" },
  } as never).catch((e: unknown) => e);
  return { caught, sentTrace };
}

describe("a failed RPC in the dev log", () => {
  let posted: () => Entry[];

  beforeEach(() => {
    vi.useFakeTimers();
    posted = stubDevLogEndpoint();
    installDevLogging({ dev: true, mirrorToConsole: false });
  });

  afterEach(() => {
    uninstallDevLogging();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("is an error line naming the procedure, the code and the server's trace id", async () => {
    const { caught } = await failedCall(() => {
      throw new ConnectError("deadline exceeded", Code.DeadlineExceeded, {
        [TRACERESPONSE_HEADER]: `00-${SERVER_TRACE}-29c6a57eee308d8d-01`,
      });
    });
    await microtasks();

    // The caller still gets the typed error, exactly as before.
    expect(caught).toBeInstanceOf(ConnectClientError);
    expect(posted()).toEqual([
      {
        level: "error",
        msg: `rpc failed procedure=${PROCEDURE} code=deadline_exceeded message="deadline exceeded" trace_id=${SERVER_TRACE}`,
      },
    ]);
  });

  // A proxy that hangs up answers with no traceresponse; the line then names
  // the trace of the traceparent the browser sent, which the server joined.
  it("falls back to the trace the request carried when the server sent none", async () => {
    const { sentTrace } = await failedCall(() => {
      throw new ConnectError("HTTP 500", Code.Unknown);
    });
    await microtasks();

    expect(sentTrace).toMatch(/^[0-9a-f]{32}$/);
    const [line] = posted();
    expect(line?.level).toBe("error");
    expect(line?.msg).toContain(`procedure=${PROCEDURE} code=unknown`);
    expect(line?.msg.endsWith(` trace_id=${sentTrace}`)).toBe(true);
  });

  it("names the backend's reason when it set one", async () => {
    await failedCall(() => {
      throw new ConnectError(
        "customer has open orders",
        Code.FailedPrecondition,
        {
          "x-forge-error-reason": "reference_in_use",
        },
      );
    });
    await microtasks();

    expect(posted()[0]?.msg).toContain(
      "code=failed_precondition reason=reference_in_use",
    );
  });

  // Forge keeps a cancelled request — the user navigated away, a component
  // unmounted, a newer query superseded this one — out of the error log
  // (benign-errors.ts): it describes nothing that broke.
  it("leaves a cancelled request out", async () => {
    const { caught } = await failedCall(() => {
      throw new DOMException("signal is aborted without reason", "AbortError");
    });
    await microtasks();
    vi.advanceTimersByTime(1_000);

    expect((caught as ConnectClientError).code).toBe(Code.Canceled);
    expect(posted()).toEqual([]);
  });
});

describe("without dev logging", () => {
  afterEach(() => setRpcFailureSink(null));

  // Production installs no dev logging, so nothing is written anywhere: the
  // browser telemetry SDK already records the failed fetch with its trace.
  it("logs nothing, and the call still fails with the typed error", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const { caught } = await failedCall(() => {
      throw new ConnectError("nope", Code.Internal);
    });
    expect(caught).toBeInstanceOf(ConnectClientError);
    expect(log).not.toHaveBeenCalled();
    log.mockRestore();
  });

  it("gets the app's own sink back when dev logging is uninstalled", () => {
    const target = { addEventListener() {}, removeEventListener() {} };
    vi.stubGlobal("window", target);
    vi.stubGlobal("document", { ...target, visibilityState: "visible" });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(null))),
    );
    const mine = vi.fn();
    setRpcFailureSink(mine);

    installDevLogging({ dev: true, mirrorToConsole: false });
    uninstallDevLogging();
    vi.unstubAllGlobals();

    expect(setRpcFailureSink(null)).toBe(mine);
  });

  it("is a seam another sink can take, and a throwing sink breaks nothing", async () => {
    const seen: string[] = [];
    setRpcFailureSink((f) => seen.push(formatRpcFailure(f)));
    await failedCall(() => {
      throw new ConnectError("nope", Code.Internal);
    });
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatch(
      new RegExp(
        `^rpc failed procedure=${PROCEDURE} code=internal message="nope" trace_id=[0-9a-f]{32}$`,
      ),
    );

    setRpcFailureSink(() => {
      throw new Error("sink is broken");
    });
    expect(() =>
      logRpcFailure({
        procedure: PROCEDURE,
        codeName: "internal",
        reason: null,
        message: "x",
        traceId: null,
      }),
    ).not.toThrow();
  });
});
