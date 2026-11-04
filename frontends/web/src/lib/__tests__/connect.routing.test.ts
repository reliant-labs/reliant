/**
 * src/lib/connect.ts decides which backend every RPC dials. These run the
 * REAL transports (connect-web, the full interceptor chain) and look at what
 * reaches fetch, one test per route, plus the routing decisions a packaged
 * build depends on and dev can never show.
 *
 * Packaged-only regressions are the reason this file exists. In dev the
 * renderer is same-origin and the Vite proxy sends `/reliant.v1.*` to
 * reliant-api, so a service routed at the wrong host still works by accident.
 * The packaged app dials absolute URLs, where admin-server answers
 * `/reliant.v1.DaemonRegistryService/*` with HTTP 200 from a DIFFERENT
 * database ("you have no daemon") and does not serve TokenService at all.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import type { DescService } from "@bufbuild/protobuf";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";
import { TokenService } from "@/gen/reliant/v1/token_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { SystemService } from "@/gen/reliant/v1/system_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { ObservabilityBillingService } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

const state = vi.hoisted(() => ({ controlPlaneUrl: "" }));

vi.mock("@/services/controlPlane/config", () => ({
  get CONTROL_PLANE_API_URL() {
    return state.controlPlaneUrl;
  },
  get hasControlPlane() {
    return Boolean(state.controlPlaneUrl);
  },
}));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

const CP_URL = "https://admin.example.test";
const DAEMON_URL = "http://127.0.0.1:54321";

interface Sent {
  url: string;
  headers: Headers;
}

let sent: Sent[];

/** A fetch that records each request and answers every unary call with `{}`. */
function stubFetch() {
  sent = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push({ url: String(input), headers: new Headers(init?.headers) });
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
}

function packaged(config: { grpcUrl?: string } | null) {
  vi.stubGlobal("location", { protocol: "app:", origin: "app://bundle", pathname: "/" });
  (window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG = config
    ? { isElectron: true, ...config }
    : undefined;
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
  state.controlPlaneUrl = "";
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  delete (window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG;
});

describe("routeFor", () => {
  it("never routes DaemonRegistryService, TokenService or ForgeService at control-plane", async () => {
    const { routeFor } = await import("../connect");
    const services: DescService[] = [DaemonRegistryService, TokenService, ForgeService];
    for (const service of services) {
      for (const method of service.methods) {
        expect(routeFor(service.typeName, method.name), `${service.typeName}/${method.name}`).toBe(
          "reliant-api",
        );
      }
    }
  });

  it("routes every reliant.v1 service to reliant's api-server", async () => {
    const { routeFor } = await import("../connect");
    const modules = import.meta.glob<Record<string, unknown>>("../../gen/reliant/v1/*_pb.ts", {
      eager: true,
    });
    let checked = 0;
    for (const mod of Object.values(modules)) {
      for (const value of Object.values(mod)) {
        const svc = value as Partial<DescService>;
        if (svc?.kind !== "service" || !svc.typeName || !svc.methods) continue;
        for (const method of svc.methods) {
          expect(["reliant-api", "reliant-stream", "reliant-unauth"]).toContain(
            routeFor(svc.typeName, method.name),
          );
          checked++;
        }
      }
    }
    expect(checked).toBeGreaterThan(100);
  });

  it("sends only the pre-session sign-in procedures unauthenticated", async () => {
    const { routeFor } = await import("../connect");
    const unauth = SystemService.methods
      .filter((m) => routeFor(SystemService.typeName, m.name) === "reliant-unauth")
      .map((m) => m.name)
      .sort();
    expect(unauth).toEqual(["DevAuthClear", "DevAuthLoad", "DevAuthSave", "StartOAuthSignIn"]);
    // auth_required: false on these means "the handler authenticates the
    // bearer itself", so they must still send one.
    expect(routeFor(TokenService.typeName, "ExchangeToken")).toBe("reliant-api");
    expect(routeFor(TokenService.typeName, "GetGitToken")).toBe("reliant-api");
  });
});

describe("one transport per route, through the real chain", () => {
  it("reliant-api: same-origin in dev, JSON, bearer + x-daemon-last-seen", async () => {
    const { connectClient } = await load();
    const { setDaemonLastSeen } = await import("@/api/transport");
    setDaemonLastSeen(1_700_000_000);

    await connectClient(SettingsService).listSettings({});

    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(`${window.location.origin}/reliant.v1.SettingsService/ListSettings`);
    expect(sent[0].headers.get("content-type")).toBe("application/json");
    expect(sent[0].headers.get("authorization")).toBe("Bearer test-token");
    expect(sent[0].headers.get("x-daemon-last-seen")).toBe("1700000000");
  });

  it("reliant-stream: StreamingService on the binary wire format", async () => {
    const { connectClient } = await load();

    const stream = connectClient(StreamingService).streamUserUpdates({});
    await (async () => {
      for await (const event of stream) void event;
    })().catch(() => undefined);

    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(`${window.location.origin}/reliant.v1.StreamingService/StreamUserUpdates`);
    expect(sent[0].headers.get("content-type")).toBe("application/connect+proto");
    expect(sent[0].headers.get("authorization")).toBe("Bearer test-token");
  });

  it("reliant-unauth: the sign-in procedures carry no credential", async () => {
    const { connectClient } = await load();

    await connectClient(SystemService).devAuthLoad({});

    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(`${window.location.origin}/reliant.v1.SystemService/DevAuthLoad`);
    expect(sent[0].headers.has("authorization")).toBe(false);
  });

  it("control-plane: bearer, but never x-daemon-last-seen (admin-server's CORS refuses it)", async () => {
    state.controlPlaneUrl = CP_URL;
    const { connectClient } = await load();
    const { setDaemonLastSeen } = await import("@/api/transport");
    setDaemonLastSeen(1_700_000_000);

    await connectClient(UserService).getCurrentUser({});

    expect(sent).toHaveLength(1);
    // Dev: same-origin, so Vite's /controlplane.v1.* proxy forwards it.
    expect(sent[0].url).toBe(`${window.location.origin}/controlplane.v1.UserService/GetCurrentUser`);
    expect(sent[0].headers.get("authorization")).toBe("Bearer test-token");
    expect(sent[0].headers.has("x-daemon-last-seen")).toBe(false);
  });

  it("observability billing's separate proto package routes to the control-plane with its auth chain", async () => {
    state.controlPlaneUrl = CP_URL;
    const { connectClient } = await load();
    await connectClient(ObservabilityBillingService).getObservabilityUsage({ orgId: "org" });
    expect(sent[0].url).toBe(`${window.location.origin}/observability_billing.v1.ObservabilityBillingService/GetObservabilityUsage`);
    expect(sent[0].headers.get("authorization")).toBe("Bearer test-token");
    expect(sent[0].headers.has("x-daemon-last-seen")).toBe(false);
  });

  it("control-plane without a configured URL fails the call, naming the variable", async () => {
    const { connectClient } = await load();
    await expect(connectClient(UserService).getCurrentUser({})).rejects.toThrow(
      /CONTROL_PLANE_API_URL/,
    );
    expect(sent).toHaveLength(0);
  });
});

describe("packaged desktop app (absolute URLs, no proxy)", () => {
  it("sends controlplane.v1 to admin-server and the registry + tokens to the daemon's api", async () => {
    state.controlPlaneUrl = CP_URL;
    packaged({ grpcUrl: DAEMON_URL });
    const { connectClient } = await load();

    await connectClient(UserService).getCurrentUser({});
    await connectClient(ObservabilityBillingService).getObservabilityUsage({ orgId: "org" });
    await connectClient(DaemonRegistryService).listDaemons({});
    await connectClient(TokenService).listTokens({});

    expect(sent.map((s) => s.url)).toEqual([
      `${CP_URL}/controlplane.v1.UserService/GetCurrentUser`,
      `${CP_URL}/observability_billing.v1.ObservabilityBillingService/GetObservabilityUsage`,
      `${DAEMON_URL}/reliant.v1.DaemonRegistryService/ListDaemons`,
      `${DAEMON_URL}/reliant.v1.TokenService/ListTokens`,
    ]);
  });

  it("a client built before the backend URL arrives reaches it once it does", async () => {
    packaged(null);
    const { connectClient, isApiReady } = await load();
    // Built at module load, exactly like every generated hook file's client.
    const client = connectClient(SettingsService);

    expect(isApiReady()).toBe(false);
    await expect(client.listSettings({})).rejects.toThrow(/not ready/);
    expect(sent).toHaveLength(0);

    packaged({ grpcUrl: DAEMON_URL });
    expect(isApiReady()).toBe(true);
    await client.listSettings({});
    expect(sent[0].url).toBe(`${DAEMON_URL}/reliant.v1.SettingsService/ListSettings`);

    // And it follows a later change (the daemon restarted on another port).
    packaged({ grpcUrl: "http://127.0.0.1:60000" });
    await client.listSettings({});
    expect(sent[1].url).toBe("http://127.0.0.1:60000/reliant.v1.SettingsService/ListSettings");
  });
});

describe("the test seam", () => {
  it("setTransport sends every route to the given transport; null restores routing", async () => {
    state.controlPlaneUrl = CP_URL;
    const { connectClient, setTransport } = await load();
    const { mockTransport } = await import("../test-utils");
    setTransport(
      mockTransport({
        "SettingsService.ListSettings": { response: {} },
        "UserService.GetCurrentUser": { response: {} },
      }),
    );

    await connectClient(SettingsService).listSettings({});
    await connectClient(UserService).getCurrentUser({});
    expect(sent).toHaveLength(0);

    setTransport(null);
    await connectClient(SettingsService).listSettings({});
    expect(sent).toHaveLength(1);
  });
});

describe("forge's generated hooks", () => {
  it("dispatch through connect.ts's routed transport", async () => {
    await load();
    const { useListPendingQuestions } = await import("@/hooks/question-service-hooks_gen");
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);

    const { result } = renderHook(() => useListPendingQuestions({}), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("success"));

    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(
      `${window.location.origin}/reliant.v1.QuestionService/ListPendingQuestions`,
    );
    expect(sent[0].headers.get("authorization")).toBe("Bearer test-token");
    queryClient.clear();
  });
});
