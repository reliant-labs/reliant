/** The forge broker's account routes client (forge#2663's JSON). */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/auth/forge-api", () => ({ reliantForgeBase: () => "https://app.test/_api/@reliant" }));

const account = await import("../account");

const fetchMock = vi.fn();

function reply(status: number, body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("account client", () => {
  it("reads the account page with the session cookie and no bearer", async () => {
    fetchMock.mockReturnValueOnce(
      reply(200, {
        subject: "u1",
        email: "ada@example.com",
        emailVerified: true,
        hasPassword: true,
        links: [{ provider: "github", label: "GitHub", userName: "ada" }],
        social: [{ type: "github", label: "GitHub", connected: true }],
        passkeys: [{ id: "pk1", name: "Laptop" }],
        sessions: [{ handle: "h1", createdAt: "", lastSeenAt: "", expiresAt: "", current: true }],
        provedAt: "2026-11-20T00:00:00Z",
        reauthRequired: false,
      }),
    );
    const a = await account.fetchAccount();
    expect(a.email).toBe("ada@example.com");
    expect(a.links[0].userName).toBe("ada");
    expect(a.sessions[0].current).toBe(true);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://app.test/_api/@reliant/auth/account");
    expect(init.method).toBe("GET");
    expect(init.credentials).toBe("include");
    expect(init.headers.Authorization).toBeUndefined();
    expect(init.body).toBeUndefined();
  });

  it("posts JSON to the mutating routes", async () => {
    fetchMock.mockReturnValueOnce(reply(202, { sent: true, message: "Check your email." }));
    await expect(account.changeEmail("new@example.com")).resolves.toEqual({ message: "Check your email." });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://app.test/_api/@reliant/auth/account/email");
    expect(init.method).toBe("POST");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({ email: "new@example.com" });
  });

  it("escapes path segments for deletes", async () => {
    fetchMock.mockReturnValue(reply(200, { removed: true }));
    await account.removePasskey("a/b");
    await account.revokeSession("h 1");
    expect(fetchMock.mock.calls[0][0]).toBe("https://app.test/_api/@reliant/auth/account/passkeys/a%2Fb");
    expect(fetchMock.mock.calls[0][1].method).toBe("DELETE");
    expect(fetchMock.mock.calls[1][0]).toBe("https://app.test/_api/@reliant/auth/account/sessions/h%201");
  });

  it("surfaces reauth_required with the methods to prove one", async () => {
    fetchMock.mockReturnValueOnce(
      reply(401, { code: "reauth_required", error: "Confirm it's you first.", methods: ["password", "sign_in"] }),
    );
    const err = await account.unlinkSocial("github").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(account.AccountError);
    expect(account.isReauthRequired(err)).toBe(true);
    expect((err as InstanceType<typeof account.AccountError>).methods).toEqual(["password", "sign_in"]);
    expect((err as Error).message).toBe("Confirm it's you first.");
  });

  it("falls back to sign_in when reauth_required names no method", async () => {
    fetchMock.mockReturnValueOnce(reply(401, { code: "reauth_required", error: "Confirm it's you first." }));
    const err = (await account.revokeOtherSessions().catch((e: unknown) => e)) as InstanceType<typeof account.AccountError>;
    expect(err.methods).toEqual(["sign_in"]);
  });

  it("surfaces last_credential as a 409 with the broker's message", async () => {
    fetchMock.mockReturnValueOnce(
      reply(409, { code: "last_credential", error: "This is your only way to sign in. Add another first." }),
    );
    const err = (await account.removePasskey("pk1").catch((e: unknown) => e)) as InstanceType<typeof account.AccountError>;
    expect(err.status).toBe(409);
    expect(err.code).toBe("last_credential");
    expect(account.isReauthRequired(err)).toBe(false);
    expect(err.message).toBe("This is your only way to sign in. Add another first.");
  });

  it("surfaces sessions_not_revoked: the password changed, with the retry route", async () => {
    fetchMock.mockReturnValueOnce(
      reply(502, {
        code: "sessions_not_revoked",
        error: "Your password changed, but other devices could not be signed out.",
        changed: true,
        otherSessionsRevoked: false,
        otherSessionsEnded: 1,
        retry: "/auth/account/sessions/revoke-others",
      }),
    );
    const err = (await account
      .changePassword({ currentPassword: "old", newPassword: "new-password-1" })
      .catch((e: unknown) => e)) as InstanceType<typeof account.AccountError>;
    expect(err.code).toBe("sessions_not_revoked");
    expect(err.changed).toBe(true);
    expect(err.retry).toBe("/auth/account/sessions/revoke-others");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ currentPassword: "old", newPassword: "new-password-1" });
  });

  it("omits an empty current password", async () => {
    fetchMock.mockReturnValueOnce(reply(200, { changed: true, otherSessionsRevoked: true, otherSessionsEnded: 2 }));
    await expect(account.changePassword({ newPassword: "pw" })).resolves.toEqual({
      changed: true,
      otherSessionsRevoked: true,
      otherSessionsEnded: 2,
    });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ newPassword: "pw" });
  });

  it("answers status 0 when the broker is unreachable", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const err = (await account.listSessions().catch((e: unknown) => e)) as InstanceType<typeof account.AccountError>;
    expect(err.status).toBe(0);
  });

  it("returns the provider redirect for a link start", async () => {
    fetchMock.mockReturnValueOnce(reply(200, { redirect: "https://github.com/login/oauth/authorize?x" }));
    await expect(account.startLink("github", "/settings/account")).resolves.toEqual({
      redirect: "https://github.com/login/oauth/authorize?x",
    });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ provider: "github", returnTo: "/settings/account" });
  });
});
