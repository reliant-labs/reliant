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

  it("allows health and the telemetry sinks", () => {
    expect(kind("https://app.preview.test/healthz")).toBe("health");
    expect(kind("https://app.preview.test/__forge/log", "POST")).toBe("telemetry");
    expect(kind("https://app.preview.test/_otel/v1/traces", "POST")).toBe("telemetry");
    expect(kind("https://app.preview.test/v1/logs", "POST")).toBe("telemetry");
    expect(kind("https://otel.example.test/anything", "POST", { ...CTX, telemetryOrigin: "https://otel.example.test" })).toBe("telemetry");
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

  it("records cross-origin health and telemetry lookalikes without fetching them", async () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "https://otel.example.test" });
    const real = vi.fn();
    window.fetch = real as unknown as typeof window.fetch;
    const blocked = vi.fn();
    restore = installOutside({ user: { id: "u", email: "e@x.test", name: "N" }, now: Date.UTC(2026, 10, 18), onBlocked: blocked });

    const urls = [
      "https://outside.example.test/healthz",
      "https://outside.example.test/v1/logs",
      "https://posthog.attacker.test/e/",
    ];
    for (const url of urls) {
      await expect(fetch(url)).rejects.toThrow(/network outside the page is off/);
    }
    expect(blocked.mock.calls).toEqual(urls.map((url) => ["refused", `GET ${url}`]));
    expect((await fetch(`${window.location.origin}/healthz`)).status).toBe(200);
    expect((await fetch("https://otel.example.test/v1/logs", { method: "POST" })).status).toBe(204);
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
