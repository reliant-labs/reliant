/**
 * An org install of the GitHub App returning to a browser signed in by the
 * forge login broker (AUTH_PROVIDER "forge"): the session is the broker's
 * HttpOnly cookie, answered by /auth/session, and there is no Supabase
 * session at all. The callback must see that person as signed in (through the
 * auth store, which restores the broker's session first), forward the
 * installation and its state to Settings › Organization GitHub, and that page
 * must link it. Reading Supabase alone ended this flow on the sessionless
 * "GitHub App installed" page and never linked anything.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import type { MyOrg } from "@/services/controlPlane/orgs";

const mocks = vi.hoisted(() => ({
  search: {} as Record<string, unknown>,
  navigate: vi.fn(),
  replace: vi.fn(),
  assign: vi.fn(),
  supabaseGetSession: vi.fn(),
  exchangeGithubOAuthCode: vi.fn(),
  link: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useSearch: () => mocks.search,
}));
// No Supabase session in this browser: forge sign-in never touches Supabase.
vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: mocks.supabaseGetSession, onAuthStateChange: vi.fn() } },
  setAuthStorageUnreadableHandler: vi.fn(),
}));
vi.mock("@/services/controlPlane/git", () => ({
  gitService: { exchangeGithubOAuthCode: mocks.exchangeGithubOAuthCode },
}));
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } }));
vi.mock("../GradientBackground", () => ({ GradientBackground: () => null }));
vi.mock("../icons/BrandMark", () => ({ BrandMark: () => null }));

const ACME: MyOrg = { id: "org-a", name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true };
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ active: ACME, orgs: [ACME], isLoading: false }),
}));
vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({ hasToken: false, installations: [], scopes: "", isLoading: false, isError: false, refresh: vi.fn() }),
}));

import { GitHubOAuthCallback } from "../GitHubOAuthCallback";
import { OrgGitHubPage } from "../Settings/cloud/github/OrgGitHubPage";

/** A state the way control-plane signs one: base64url(JSON) "." base64url(HMAC). */
function signedState(payload: Record<string, unknown>): string {
  const b64 = (v: string) => btoa(v).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${b64(JSON.stringify(payload))}.${b64("not-a-real-mac")}`;
}

/** The broker: /auth/session answers a signed-in identity from the cookie. */
const brokerFetch = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.endsWith("/auth/session")) {
    return new Response(JSON.stringify({ authenticated: true, subject: "u-1", email: "alice@example.test", name: "Alice" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }
  return new Response("not found", { status: 404 });
});

beforeEach(() => {
  window.__FORGE_CONFIG__ = { AUTH_PROVIDER: "forge" };
  vi.stubGlobal("fetch", brokerFetch);
  mocks.search = {};
  mocks.supabaseGetSession.mockResolvedValue({ data: { session: null } });
  mocks.link.mockImplementation((req: { installationId: bigint }) => ({
    installation: { installationId: req.installationId, orgId: "org-a", accountLogin: "acme", accountType: "Organization" },
  }));
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { ...window.location, origin: "https://app.reliantlabs.io", assign: mocks.assign, replace: mocks.replace },
  });
  setTransport(
    mockTransport({
      "OrganizationService.ListGitHubAppInstallations": () => ({ installations: [] }),
      "OrganizationService.LinkGitHubAppInstallation": (req) => mocks.link(req),
    }),
  );
});

afterEach(() => {
  window.__FORGE_CONFIG__ = undefined;
  vi.unstubAllGlobals();
});

describe("an org install returning to a forge-auth browser", () => {
  it("is seen as signed in by the broker's session, forwarded to the settings page, and linked there", async () => {
    const state = signedState({ k: "org_install", org: "org-a", uid: "u-1", n: "x", exp: 9999999999 });
    mocks.search = { installation_id: "166627811", setup_action: "install", state };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    expect(brokerFetch).toHaveBeenCalledWith(expect.stringMatching(/\/auth\/session$/), expect.objectContaining({ credentials: "include" }));
    expect(mocks.supabaseGetSession).not.toHaveBeenCalled();
    expect(screen.queryByText("GitHub App installed")).toBeNull();
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();

    const target = new URL(mocks.replace.mock.calls[0][0] as string, "https://app.reliantlabs.io");
    expect(target.pathname).toBe("/settings/org-github");

    // The page GitHub's return was forwarded to links what it names.
    mocks.search = Object.fromEntries(target.searchParams);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <OrgGitHubPage />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(mocks.link).toHaveBeenCalledTimes(1));
    expect(mocks.link.mock.calls[0][0]).toMatchObject({ installationId: 166627811n, state });
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent("@acme is linked to Acme.");
  });
});
