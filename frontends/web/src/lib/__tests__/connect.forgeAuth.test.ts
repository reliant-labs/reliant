/**
 * forge sign-in (AUTH_PROVIDER "forge"): every RPC goes to this page's own
 * origin under forge's named-API paths, carries the session cookie
 * (`credentials: "include"`) and NO Authorization header. Supabase mode is
 * covered by connect.routing.test.ts and must not change.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createClient } from "@connectrpc/connect";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

const state = vi.hoisted(() => ({ controlPlaneUrl: "https://admin.example.test" }));

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

interface Sent {
  url: string;
  headers: Headers;
  credentials: RequestCredentials | undefined;
}
let sent: Sent[];

function stubFetch() {
  sent = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push({ url: String(input), headers: new Headers(init?.headers), credentials: init?.credentials });
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
}

function setConfig(values: Record<string, unknown>) {
  (window as unknown as { __FORGE_CONFIG__?: unknown }).__FORGE_CONFIG__ = values;
}

beforeAll(async () => {
  await import("../connect");
}, 60_000);

beforeEach(() => {
  vi.resetModules();
  stubFetch();
});

afterEach(() => {
  vi.unstubAllGlobals();
  delete (window as unknown as { __FORGE_CONFIG__?: unknown }).__FORGE_CONFIG__;
});

describe("forge sign-in transport", () => {
  it("sends reliant RPCs same-origin under /_api/@reliant with the cookie and no bearer", async () => {
    setConfig({ AUTH_PROVIDER: "forge", API_URL: "https://api.example.test" });
    const { transport } = await import("../connect");
    await createClient(SettingsService, transport).listSettings({});
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(`${window.location.origin}/_api/@reliant/reliant.v1.SettingsService/ListSettings`);
    expect(sent[0].credentials).toBe("include");
    expect(sent[0].headers.has("Authorization")).toBe(false);
  });

  it("sends control-plane RPCs same-origin under /_api/@cp with the cookie and no bearer", async () => {
    setConfig({ AUTH_PROVIDER: "forge", CONTROL_PLANE_API_URL: state.controlPlaneUrl });
    const { transport } = await import("../connect");
    await createClient(UserService, transport).getCurrentUser({});
    expect(sent[0].url).toBe(
      `${window.location.origin}/_api/@cp/controlplane.v1.UserService/GetCurrentUser`,
    );
    expect(sent[0].credentials).toBe("include");
    expect(sent[0].headers.has("Authorization")).toBe(false);
  });

  it("uses the API bases config.js names", async () => {
    setConfig({ AUTH_PROVIDER: "forge", apis: { reliant: "/custom/reliant/", cp: "https://cp.example.test/_api/@cp" } });
    const { reliantApiBaseUrl, controlPlaneBaseUrl } = await import("../connect");
    expect(reliantApiBaseUrl()).toBe(`${window.location.origin}/custom/reliant`);
    expect(controlPlaneBaseUrl()).toBe("https://cp.example.test/_api/@cp");
  });

  it("keeps the bearer and the cookie-less fetch in supabase mode", async () => {
    setConfig({ AUTH_PROVIDER: "supabase" });
    const connect = await import("../connect");
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    setAuthTokenProvider({ getToken: async () => "test-token", hasSession: async () => true, refresh: async () => null });
    await createClient(SettingsService, connect.transport).listSettings({});
    expect(sent[0].url).not.toContain("/_api/@");
    expect(sent[0].credentials).toBeUndefined();
    expect(sent[0].headers.get("Authorization")).toBe("Bearer test-token");
  });
});
