/**
 * The invitation link's fragment handling (inviteLink.ts, inviteLinkBoot.ts).
 *
 * The token is a bearer secret until accepted, so the properties pinned are
 * about where it may and may not be: out of the address bar as soon as the app
 * boots (before the router or a telemetry SDK reads the URL), always scrubbed
 * on the accept path even when malformed, never captured from any other path,
 * kept in storage across a sign-in round trip, and forgotten when too old.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const TOKEN = "rinv_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_";
const OTHER = "rinv_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ";

beforeEach(() => {
  vi.resetModules();
  window.localStorage.clear();
});
afterEach(() => window.history.replaceState(null, "", "/"));

async function boot(url: string) {
  window.history.replaceState(null, "", url);
  const bootModule = await import("../inviteLinkBoot");
  const link = await import("../inviteLink");
  return { bootModule, link };
}

describe("inviteLink", () => {
  it("moves the token out of the fragment at boot, keeping the path and query", async () => {
    const { link } = await boot(`/accept-invite?ref=email#${TOKEN}`);
    expect(window.location.hash).toBe("");
    expect(window.location.pathname).toBe("/accept-invite");
    expect(window.location.search).toBe("?ref=email");
    expect(link.readPendingInvite()?.token).toBe(TOKEN);
    // It is in storage, so it survives leaving the page to sign in.
    expect(window.localStorage.getItem("reliant:invite")).toContain(TOKEN);
  });

  it("scrubs a malformed fragment on the accept path without storing it", async () => {
    const { link } = await boot("/accept-invite#not-a-token");
    expect(window.location.hash).toBe("");
    expect(link.readPendingInvite()).toBeNull();
  });

  it("never touches a fragment on any other path", async () => {
    const { link } = await boot(`/settings#${TOKEN}`);
    expect(window.location.hash).toBe(`#${TOKEN}`);
    expect(link.readPendingInvite()).toBeNull();
  });

  it("does not take a claim link's token for an invitation", async () => {
    const { link } = await boot("/accept-invite#rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789");
    expect(link.readPendingInvite()).toBeNull();
  });

  it("forgets an invitation older than an invitation can live", async () => {
    const { link } = await boot(`/accept-invite#${TOKEN}`);
    const saved = link.readPendingInvite()!.savedAt;
    expect(link.readPendingInvite(saved + 9 * 24 * 60 * 60 * 1000)).toBeNull();
    expect(window.localStorage.getItem("reliant:invite")).toBeNull();
  });

  it("a different link pasted into the open tab restarts the page; the same one does not", async () => {
    const { bootModule, link } = await boot(`/accept-invite#${TOKEN}`);
    const restart = vi.fn();

    window.history.replaceState(null, "", `/accept-invite#${TOKEN}`);
    bootModule.onInviteHashChange(restart);
    expect(window.location.hash).toBe("");
    expect(restart).not.toHaveBeenCalled();

    window.history.replaceState(null, "", `/accept-invite#${OTHER}`);
    bootModule.onInviteHashChange(restart);
    expect(window.location.hash).toBe("");
    expect(restart).toHaveBeenCalledTimes(1);
    expect(link.readPendingInvite()?.token).toBe(OTHER);
  });

  it("clearPendingInvite forgets it", async () => {
    const { link } = await boot(`/accept-invite#${TOKEN}`);
    link.clearPendingInvite();
    expect(link.readPendingInvite()).toBeNull();
  });
});
