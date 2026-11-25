import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/auth/forge-api", () => ({ reliantForgeBase: () => "/_api/@reliant" }));

import { AuthError, fetchIdentity } from "../native-login";

const respond = (status: number, body: string) =>
  vi.stubGlobal("fetch", vi.fn(async () => new Response(body, { status, headers: { "Content-Type": "application/json" } })));

afterEach(() => {
  vi.unstubAllGlobals();
});

// Only a definitive answer about the session may resolve: anything that says
// nothing about it rejects, so a caller holding a session keeps it.
describe("fetchIdentity", () => {
  it("resolves with the broker's identity", async () => {
    respond(200, JSON.stringify({ authenticated: true, subject: "s-1" }));
    await expect(fetchIdentity()).resolves.toEqual({ authenticated: true, subject: "s-1" });
  });

  it("resolves signed out when the broker says so", async () => {
    respond(200, JSON.stringify({ authenticated: false }));
    await expect(fetchIdentity()).resolves.toEqual({ authenticated: false });
  });

  it("resolves signed out for a 401", async () => {
    respond(401, JSON.stringify({ error: "unauthenticated" }));
    await expect(fetchIdentity()).resolves.toEqual({ authenticated: false });
  });

  it("rejects when the broker cannot be reached", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    const err = await fetchIdentity().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AuthError);
    expect((err as AuthError).status).toBe(0);
  });

  it.each([
    ["a 503 (its session store is down)", 503, JSON.stringify({ error: "Sign-in is unavailable right now. Please try again." })],
    ["a 502 with no body", 502, ""],
    ["a 404", 404, "not found"],
    ["a 429", 429, JSON.stringify({ error: "slow down" })],
  ])("rejects for %s", async (_name, status, body) => {
    respond(status, body);
    await expect(fetchIdentity()).rejects.toBeInstanceOf(AuthError);
  });

  it.each([
    ["invalid JSON", "<html>proxy error</html>"],
    ["a body that is not an identity", JSON.stringify({ ok: true })],
    ["null", "null"],
  ])("rejects for a 200 carrying %s", async (_name, body) => {
    respond(200, body);
    await expect(fetchIdentity()).rejects.toBeInstanceOf(AuthError);
  });
});
