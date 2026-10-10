/**
 * Which RPC failures the transport reports to Sentry, run through the real
 * interceptor chain. Each case is a prod Sentry issue (2026-10-07/08) that was
 * reported as a crash but was not a defect in the request that failed.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import type { Interceptor } from "@connectrpc/connect";

const sentry = vi.hoisted(() => ({
  captureException: vi.fn(),
  captureMessage: vi.fn(),
}));
vi.mock("@sentry/react", () => sentry);

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

type Next = Parameters<Interceptor>[0];
type Req = Parameters<Next>[0];

async function chainFailingWith(failure: (req: Req) => unknown) {
  const { buildInterceptors } = await import("../transport");
  const wire = (async (req: Req) => {
    throw failure(req);
  }) as unknown as Next;
  const run = buildInterceptors({ withAuth: false }).reduceRight<Next>(
    (next, interceptor) => interceptor(next),
    wire,
  );
  return (service: string, method: string, signal = new AbortController().signal) =>
    run({
      stream: false,
      service: { typeName: service },
      method: { name: method },
      header: new Headers(),
      signal,
      message: {},
    } as unknown as Req);
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

describe("the machine is still starting (ELECTRON-AV)", () => {
  it("is a wait the UI renders, not a crash report", async () => {
    const call = await chainFailingWith(
      () =>
        new ConnectError(
          "resolving daemon for command: your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
          Code.Unavailable,
        ),
    );
    await expect(call("reliant.v1.FileSystemService", "GetFileTree")).rejects.toMatchObject({
      code: Code.Unavailable,
    });
    expect(sentry.captureException).not.toHaveBeenCalled();
  });

  it("still reports an Unavailable that is not a machine wait", async () => {
    const call = await chainFailingWith(() => new ConnectError("upstream connect error", Code.Unavailable));
    await expect(call("reliant.v1.ChatService", "ListChats")).rejects.toBeInstanceOf(ConnectError);
    expect(sentry.captureException).toHaveBeenCalledTimes(1);
  });
});

describe("an anonymous account asked for a cloud machine", () => {
  it("is not reported to Sentry", async () => {
    const call = await chainFailingWith(
      () =>
        new ConnectError(
          "sign in with an email account to start a cloud machine",
          Code.FailedPrecondition,
          new Headers({ "x-reliant-reason": "account_required" }),
        ),
    );
    await expect(call("controlplane.v1.DaemonService", "CreateDaemon")).rejects.toMatchObject({
      code: Code.FailedPrecondition,
    });
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});

describe("a request its owner aborted (ELECTRON-8X, ELECTRON-74)", () => {
  it("does not report the timeout error connect aborts a request with", async () => {
    const controller = new AbortController();
    const timeout = new ConnectError("ListProcesses timed out after 10000ms", Code.DeadlineExceeded);
    const call = await chainFailingWith((req) => {
      controller.abort(timeout);
      return req.signal.reason;
    });
    await expect(
      call("reliant.v1.PackageCommandsService", "ListProcesses", controller.signal),
    ).rejects.toBe(timeout);
    expect(sentry.captureException).not.toHaveBeenCalled();
  });

  it("does not report connect-web's bare 'missing request message' string, and surfaces a ConnectError", async () => {
    const controller = new AbortController();
    const call = await chainFailingWith(() => {
      controller.abort();
      return "missing request message";
    });
    const error = await call("reliant.v1.StreamingService", "StreamUserUpdates", controller.signal).catch(
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(ConnectError);
    expect((error as ConnectError).code).toBe(Code.Canceled);
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});

describe("an app older than the server (ELECTRON-B3)", () => {
  afterEach(async () => {
    const { useModalStore } = await import("../../store/modalStore");
    useModalStore.getState().closeModal();
  });

  it("prompts to update instead of reporting a crash", async () => {
    // What connect-web throws for a route the server does not serve: the
    // api-server's mux answered "404 page not found" as text/plain.
    const call = await chainFailingWith(() => new ConnectError("HTTP 404", Code.Unimplemented));
    await expect(call("reliant.v1.ChatService", "CreateChat")).rejects.toMatchObject({
      code: Code.Unimplemented,
    });

    expect(sentry.captureException).not.toHaveBeenCalled();
    expect(sentry.captureMessage).toHaveBeenCalledWith(
      "version-skew: reliant.v1.ChatService/CreateChat is not served",
      expect.objectContaining({ level: "warning" }),
    );
    const { useModalStore } = await import("../../store/modalStore");
    await vi.waitFor(() => expect(useModalStore.getState().activeModal).toBe("app-out-of-date"));
  });

  it("does not treat another backend's Unimplemented as this app being old", async () => {
    // The control plane answers Unimplemented when IT predates a service; the
    // forge surfaces render that as "not configured".
    const call = await chainFailingWith(() => new ConnectError("HTTP 404", Code.Unimplemented));
    await expect(call("controlplane.v1.DeployService", "GetLiveView")).rejects.toBeInstanceOf(ConnectError);
    expect(sentry.captureMessage).not.toHaveBeenCalledWith(
      expect.stringContaining("version-skew"),
      expect.anything(),
    );
  });
});

describe("ELECTRON-74 end to end through connect-web", () => {
  // The real sequence: the stream's owner aborts (a reconnect superseded it)
  // while an earlier interceptor is still awaiting the auth token. connect
  // closes the request iterable on abort, and its fetch layer then finds it
  // empty and throws the STRING "missing request message".
  it("an aborted stream start is a cancellation, not a crash", async () => {
    const { createConnectTransport } = await import("@connectrpc/connect-web");
    const { buildInterceptors } = await import("../transport");
    const { StreamingService } = await import("../../gen/reliant/v1/streaming_pb");

    let releaseToken!: () => void;
    const tokenGate = new Promise<void>((resolve) => (releaseToken = resolve));
    const slowAuth: Interceptor = (next) => async (req) => {
      await tokenGate;
      return next(req);
    };
    const fetchSpy = vi.fn();
    const transport = createConnectTransport({
      baseUrl: "https://api.test",
      fetch: fetchSpy as unknown as typeof fetch,
      interceptors: [slowAuth, ...buildInterceptors({ withAuth: false })],
    });
    const client = createClient(StreamingService, transport);

    const controller = new AbortController();
    const stream = client.streamUserUpdates({}, { signal: controller.signal });
    const consumed = (async () => {
      for await (const event of stream) void event;
    })().catch((e: unknown) => e);

    controller.abort();
    releaseToken();
    const error = await consumed;

    expect(error).toBeInstanceOf(ConnectError);
    expect((error as ConnectError).code).toBe(Code.Canceled);
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(sentry.captureException).not.toHaveBeenCalled();
  });
});
