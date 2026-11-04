/**
 * Errors through the FULL chain: connectClient → routed transport →
 * buildInterceptors (forge's error normalization outermost) → connect-web →
 * fetch, with only fetch faked.
 *
 * The chain now throws forge's ConnectClientError, the type the generated
 * hooks declare. The app has ~77 call sites written against connect-es —
 * `err instanceof ConnectError`, the numeric `err.code`, `err.metadata`,
 * `err.findDetails(...)`, `err.rawMessage` — so these pin that each of those
 * still answers exactly as it did on the raw error, by running real readers
 * from the app (accountRequired, daemon-errors, rpcErrorPolicy) on it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { ConnectClientError } from "@reliantlabs/forge-web-runtime/interceptors";
import { DaemonRegistryService, DaemonWakingSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import { ChatService } from "@/gen/reliant/v1/chat_pb";

const reporting = vi.hoisted(() => ({
  reportError: vi.fn(),
  reportFailure: vi.fn(),
}));
vi.mock("@/lib/errorReporting", () => reporting);
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

function base64(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/=+$/, "");
}

/** fetch answers every call with this Connect unary error. */
function serverFails(
  status: number,
  body: { code: string; message: string; details?: unknown[] },
  headers: Record<string, string> = {},
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "content-type": "application/json", ...headers },
        }),
    ),
  );
}

async function load() {
  const connect = await import("../connect");
  const { setAuthTokenProvider } = await import("@/api/authProvider");
  setAuthTokenProvider({
    getToken: async () => "test-token",
    hasSession: async () => true,
    refresh: async () => null,
  });
  return connect;
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a server error, through the full chain", () => {
  const message = "sign in with an email account to start a cloud machine";
  const waking = base64(toBinary(DaemonWakingSchema, create(DaemonWakingSchema, { daemonId: "d-1" })));

  async function failedCall(): Promise<unknown> {
    serverFails(
      400,
      {
        code: "failed_precondition",
        message,
        details: [{ type: "reliant.v1.DaemonWaking", value: waking }],
      },
      { "x-forge-error-reason": "account_required" },
    );
    const { connectClient } = await load();
    return connectClient(DaemonRegistryService).listDaemons({}).catch((e: unknown) => e);
  }

  it("is still a ConnectError with its numeric code — and the runtime's typed error", async () => {
    const err = await failedCall();
    expect(err).toBeInstanceOf(ConnectError);
    expect(err).toBeInstanceOf(ConnectClientError);
    const e = err as ConnectClientError;
    expect(e.code).toBe(Code.FailedPrecondition);
    expect(e.codeName).toBe("failed_precondition");
    expect(e.reason).toBe("account_required");
    // ConnectError.from is the idiom several catch blocks use; it must not re-wrap.
    expect(ConnectError.from(e)).toBe(e);
  });

  it("keeps metadata, details and rawMessage, so the app's readers answer as before", async () => {
    const err = await failedCall();
    const { isAccountRequiredError } = await import("@/lib/accountRequired");
    const { wakingDaemonId } = await import("@/lib/daemon-errors");

    expect(isAccountRequiredError(err)).toBe(true);
    expect(wakingDaemonId(err)).toBe("d-1");
    const e = err as ConnectError;
    expect(e.rawMessage).toBe(message);
    // The "[code] " prefix is gone from .message; the strip several billing
    // surfaces apply is therefore a no-op, not a mangle.
    expect(e.message).toBe(message);
    expect(e.message.replace(/^\[[a-z_]+\]\s*/i, "")).toBe(message);
  });

  it("is classified by rpcErrorPolicy as before (a machine wait is not a crash report)", async () => {
    serverFails(
      503,
      { code: "unavailable", message: "your machine is still starting: no daemon connected" },
      { "x-forge-error-reason": "daemon_starting" },
    );
    const { connectClient } = await load();
    const err = await connectClient(ChatService)
      .listChats({})
      .catch((e: unknown) => e);

    const { isDaemonConnectingError } = await import("@/lib/daemon-errors");
    const { classifyRpcFailure } = await import("@/api/rpcErrorPolicy");
    expect(err).toBeInstanceOf(ConnectError);
    expect((err as ConnectError).code).toBe(Code.Unavailable);
    expect(isDaemonConnectingError(err)).toBe(true);
    expect(
      classifyRpcFailure(err, { serviceTypeName: ChatService.typeName, signalAborted: false }),
    ).toBe("machine-wait");
    expect(reporting.reportError).not.toHaveBeenCalled();
  });

  it("reaches a generated hook's error as a ConnectError", async () => {
    serverFails(404, { code: "not_found", message: "no such question" });
    await load();
    const { useListPendingQuestions } = await import("@/hooks/question-service-hooks_gen");
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);

    const { result } = renderHook(() => useListPendingQuestions({}), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("error"));

    expect(result.current.error).toBeInstanceOf(ConnectError);
    expect(result.current.error?.code).toBe(Code.NotFound);
    expect(result.current.error?.codeName).toBe("not_found");
    queryClient.clear();
  });
});

describe("a failure that never reached a server", () => {
  it("a network fault is a ConnectError too", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    const { connectClient } = await load();
    const err = await connectClient(ChatService)
      .listChats({})
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConnectError);
    expect(err).toBeInstanceOf(ConnectClientError);
  });
});
