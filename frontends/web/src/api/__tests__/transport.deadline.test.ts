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

const reporting = vi.hoisted(() => ({
  reportError: vi.fn(),
  reportFailure: vi.fn(),
  reportTypedFailure: vi.fn(),
}));
vi.mock("@/lib/errorReporting", () => reporting);

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
    expect(reporting.reportFailure).not.toHaveBeenCalled();
    expect(reporting.reportError).not.toHaveBeenCalled();
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
    expect(reporting.reportTypedFailure).toHaveBeenCalledTimes(1);
    const [type, message, context] = reporting.reportTypedFailure.mock.calls[0];
    expect(context["rpc.in_flight"]).toContain("UpdateSetting:10s (70s wall)");
    // The message is the issue's identity: only the method and its budget,
    // never the in-flight snapshot, which differs on every timeout. The type
    // tells the control plane it is a client-side timeout.
    expect(type).toBe("RpcTimeout");
    expect(message).toBe("rpc-timeout: UpdateSetting after 10000ms");
    expect(context["rpc.method"]).toBe("UpdateSetting");
    // The timed-out call is not ALSO an exception report (ELECTRON-8X).
    expect(reporting.reportError).not.toHaveBeenCalled();
  });
});

describe("a fast read whose answer was lost (ELECTRON-B0, BA)", () => {
  it("is hedged once at the stall deadline, and the hedge's answer wins", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.DaemonRegistryService", "ListDaemons");
    await flush();

    await vi.advanceTimersByTimeAsync(3_900);
    expect(calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(200);
    expect(calls).toHaveLength(2);
    // The stalled request stays on the wire: it may only be slow.
    expect(calls[0].req.signal.aborted).toBe(false);

    calls[1].resolve({ daemons: [] });
    await expect(result).resolves.toMatchObject({ message: { daemons: [] } });
    // Answered: the request still out is withdrawn, and that is not an error.
    expect(calls[0].req.signal.aborted).toBe(true);
    expect(reporting.reportError).not.toHaveBeenCalled();
  });

  // Prod, 2026-11-01: GetWorkflowExecutions on chat 9fbaace8 was aborted at
  // 3.94s and resent, and the resend took 3.6s — 7.7s for an answer the first
  // request was moments from. A slow read is not restarted from zero.
  it("keeps the stalled request, whose late answer wins over the hedge", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.ChatService", "GetWorkflowExecutions");
    await flush();

    await vi.advanceTimersByTimeAsync(4_100);
    expect(calls).toHaveLength(2);

    calls[0].resolve({ executions: ["first"] });
    await expect(result).resolves.toMatchObject({ message: { executions: ["first"] } });
    expect(calls[1].req.signal.aborted).toBe(true);
    expect(calls).toHaveLength(2);
  });

  it("waits for the hedge when the stalled request fails", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.ChatService", "GetChat");
    let settled: unknown = "pending";
    result.then(
      (r) => (settled = r),
      (e) => (settled = e),
    );
    await flush();
    await vi.advanceTimersByTimeAsync(4_100);
    expect(calls).toHaveLength(2);

    // The connection the first request rode died under it.
    calls[0].reject(new ConnectError("connection reset", Code.Unavailable));
    await flush();
    expect(settled).toBe("pending");

    calls[1].resolve({ chat: {} });
    await flush();
    expect(settled).toMatchObject({ message: { chat: {} } });
  });

  it("is hedged at most once, then times out on its full budget", async () => {
    const { calls, call } = await loadChain();
    const result = call("reliant.v1.ChatService", "GetChat");
    let error: unknown;
    result.catch((e) => (error = e));
    await flush();

    await vi.advanceTimersByTimeAsync(9_900);
    expect(calls).toHaveLength(2);
    expect(error).toBeUndefined();

    await vi.advanceTimersByTimeAsync(200);
    expect((error as ConnectError).code).toBe(Code.DeadlineExceeded);
    expect(calls).toHaveLength(2);
    expect(calls.every((c) => c.req.signal.aborted)).toBe(true);
    // Both requests ran out of the one budget together: one timeout report.
    expect(reporting.reportTypedFailure).toHaveBeenCalledTimes(1);
    expect(reporting.reportTypedFailure.mock.calls[0][0]).toBe("RpcTimeout");
  });

  it("leaves reads the server is genuinely slow on alone", async () => {
    const { calls, call } = await loadChain();
    // ListWorkflows: server p90 5s. Replacing it would only restart the wait.
    void call("reliant.v1.WorkflowService", "ListWorkflows").catch(() => {});
    await flush();
    await vi.advanceTimersByTimeAsync(9_000);
    expect(calls).toHaveLength(1);
  });

  // Prod obs 16122d5e (2026-11-18): ListMessages' slow tail is the chat's
  // message count on the server; a hedge only ran the same count twice.
  it("does not hedge ListMessages", async () => {
    const { calls, call } = await loadChain();
    void call("reliant.v1.ChatService", "ListMessages").catch(() => {});
    await flush();
    await vi.advanceTimersByTimeAsync(9_000);
    expect(calls).toHaveLength(1);
  });

  // 2026-11-17/18: ListInbox p95 2.9s (3.6s under load), ListChats 2.25s in
  // one trace; the sidebar lists are slow on the server, so a hedge is a
  // second copy of the same slow read on the database.
  it.each([
    ["reliant.v1.InboxService", "ListInbox"],
    ["reliant.v1.ChatService", "ListChats"],
    ["reliant.v1.ChatService", "ListArchivedChats"],
  ])("does not hedge %s/%s", async (service, method) => {
    const { calls, call } = await loadChain();
    void call(service, method).catch(() => {});
    await flush();
    await vi.advanceTimersByTimeAsync(9_000);
    expect(calls).toHaveLength(1);
  });
});

// Prod, 2026-11-18: four calls of one tab hung together on a dead connection,
// and the first to run out of budget (ListDeployAttempts, which the server had
// answered in 22ms) opened an issue of its own and paged. Calls that hang
// together are one stall, reported once, carrying every method.
describe("calls that hang together", () => {
  it("are one stall report carrying every method, not a timeout per method", async () => {
    const { calls, call } = await loadChain();
    const methods: Array<[string, string]> = [
      ["reliant.v1.DeployService", "ListDeployAttempts"],
      ["reliant.v1.ProjectService", "GetProject"],
      ["reliant.v1.SettingsService", "UpdateSetting"],
      ["reliant.v1.WorkflowService", "ListWorkflows"],
    ];
    const errors: unknown[] = [];
    for (const [service, method] of methods) {
      call(service, method).catch((e) => errors.push(e));
      await vi.advanceTimersByTimeAsync(300);
    }
    await flush();
    expect(calls.length).toBeGreaterThanOrEqual(4);

    // Every budget runs out, one after another.
    await vi.advanceTimersByTimeAsync(11_000);
    expect(errors).toHaveLength(4);
    for (const e of errors) expect((e as ConnectError).code).toBe(Code.DeadlineExceeded);

    expect(reporting.reportTypedFailure).toHaveBeenCalledTimes(1);
    const [type, message, context] = reporting.reportTypedFailure.mock.calls[0];
    // One issue for every stall: the message names no method.
    expect(type).toBe("RpcStall");
    expect(message).toBe("connection stalled");
    expect(context["rpc.stalled_methods"]).toBe("GetProject,ListDeployAttempts,ListWorkflows,UpdateSetting");
    expect(context["rpc.stalled_count"]).toBe(4);
    expect(context["rpc.first_timeout_method"]).toBe("ListDeployAttempts");
    expect(context["rpc.classification"]).toBe("client_stall");
    expect(reporting.reportFailure).not.toHaveBeenCalled();
    expect(reporting.reportError).not.toHaveBeenCalled();
  });

  it("leaves out a call that started well after the timed-out one", async () => {
    const { call } = await loadChain();
    call("reliant.v1.SettingsService", "UpdateSetting").catch(() => {});
    await flush();
    await vi.advanceTimersByTimeAsync(5_000);
    // Started 5s later: silent for 5s when the first times out, but not part
    // of its stall (a new request on a fresh connection, say).
    call("reliant.v1.ProjectService", "UpdateProject").catch(() => {});
    await flush();

    await vi.advanceTimersByTimeAsync(5_100);
    expect(reporting.reportTypedFailure).toHaveBeenCalledTimes(1);
    expect(reporting.reportTypedFailure.mock.calls[0].slice(0, 2)).toEqual([
      "RpcTimeout",
      "rpc-timeout: UpdateSetting after 10000ms",
    ]);
  });

  it("leaves out a call that was answered", async () => {
    const { calls, call } = await loadChain();
    call("reliant.v1.SettingsService", "UpdateSetting").catch(() => {});
    void call("reliant.v1.ProjectService", "UpdateProject");
    await flush();
    calls[1].resolve({});
    await flush();

    await vi.advanceTimersByTimeAsync(10_100);
    expect(reporting.reportTypedFailure).toHaveBeenCalledTimes(1);
    expect(reporting.reportTypedFailure.mock.calls[0][0]).toBe("RpcTimeout");
  });

  it("can be turned off: every timeout is then its own", async () => {
    const { createDeadlineInterceptor } = await import("../rpcDeadline");
    const { pageActivity } = await import("../../lib/pageActivity");
    const onTimeout = vi.fn();
    const onStall = vi.fn();
    const deadline = createDeadlineInterceptor({
      timeoutFor: () => 10_000,
      activity: pageActivity,
      onTimeout,
      onStall,
      stallGrouping: false,
    });
    const { wire } = makeWire();
    const run = deadline.interceptor(wire);
    for (const method of ["A", "B"]) {
      void run({
        stream: false,
        service: { typeName: "reliant.v1.X" },
        method: { name: method },
        header: new Headers(),
        signal: new AbortController().signal,
        message: {},
      } as unknown as Req).catch(() => {});
    }
    await vi.advanceTimersByTimeAsync(10_100);
    expect(onStall).not.toHaveBeenCalled();
    expect(onTimeout.mock.calls.map((c) => c[0]).sort()).toEqual(["A", "B"]);
  });
});

// The stall is one incident until every call in it has settled: a member that
// times out after the first was aborted, or after another member was answered,
// is part of the incident already reported, not a report of its own.
describe("a reported stall", () => {
  async function stallHarness(budgets: Record<string, number> = {}) {
    const { createDeadlineInterceptor } = await import("../rpcDeadline");
    const { pageActivity } = await import("../../lib/pageActivity");
    const onTimeout = vi.fn();
    const onStall = vi.fn();
    const deadline = createDeadlineInterceptor({
      timeoutFor: (_service, method) => budgets[method] ?? 10_000,
      activity: pageActivity,
      onTimeout,
      onStall,
    });
    const { calls, wire } = makeWire();
    const run = deadline.interceptor(wire);
    const call = (method: string, signal = new AbortController().signal) => {
      const result = run({
        stream: false,
        service: { typeName: "reliant.v1.X" },
        method: { name: method },
        header: new Headers(),
        signal,
        message: {},
      } as unknown as Req);
      result.catch(() => {});
      return result;
    };
    return { deadline, calls, call, onTimeout, onStall };
  }

  it("is not reported again when another member's deadline passes", async () => {
    // D has the longer budget, so its deadline passes well after the stall
    // was reported, the timed-out call aborted and B answered.
    const { deadline, calls, call, onTimeout, onStall } = await stallHarness({ UpdateD: 20_000 });
    for (const method of ["UpdateA", "UpdateB", "UpdateC", "UpdateD"]) {
      call(method);
      await vi.advanceTimersByTimeAsync(300);
    }
    await flush();
    expect(calls).toHaveLength(4);

    // A runs out of budget first: one stall, all four calls in it.
    await vi.advanceTimersByTimeAsync(10_000 - 1_200 + 50);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onStall.mock.calls[0][0].methods).toEqual(["UpdateA", "UpdateB", "UpdateC", "UpdateD"]);

    calls[1].resolve({});
    await flush();
    // C's and then D's deadlines pass.
    await vi.advanceTimersByTimeAsync(11_000);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onTimeout).not.toHaveBeenCalled();
    expect(deadline.describeInFlight()).toBe("0 in flight");
    expect(deadline.openStallIncidents()).toBe(0);
  });

  it("is released once every member settled, so a new hang is reported", async () => {
    const { deadline, call, onTimeout, onStall } = await stallHarness();
    call("UpdateA");
    call("UpdateB");
    await vi.advanceTimersByTimeAsync(10_100);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(deadline.openStallIncidents()).toBe(0);

    call("UpdateA");
    await vi.advanceTimersByTimeAsync(10_100);
    expect(onTimeout).toHaveBeenCalledTimes(1);
    expect(onTimeout.mock.calls[0][0]).toBe("UpdateA");

    call("UpdateA");
    call("UpdateB");
    await vi.advanceTimersByTimeAsync(10_100);
    expect(onStall).toHaveBeenCalledTimes(2);
    expect(deadline.openStallIncidents()).toBe(0);
  });

  it("is not reported again by hedged reads or by members that failed or were aborted", async () => {
    const { deadline, calls, call, onTimeout, onStall } = await stallHarness({ UpdateA: 10_000 });
    // Fast reads are hedged at 4s: a second request of the same call.
    const fast = (method: string, signal?: AbortSignal) => {
      const result = deadline.interceptor(((req: Req) => {
        return new Promise((resolve, reject) => {
          calls.push({ req, resolve: () => resolve({}), reject });
          req.signal.addEventListener("abort", () => reject(req.signal.reason));
        });
      }) as unknown as Next)({
        stream: false,
        service: { typeName: "reliant.v1.ChatService" },
        method: { name: method },
        header: new Headers(),
        signal: signal ?? new AbortController().signal,
        message: {},
      } as unknown as Req);
      result.catch(() => {});
      return result;
    };
    const callerOfE = new AbortController();
    call("UpdateA");
    await vi.advanceTimersByTimeAsync(300);
    fast("ListChats");
    fast("GetChat");
    call("UpdateD");
    call("UpdateE", callerOfE.signal);
    await vi.advanceTimersByTimeAsync(9_800);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onStall.mock.calls[0][0].methods).toEqual([
      "GetChat",
      "ListChats",
      "UpdateA",
      "UpdateD",
      "UpdateE",
    ]);

    // A member fails, another is aborted by its caller, the rest time out.
    const updateD = calls.find((c) => c.req.method.name === "UpdateD");
    updateD?.reject(new ConnectError("gone", Code.Unavailable));
    callerOfE.abort();
    await flush();
    await vi.advanceTimersByTimeAsync(11_000);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onTimeout).not.toHaveBeenCalled();
    expect(deadline.describeInFlight()).toBe("0 in flight");
    expect(deadline.openStallIncidents()).toBe(0);
  });

  // Review of #2929 (B1): the timed-out member is aborted and its promise
  // settles first, running its own cleanup; only then does another member's
  // deadline pass. The incident outlives that settle, so it is still one.
  it("is reported once when the timed-out member settles before another member's deadline", async () => {
    const { deadline, calls, call, onTimeout, onStall } = await stallHarness();
    let firstSettled = false;
    void call("UpdateA").catch(() => {
      firstSettled = true;
    });
    for (const method of ["UpdateB", "UpdateC", "UpdateD"]) {
      await vi.advanceTimersByTimeAsync(300);
      call(method);
    }
    await flush();
    expect(calls).toHaveLength(4);

    // A's deadline passes (B's is 300ms away): one stall of all four.
    await vi.advanceTimersByTimeAsync(10_000 - 900 + 50);
    await flush();
    expect(firstSettled).toBe(true);
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onStall.mock.calls[0][0].methods).toEqual(["UpdateA", "UpdateB", "UpdateC", "UpdateD"]);
    expect(deadline.describeInFlight()).toMatch(/^3 in flight/);
    expect(deadline.openStallIncidents()).toBe(1);

    // Now B's, C's and D's deadlines pass, one at a time.
    for (let i = 0; i < 3; i++) {
      await vi.advanceTimersByTimeAsync(300);
      await flush();
    }
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onTimeout).not.toHaveBeenCalled();
    expect(deadline.describeInFlight()).toBe("0 in flight");
    expect(deadline.openStallIncidents()).toBe(0);
  });

  it("does not take in a call that started after it was reported, nor report itself again for it", async () => {
    const { deadline, calls, call, onTimeout, onStall } = await stallHarness({
      UpdateB: 20_000,
      UpdateC: 20_000,
      UpdateD: 20_000,
    });
    for (const method of ["UpdateA", "UpdateB", "UpdateC", "UpdateD"]) {
      call(method);
      await vi.advanceTimersByTimeAsync(300);
    }
    await vi.advanceTimersByTimeAsync(10_000 - 1_200 + 50);
    await flush();
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(deadline.describeInFlight()).toMatch(/^3 in flight/);

    // A has settled. A new call hangs beside B, C and D, still unanswered.
    call("ListNew");
    await flush();
    expect(calls).toHaveLength(5);

    // The new call's deadline passes first, then B's, C's and D's. B, C and D
    // belong to the open incident, so the new call is a timeout on its own,
    // and the incident is never reported again.
    await vi.advanceTimersByTimeAsync(11_000);
    await flush();
    expect(onStall).toHaveBeenCalledTimes(1);
    expect(onTimeout).toHaveBeenCalledTimes(1);
    expect(onTimeout.mock.calls[0][0]).toBe("ListNew");
    expect(deadline.describeInFlight()).toBe("0 in flight");
    expect(deadline.openStallIncidents()).toBe(0);
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
    expect(reporting.reportError).not.toHaveBeenCalled();
  });
});
