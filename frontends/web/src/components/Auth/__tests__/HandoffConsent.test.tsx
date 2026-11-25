/**
 * The desktop sign-in consent page (`/auth/handoff`).
 *
 * Pins what the page is for: it asks reliant-api (same origin) who is signed
 * in, posts the request id only on Continue, and navigates only to this
 * origin (sign-in) or a loopback listener (the code).
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const assign = vi.fn();
vi.mock("@/lib/hardNavigate", () => ({ hardNavigate: (url: string) => assign(url) }));

import { HandoffConsent } from "../HandoffConsent";

const ORIGIN = window.location.origin;
const AUTHORIZE = "/_api/@reliant/auth/handoff/authorize";

let fetchMock: ReturnType<typeof vi.fn>;

function reply(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

const READY = {
  account: { subject: "u-1", email: "alice@example.test", name: "Alice" },
  destination: "reliant-desktop",
  redirect: "",
  kind: "native",
};

beforeEach(() => {
  window.__FORGE_CONFIG__ = { AUTH_PROVIDER: "forge" };
  assign.mockReset();
  window.history.replaceState(null, "", "/auth/handoff?req=r-123");
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  window.__FORGE_CONFIG__ = undefined;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("HandoffConsent", () => {
  it("asks who is signed in, and posts the request only on Continue", async () => {
    fetchMock
      .mockResolvedValueOnce(reply(200, READY))
      .mockResolvedValueOnce(reply(200, { redirect: "http://127.0.0.1:53111/callback?code=c&state=s" }));
    render(<HandoffConsent />);

    expect(await screen.findByText("Continue as alice@example.test to the Reliant desktop app")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(`${AUTHORIZE}?req=r-123`, { credentials: "include" });
    expect(assign).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith("http://127.0.0.1:53111/callback?code=c&state=s"));
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe(AUTHORIZE);
    expect(init).toMatchObject({
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
    });
    expect(JSON.parse(init.body)).toEqual({ req: "r-123" });
    expect(screen.getByText("Return to the desktop app")).toBeInTheDocument();
  });

  it("accepts an IPv6 loopback redirect", async () => {
    fetchMock
      .mockResolvedValueOnce(reply(200, READY))
      .mockResolvedValueOnce(reply(200, { redirect: "http://[::1]:53111/callback?code=c" }));
    render(<HandoffConsent />);
    await userEvent.click(await screen.findByRole("button", { name: "Continue" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("http://[::1]:53111/callback?code=c"));
  });

  it.each([
    ["https://evil.example/callback?code=c"],
    ["http://localhost:53111/callback?code=c"],
    ["https://127.0.0.1:53111/callback?code=c"],
    ["http://127.0.0.1/callback?code=c"],
    ["http://user@127.0.0.1:53111/callback"],
    ["not a url"],
  ])("refuses a non-loopback redirect %s", async (redirect) => {
    fetchMock.mockResolvedValueOnce(reply(200, READY)).mockResolvedValueOnce(reply(200, { redirect }));
    render(<HandoffConsent />);
    await userEvent.click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Sign-in refused")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("Cancel sends nothing", async () => {
    fetchMock.mockResolvedValueOnce(reply(200, READY));
    render(<HandoffConsent />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(screen.getByText("Sign-in cancelled")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(assign).not.toHaveBeenCalled();
  });

  it("follows a same-origin sign-in link when signed out", async () => {
    const signIn = `${ORIGIN}/auth/sign-in?returnTo=%2Fauth%2Fhandoff%3Freq%3Dr-123`;
    fetchMock.mockResolvedValueOnce(reply(401, { code: "not_signed_in", sign_in: signIn }));
    render(<HandoffConsent />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith(signIn));
  });

  it("follows a relative sign-in link on this origin", async () => {
    fetchMock.mockResolvedValueOnce(reply(401, { code: "not_signed_in", sign_in: "/auth/sign-in?returnTo=x" }));
    render(<HandoffConsent />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith(`${ORIGIN}/auth/sign-in?returnTo=x`));
  });

  it("refuses a cross-origin sign-in link", async () => {
    fetchMock.mockResolvedValueOnce(
      reply(401, { code: "not_signed_in", sign_in: "https://evil.example/auth/sign-in?returnTo=x" }),
    );
    render(<HandoffConsent />);
    expect(await screen.findByText("Sign-in refused")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("explains reauth_required", async () => {
    fetchMock.mockResolvedValueOnce(reply(401, { code: "reauth_required" }));
    render(<HandoffConsent />);
    expect(await screen.findByText("Sign in again")).toBeInTheDocument();
    expect(screen.getByTestId("handoff-message")).toHaveTextContent(/started before desktop sign-in was available/);
    expect(assign).not.toHaveBeenCalled();
  });

  it("explains an expired request", async () => {
    fetchMock.mockResolvedValueOnce(reply(400, { code: "handoff_invalid", error: "expired" }));
    render(<HandoffConsent />);
    expect(await screen.findByText("This sign-in link has expired")).toBeInTheDocument();
    expect(screen.getByTestId("handoff-message")).toHaveTextContent(/Start signing in again from the desktop app/);
  });

  it("explains a network failure", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    render(<HandoffConsent />);
    expect(await screen.findByText("Could not reach Reliant")).toBeInTheDocument();
  });

  it("makes no request without a req", async () => {
    window.history.replaceState(null, "", "/auth/handoff");
    render(<HandoffConsent />);
    expect(screen.getByText("This sign-in link has expired")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does nothing in supabase mode", async () => {
    window.__FORGE_CONFIG__ = { AUTH_PROVIDER: "supabase" };
    render(<HandoffConsent />);
    expect(screen.getByText("Desktop sign-in is not available here")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});
