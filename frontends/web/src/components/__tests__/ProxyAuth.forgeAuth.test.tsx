/**
 * The workspace-proxy sign-in bounce (`/auth/proxy?return=…`).
 *
 * Pins who counts as signed in, and what the mint is given, under each
 * provider: Supabase hands its session's bearer to the mint; forge sign-in
 * (AUTH_PROVIDER "forge") reads the broker's session from the auth store and
 * hands the mint no token — the cookie is the credential (lib/proxy-session).
 */
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const navigate = vi.hoisted(() => vi.fn());
const mint = vi.hoisted(() => vi.fn());
const getSession = vi.hoisted(() => vi.fn());
const forge = vi.hoisted(() => ({ on: false }));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useSearch: () => ({ return: "https://5173-d1.workspaces.example.com/app" }),
}));
vi.mock("@/lib/proxy-session", () => ({ mintProxySessionCallbackUrl: mint }));
vi.mock("@/lib/supabase", () => ({ supabase: { auth: { getSession } } }));
vi.mock("@/lib/auth/forge-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/forge-api")>()),
  isForgeAuth: () => forge.on,
}));

import { useAuthStore } from "@/store/authStore";
import { forgeUserOf } from "@/lib/auth/forge-session";
import { ProxyAuth } from "../ProxyAuth";

const RETURN = "https://5173-d1.workspaces.example.com/app";

beforeEach(() => {
  navigate.mockReset();
  getSession.mockReset();
  // A refused mint shows the error screen instead of navigating this page.
  mint.mockReset().mockRejectedValue(new Error("mint refused"));
});

afterEach(() => {
  forge.on = false;
  useAuthStore.setState({ user: null, initialized: false });
});

describe("ProxyAuth", () => {
  it("supabase: mints with the session's bearer", async () => {
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1" } }, error: null });
    render(<ProxyAuth />);
    await waitFor(() => expect(mint).toHaveBeenCalledWith(RETURN, "jwt-1"));
    expect(await screen.findByText("mint refused")).toBeTruthy();
  });

  it("forge sign-in: mints with no token for a broker session, never reading Supabase", async () => {
    forge.on = true;
    useAuthStore.setState({ user: forgeUserOf({ authenticated: true, subject: "alice" }), initialized: true });
    render(<ProxyAuth />);
    await waitFor(() => expect(mint).toHaveBeenCalledWith(RETURN, null));
    expect(getSession).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("forge sign-in: sends a person with no broker session to sign in and back", async () => {
    forge.on = true;
    useAuthStore.setState({ user: null, initialized: true });
    render(<ProxyAuth />);
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: "/auth",
        search: { redirect: `/auth/proxy?return=${encodeURIComponent(RETURN)}` },
      }),
    );
    expect(mint).not.toHaveBeenCalled();
  });
});
