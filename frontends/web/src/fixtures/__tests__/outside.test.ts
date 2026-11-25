import { afterEach, describe, expect, it, vi } from "vitest";

import { setRuntimeConfig } from "@/test/runtimeConfig";

import { classifyOutside, fakeSession, installOutside, SESSION_STORAGE_KEY, type OutsideContext } from "../outside";

const CTX: OutsideContext = { origin: "https://app.preview.test", supabaseOrigin: "https://preview.supabase.co", telemetryOrigin: null };
const kind = (href: string, method = "GET", ctx = CTX) => classifyOutside(new URL(href), method, ctx);

describe("classifyOutside", () => {
  it("names an RPC URL on any origin as an escaped rpc", () => {
    expect(kind("https://api.preview.invalid/reliant.v1.ChatService/ListChats", "POST")).toBe("rpc");
    expect(kind("https://app.preview.test/controlplane.v1.BillingService/ListPlans", "POST")).toBe("rpc");
    expect(kind("https://app.preview.test/observability_billing.v1.ObservabilityBillingService/GetObservabilityUsage", "POST")).toBe("rpc");
  });

  it("allows the configured Supabase project's /auth/v1, and nothing else of it", () => {
    expect(kind("https://preview.supabase.co/auth/v1/user")).toBe("auth");
    expect(kind("https://preview.supabase.co/auth/v1/token?grant_type=refresh_token", "POST")).toBe("auth");
    expect(kind("https://preview.supabase.co/rest/v1/profiles")).toBe("refused");
    expect(kind("https://other.supabase.co/auth/v1/user")).toBe("refused");
  });

  it("allows health only on the page or a configured backend origin, for GETs", () => {
    const ctx = { ...CTX, backendOrigins: ["https://api.preview.test", "https://cp.preview.test", "https://gw.preview.test"] };
    expect(kind("https://api.preview.test/health", "GET", ctx)).toBe("health");
    expect(kind("https://cp.preview.test/healthz", "GET", ctx)).toBe("health");
    expect(kind("https://gw.preview.test/readyz", "GET", ctx)).toBe("health");
    expect(kind("https://other.preview.test/health", "GET", ctx)).toBe("refused");
    expect(kind("https://api.preview.test.attacker.test/health", "GET", ctx)).toBe("refused");
    expect(kind("https://api.preview.test/health", "POST", ctx)).toBe("refused");
    expect(kind("https://api.preview.test:8443/health", "GET", ctx)).toBe("refused");
    expect(kind("http://api.preview.test/health", "GET", ctx)).toBe("refused");
    expect(kind("https://api.preview.test/other", "GET", ctx)).toBe("refused");
  });

  it("allows health and the telemetry sinks", () => {
    expect(kind("https://app.preview.test/healthz")).toBe("health");
    expect(kind("https://app.preview.test/__forge/log", "POST")).toBe("telemetry");
    expect(kind("https://app.preview.test/_otel/v1/traces", "POST")).toBe("telemetry");
    expect(kind("https://app.preview.test/v1/logs", "POST")).toBe("telemetry");
    expect(kind("https://otel.example.test/v1/traces", "POST", { ...CTX, telemetryOrigin: "https://otel.example.test" })).toBe("telemetry");
  });

  it.each([CTX.origin, "https://otel.example.test"])("scopes configured telemetry on %s to POST signal paths", (origin) => {
    const ctx = { ...CTX, telemetryOrigin: origin, telemetryPath: "/k/fixture-key" };
    for (const signal of ["traces", "logs", "metrics"]) {
      const url = `${origin}/k/fixture-key/v1/${signal}`;
      expect(kind(url, "POST", ctx)).toBe("telemetry");
      for (const method of ["GET", "PUT", "DELETE", "OPTIONS"]) {
        expect(kind(url, method, ctx)).toBe(origin === CTX.origin && method === "GET" ? "page" : "refused");
      }
    }
    for (const path of ["/anything", "/k/fixture-key", "/k/fixture-key/other", "/k/fixture-key/v1/logs/extra", "/k/fixture-key-other/v1/logs"]) {
      expect(kind(`${origin}${path}`, "POST", ctx)).toBe("refused");
    }
    expect(kind(`${origin}/_otel/arbitrary`, "POST", ctx)).toBe("refused");
    expect(kind(`${origin}/reliant.v1.ChatService/ListChats`, "POST", ctx)).toBe("rpc");
  });

  it.each([
    "/health", "/healthz", "/readyz", "/__forge/log", "/_otel/v1/traces",
    "/v1/traces", "/v1/logs", "/v1/metrics",
  ])("refuses cross-origin lookalike sink %s", (path) => {
    expect(kind(`https://outside.example.test${path}`, "POST")).toBe("refused");
    expect(kind(`https://outside.example.test${path}`)).toBe("refused");
  });

  it.each([
    "https://eu.i.posthog.com/e/",
    "https://posthog.attacker.test/e/",
    "https://sentry.attacker.test/envelope/",
    "https://otel.example.test:8443/v1/logs",
    "http://otel.example.test/v1/logs",
  ])("refuses unconfigured telemetry origin %s", (url) => {
    expect(kind(url, "POST", { ...CTX, telemetryOrigin: "https://otel.example.test" })).toBe("refused");
  });

  it("passes the page's own GETs, and refuses everything else", () => {
    expect(kind("https://app.preview.test/assets/index-abc.js")).toBe("page");
    expect(kind("https://app.preview.test/config.js")).toBe("page");
    expect(kind("https://app.preview.test/api/upload", "POST")).toBe("refused");
    expect(kind("https://api.github.com/repos/acme/shop")).toBe("refused");
    expect(kind("https://gw.preview.invalid/ws")).toBe("refused");
  });
});

describe("installOutside", () => {
  let restore: (() => void) | undefined;
  afterEach(() => {
    restore?.();
    restore = undefined;
    localStorage.clear();
  });

  it("writes the fake session first, answers auth, and throws on an escaped rpc or a refused request", async () => {
    setRuntimeConfig({ SUPABASE_URL: "https://preview.supabase.co" });
    const real = vi.fn(async () => new Response("asset"));
    window.fetch = real as unknown as typeof window.fetch;
    const blocked: string[] = [];
    const user = { id: "u-1", email: "alex@example.com", name: "Alex" };
    restore = installOutside({ user, now: Date.UTC(2026, 10, 18), onBlocked: (k, r) => blocked.push(`${k} ${r}`) });

    const stored = JSON.parse(localStorage.getItem(SESSION_STORAGE_KEY)!);
    expect(stored.user.id).toBe("u-1");
    expect(await (await fetch("https://preview.supabase.co/auth/v1/user")).json()).toMatchObject({ id: "u-1", email: "alex@example.com" });

    await expect(fetch(`${window.location.origin}/reliant.v1.ChatService/ListChats`, { method: "POST" })).rejects.toThrow(/escaped the fixture transport/);
    await expect(fetch("https://api.github.com/user")).rejects.toThrow(/network outside the page is off/);
    expect(blocked).toEqual([`rpc POST ${window.location.origin}/reliant.v1.ChatService/ListChats`, "refused GET https://api.github.com/user"]);

    await fetch("/config.js");
    expect(real).toHaveBeenCalledTimes(1);
  });

  it("answers the configured backends' health probes from config.js, and blocks another origin's", async () => {
    setRuntimeConfig({ API_URL: "https://api.visual.invalid", CONTROL_PLANE_API_URL: "https://cp.visual.invalid", GATEWAY_URL: "https://gw.visual.invalid" });
    const real = vi.fn();
    window.fetch = real as unknown as typeof window.fetch;
    const blocked = vi.fn();
    restore = installOutside({ user: { id: "u", email: "e@x.test", name: "N" }, now: Date.UTC(2026, 10, 18), onBlocked: blocked });

    for (const url of ["https://api.visual.invalid/health", "https://cp.visual.invalid/healthz", "https://gw.visual.invalid/readyz"]) {
      const res = await fetch(url);
      expect(res.status, url).toBe(200);
      expect(await res.json()).toEqual({ status: "ok" });
    }
    await expect(fetch("https://other.visual.invalid/health")).rejects.toThrow(/network outside the page is off/);
    await expect(fetch("https://api.visual.invalid.attacker.test/health")).rejects.toThrow(/network outside the page is off/);
    expect(blocked.mock.calls).toEqual([
      ["refused", "GET https://other.visual.invalid/health"],
      ["refused", "GET https://api.visual.invalid.attacker.test/health"],
    ]);
    expect(real).not.toHaveBeenCalled();
  });

  it("records cross-origin health and telemetry lookalikes without fetching them", async () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "https://otel.example.test/k/fixture-key/" });
    const real = vi.fn();
    window.fetch = real as unknown as typeof window.fetch;
    const blocked = vi.fn();
    restore = installOutside({ user: { id: "u", email: "e@x.test", name: "N" }, now: Date.UTC(2026, 10, 18), onBlocked: blocked });

    const urls = [
      "https://outside.example.test/healthz",
      "https://outside.example.test/v1/logs",
      "https://posthog.attacker.test/e/",
      "https://otel.example.test/anything",
      "https://otel.example.test/k/fixture-key/v1/logs",
    ];
    for (const url of urls) {
      await expect(fetch(url)).rejects.toThrow(/network outside the page is off/);
    }
    expect(blocked.mock.calls).toEqual(urls.map((url) => ["refused", `GET ${url}`]));
    expect((await fetch(`${window.location.origin}/healthz`)).status).toBe(200);
    expect((await fetch("https://otel.example.test/k/fixture-key/v1/logs", { method: "POST" })).status).toBe(204);
    await expect(fetch("https://otel.example.test/anything", { method: "POST" })).rejects.toThrow(/network outside the page is off/);
    expect(blocked).toHaveBeenLastCalledWith("refused", "POST https://otel.example.test/anything");
    expect(real).not.toHaveBeenCalled();
  });

  it("issues the session at the pack clock, valid for a month", () => {
    const now = Date.UTC(2026, 10, 18, 15, 30);
    const session = fakeSession({ id: "u", email: "e@x.test", name: "N" }, now);
    expect(session.expires_at).toBe(now / 1000 + 86400 * 30);
    const claims = JSON.parse(atob(session.access_token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/")));
    expect(claims).toMatchObject({ sub: "u", iat: now / 1000 });
  });
});
