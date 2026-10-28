/**
 * The active org rides every request whose message has an `org_id` field,
 * through the REAL transport chain, and nothing else.
 *
 * What this pins is the product rule: the org a person selected is the org a
 * control-plane call acts in, with no call site having to remember it — and a
 * request that names none (no selection yet, or no field to put it in) goes out
 * untouched, so the server resolves the person's default. No "first org".
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup } from "@testing-library/react";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";

const state = vi.hoisted(() => ({ controlPlaneUrl: "https://admin.example.test" }));

vi.mock("@/services/controlPlane/config", () => ({
  get CONTROL_PLANE_API_URL() {
    return state.controlPlaneUrl;
  },
  get hasControlPlane() {
    return Boolean(state.controlPlaneUrl);
  },
}));
vi.mock("@sentry/react", () => ({ captureException: vi.fn(), captureMessage: vi.fn() }));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

let bodies: Array<{ url: string; body: Record<string, unknown> }>;

beforeEach(() => {
  vi.resetModules();
  bodies = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = init?.body ? new TextDecoder().decode(init.body as Uint8Array) : "{}";
      bodies.push({ url: String(input), body: JSON.parse(raw || "{}") as Record<string, unknown> });
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

async function load(activeOrg: string | null) {
  const connect = await import("@/lib/connect");
  const { setAuthTokenProvider } = await import("@/api/authProvider");
  setAuthTokenProvider({ getToken: async () => "t", hasSession: async () => true, refresh: async () => null });
  const store = await import("@/store/activeOrgStore");
  store.bindActiveOrgUser("user-1");
  if (activeOrg) store.chooseActiveOrg(activeOrg);
  return { ...connect, ...store };
}

describe("orgInterceptor", () => {
  it.each([
    ["SetCurrentUserWalletAutoRecharge", (client: ReturnType<typeof import("@/lib/connect").connectClient<typeof BillingService>>) => client.setCurrentUserWalletAutoRecharge({ enabled: true, thresholdCents: 1000n, amountCents: 2500n, maxPerMonthCents: 10000n })],
    ["CreateCurrentUserWalletTopupSession", (client: ReturnType<typeof import("@/lib/connect").connectClient<typeof BillingService>>) => client.createCurrentUserWalletTopupSession({ amountCents: 2500n, successUrl: "https://app.test/done", cancelUrl: "https://app.test/cancel" })],
    ["RedeemCoupon", (client: ReturnType<typeof import("@/lib/connect").connectClient<typeof BillingService>>) => client.redeemCoupon({ code: "TEST" })],
  ] as const)("pins implicit %s before deferred auth and a 401 retry", async (_method, invoke) => {
    const { connectClient, chooseActiveOrg } = await load("org-a");
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    let release!: (token: string) => void;
    const getToken = vi.fn(() => new Promise<string>((resolve) => { release = resolve; }));
    setAuthTokenProvider({ getToken, hasSession: async () => true, refresh: async () => "fresh-token" });
    const spending: Record<string, unknown> = { "org-a": "unchanged-a", "org-b": "unchanged-b" };
    const wire: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: unknown, init?: RequestInit) => {
      const body = JSON.parse(new TextDecoder().decode(init?.body as Uint8Array));
      wire.push(body);
      if (wire.length === 1) return new Response(JSON.stringify({ code: "unauthenticated", message: "expired" }), {
        status: 401, headers: { "content-type": "application/json" },
      });
      spending[body.orgId] = body;
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }));
    const pending = invoke(connectClient(BillingService));
    await vi.waitFor(() => expect(getToken).toHaveBeenCalledOnce());
    chooseActiveOrg("org-b");
    release("old-token");
    await pending;
    expect(wire.map((body) => body.orgId)).toEqual(["org-a", "org-a"]);
    expect(spending["org-a"]).not.toBe("unchanged-a");
    expect(spending["org-b"]).toBe("unchanged-b");
  });

  it.each([null, "org-explicit"])("retains initial org %s through deferred auth and retry", async (explicit) => {
    const { connectClient, chooseActiveOrg } = await load(null);
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    let release!: (token: string) => void;
    const getToken = vi.fn(() => new Promise<string>((resolve) => { release = resolve; }));
    setAuthTokenProvider({ getToken, hasSession: async () => true, refresh: async () => "fresh-token" });
    const wire: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: unknown, init?: RequestInit) => {
      wire.push(JSON.parse(new TextDecoder().decode(init?.body as Uint8Array)));
      return wire.length === 1
        ? new Response(JSON.stringify({ code: "unauthenticated", message: "expired" }), { status: 401, headers: { "content-type": "application/json" } })
        : new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }));
    const pending = connectClient(BillingService).redeemCoupon({ code: "TEST", ...(explicit ? { orgId: explicit } : {}) });
    await vi.waitFor(() => expect(getToken).toHaveBeenCalledOnce());
    chooseActiveOrg("org-b");
    release("old-token");
    await pending;
    expect(wire.map((body) => body.orgId)).toEqual([explicit ?? undefined, explicit ?? undefined]);
  });

  it("pins grant writes before deferred auth and retains that org across 401 retry", async () => {
    const { chooseActiveOrg } = await load("org-a");
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    let release!: (token: string) => void;
    const getToken = vi.fn(() => new Promise<string>((resolve) => { release = resolve; }));
    setAuthTokenProvider({ getToken, hasSession: async () => true, refresh: async () => "fresh-token" });
    // A canonical Connect endpoint model: one shared user, independent org
    // grants. The first attempt is refused before any write, as a real 401 is.
    const grants: Record<string, string[]> = { "org-a": ["deploy:read"], "org-b": ["secret:read"] };
    const wire: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: unknown, init?: RequestInit) => {
      const body = JSON.parse(new TextDecoder().decode(init?.body as Uint8Array));
      wire.push(body);
      if (wire.length === 1) return new Response(JSON.stringify({ code: "unauthenticated", message: "expired" }), {
        status: 401, headers: { "content-type": "application/json" },
      });
      grants[body.orgId] = body.scopes;
      return new Response(JSON.stringify({ member: { userId: body.userId, scopes: body.scopes } }), {
        status: 200, headers: { "content-type": "application/json" },
      });
    }));
    const { updateMemberPermissions } = await import("@/services/controlPlane/memberPermissions");
    const pending = updateMemberPermissions("shared-user", ["deploy:read", "deploy:write"], "org-a");
    await vi.waitFor(() => expect(getToken).toHaveBeenCalledOnce());
    chooseActiveOrg("org-b");
    release("old-token");
    await pending;
    expect(wire.map((body) => body.orgId)).toEqual(["org-a", "org-a"]);
    expect(grants["org-a"]).toEqual(["deploy:read", "deploy:write"]);
    expect(grants["org-b"]).toEqual(["secret:read"]);
  });

  it("keeps a created secret's org and rotated/revoked resource identity through deferred auth", async () => {
    const { chooseActiveOrg } = await load("org-a");
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    let release!: (token: string) => void;
    const token = new Promise<string>((resolve) => { release = resolve; });
    const getToken = vi.fn(() => token);
    setAuthTokenProvider({ getToken, hasSession: async () => true, refresh: async () => null });
    const { createManagedLLMKey, rotateLLMKey, revokeLLMKey } = await import("@/services/controlPlane/reliantAI");
    const pending = [
      createManagedLLMKey({ orgId: "org-a", name: "App", models: [] }),
      rotateLLMKey("org-a-key", "24h"),
      revokeLLMKey("org-a-key"),
    ];
    await vi.waitFor(() => expect(getToken).toHaveBeenCalledTimes(3));
    chooseActiveOrg("org-b");
    release("token");
    await Promise.all(pending);
    expect(bodies.find((b) => b.url.endsWith("/CreateLLMKey"))?.body.orgId).toBe("org-a");
    for (const method of ["RotateLLMKey", "RevokeLLMKey"]) {
      const body = bodies.find((b) => b.url.endsWith(`/${method}`))?.body;
      expect(body?.keyId).toBe("org-a-key");
      expect(body).not.toHaveProperty("orgId"); // canonical resource-bound RPCs
    }
  });

  it("names the active org on a control-plane request that carries org_id", async () => {
    const { connectClient } = await load("org-b");
    await connectClient(DeployService).listEnvironments({});
    await connectClient(BillingService).getCurrentUserWalletOverview({ ledgerPage: 1, ledgerPageSize: 10 });
    expect(bodies.map((b) => b.body.orgId)).toEqual(["org-b", "org-b"]);
  });

  it("names the org on ForgeService requests, which hand it to forge's credential helper", async () => {
    const { connectClient } = await load("org-b");
    await connectClient(ForgeService).planDeploy({ projectId: "p", env: "prod" });
    expect(bodies[0]?.body.orgId).toBe("org-b");
  });

  it("names nothing when no org is selected, so the server uses the default", async () => {
    const { connectClient } = await load(null);
    await connectClient(DeployService).listEnvironments({});
    expect(bodies[0]?.body).not.toHaveProperty("orgId");
  });

  it("leaves a request with no org_id field alone", async () => {
    const { connectClient } = await load("org-b");
    await connectClient(OrganizationService).listMyOrganizations({});
    await connectClient(SettingsService).listSettings({});
    for (const b of bodies) expect(b.body, b.url).not.toHaveProperty("orgId");
  });

  it("keeps an org the caller named itself", async () => {
    const { connectClient } = await load("org-b");
    await connectClient(DeployService).listEnvironments({ orgId: "org-explicit" });
    expect(bodies[0]?.body.orgId).toBe("org-explicit");
  });

  it("follows a switch on the very next request", async () => {
    const { connectClient, chooseActiveOrg } = await load("org-a");
    await connectClient(DeployService).listEnvironments({});
    chooseActiveOrg("org-b");
    await connectClient(DeployService).listEnvironments({});
    expect(bodies.map((b) => b.body.orgId)).toEqual(["org-a", "org-b"]);
  });
});
