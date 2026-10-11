/**
 * The transport's deadline: measured in time the user actually waited, and
 * never spent waiting on a request stranded on a dead connection.
 *
 * Every scenario here is one prod recorded (Sentry, 2026-10-07/08) against a
 * server that answered the same request in milliseconds — see rpcDeadline.ts.
 * The chain under test is the real buildInterceptors() chain; only the wire
 * (connect's fetch) is replaced, by a hand-driven fake.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Interceptor } from "@connectrpc/connect";

const sentry = vi.hoisted(() => ({
  captureException: vi.fn(),
  captureMessage: vi.fn(),
}));
vi.mock("@sentry/react", () => sentry);

vi.mock("@/lib/constants", () => ({
  DEFAULT_GRPC_TIMEOUT_MS: 10000,
  FILE_OPERATION_TIMEOUT_MS: 30000,
  CHAT_OPERATION_TIMEOUT_MS: 30000,
  MCP_OPERATION_TIMEOUT_MS: 60000,
  UPLOAD_TIMEOUT_MS: 60000,
  WORKTREE_OPERATION_TIMEOUT_MS: 30000,
  OAUTH_TIMEOUT_MS: 0,
  OAUTH_EXCHANGE_TIMEOUT_MS: 60000,
  PROVIDER_VALIDATION_TIMEOUT_MS: 60000,
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

type Next = Parameters<Interceptor>[0];
type Req = Parameters<Next>[0];

interface WireCall {
  req: Req;
  resolve: (message: unknown) => void;
  reject: (error: unknown) => void;
}

/** A fake wire: each request stays pending until the test answers it. */
function makeWire() {
  const calls: WireCall[] = [];
  const wire = ((req: Req) =>
    new Promise((resolve, reject) => {
      calls.push({
        req,
        resolve: (message) =>
          resolve({ stream: false, header: new Headers(), trailer: new Headers(), message }),
        reject,
      });
      // Like fetch: an aborted request rejects with the signal's reason.
      req.signal.addEventListener("abort", () => reject(req.signal.reason));
    })) as unknown as Next;
  return { calls, wire };
}

let visibility: DocumentVisibilityState = "visible";
function setVisibility(next: DocumentVisibilityState) {
  visibility = next;
  document.dispatchEvent(new Event("visibilitychange"));
}

async function loadChain() {
  const { buildInterceptors } = await import("../transport");
  const { calls, wire } = makeWire();
  const run = buildInterceptors({ withAuth: false }).reduceRight<Next>(
    (next, interceptor) => interceptor(next),
    wire,
  );
  const call = (service: string, method: string, signal = new AbortController().signal) =>
    run({
      stream: false,
      service: { typeName: service },
      method: { name: method },
      header: new Headers(),
      signal,
      message: {},
      url: `https://api.test/${service}/${method}`,
      requestMethod: "POST",
      contextValues: undefined,
    } as unknown as Req) as Promise<{ message: unknown }>;
  return { calls, call };
}

/**
 * Let promise continuations run without moving the fake clock. The response
 * passes back through every interceptor in the chain, each a few awaits deep.
 */
async function flush() {
  for (let i = 0; i < 50; i++) await Promise.resolve();
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-08T00:35:00Z"));
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  });
});

afterEach(() => {
  vi.useRealTimers();
  delete (document as unknown as { visibilityState?: unknown }).visibilityState;
});

describe("a read stranded across a suspension (ELECTRON-B2)", () => {
  it("is replaced with a fresh request on resume instead of timing out", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.ChatService", "ListChats");
    let settled: unknown = "pending";
    result.then(
      (r) => (settled = r),
      (e) => (settled = e),
    );
    await flush();
    expect(calls).toHaveLength(1);

    // The phone locks with the request in flight. Timers that come due while
    // the tab is frozen all run at once when it thaws.
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(619_000);
    expect(settled).toBe("pending");

    setVisibility("visible");
    await flush();
    expect(calls).toHaveLength(2);
    expect(calls[0].req.signal.aborted).toBe(true);

    calls[1].resolve({ chats: [] });
    await flush();
    expect(settled).toMatchObject({ message: { chats: [] } });
    expect(sentry.captureMessage).not.toHaveBeenCalled();
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});

describe("the budget is active time", () => {
  it("does not spend a write's budget while the page is hidden, and never resends it", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.SettingsService", "UpdateSetting");
    let error: unknown;
    result.catch((e) => (error = e));
    await flush();

    await vi.advanceTimersByTimeAsync(4_000);
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(60_000);
    setVisibility("visible");
    await flush();
    // A write is never sent twice, even across a resume.
    expect(calls).toHaveLength(1);
    expect(error).toBeUndefined();

    await vi.advanceTimersByTimeAsync(5_900);
    expect(error).toBeUndefined();

    await vi.advanceTimersByTimeAsync(200);
    expect(error).toBeInstanceOf(ConnectError);
    expect((error as ConnectError).code).toBe(Code.DeadlineExceeded);

    // One report for the incident, with the active age AND the wall age, so
    // a frozen tab is never again read as a slow server.
    expect(sentry.captureMessage).toHaveBeenCalledTimes(1);
    expect(sentry.captureMessage.mock.calls[0][0]).toContain("UpdateSetting:10s (70s wall)");
    // The timed-out call is not ALSO an exception report (ELECTRON-8X).
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});

describe("a fast read whose answer was lost (ELECTRON-B0, BA)", () => {
  it("is replaced once at the stall deadline", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.DaemonRegistryService", "ListDaemons");
    await flush();

    await vi.advanceTimersByTimeAsync(3_900);
    expect(calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(200);
    expect(calls).toHaveLength(2);
    expect(calls[0].req.signal.aborted).toBe(true);

    calls[1].resolve({ daemons: [] });
    await expect(result).resolves.toMatchObject({ message: { daemons: [] } });
  });

  it("is replaced at most once, then times out on its full budget", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.ChatService", "ListChats");
    let error: unknown;
    result.catch((e) => (error = e));
    await flush();

    await vi.advanceTimersByTimeAsync(9_900);
    expect(calls).toHaveLength(2);
    expect(error).toBeUndefined();

    await vi.advanceTimersByTimeAsync(200);
    expect((error as ConnectError).code).toBe(Code.DeadlineExceeded);
    expect(calls).toHaveLength(2);
  });

  it("leaves reads the server is genuinely slow on alone", async () => {
    const { calls, call } = await loadChain();
    // ListWorkflows: server p90 5s. Replacing it would only restart the wait.
    void call("reliant.v1.WorkflowService", "ListWorkflows").catch(() => {});
    await flush();
    await vi.advanceTimersByTimeAsync(9_000);
    expect(calls).toHaveLength(1);
  });
});

describe("a connection the update stream saw die", () => {
  it("replaces every read in flight on it, and only reads", async () => {
    const { calls, call } = await loadChain();
    void call("reliant.v1.InboxService", "ListInbox").catch(() => {});
    void call("reliant.v1.ChatService", "SendMessage").catch(() => {});
    await flush();
    expect(calls).toHaveLength(2);

    const { pageActivity } = await import("../../lib/pageActivity");
    pageActivity().noteConnectionLost();
    await flush();

    expect(calls).toHaveLength(3);
    expect(calls[2].req.method.name).toBe("ListInbox");
    expect(calls.find((c) => c.req.method.name === "SendMessage")?.req.signal.aborted).toBe(false);
  });
});

describe("caller cancellation", () => {
  it("rejects with the caller's reason and aborts the wire", async () => {
    const { calls, call } = await loadChain();
    const controller = new AbortController();
    const result = call("reliant.v1.ChatService", "GetChat", controller.signal);
    await flush();

    controller.abort(new ConnectError("navigated away", Code.Canceled));
    await expect(result).rejects.toMatchObject({ code: Code.Canceled });
    expect(calls[0].req.signal.aborted).toBe(true);
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});
