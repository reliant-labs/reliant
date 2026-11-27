/**
 * /auth/github/callback is reached from TWO different GitHub flows, and the
 * reported prod bug is that only one of them was handled.
 *
 * The owner installed the GitHub App. GitHub sent them to
 *
 *   /auth/github/callback?code=8de4ef83…&installation_id=166627811&setup_action=install
 *
 * and the page said "Invalid GitHub callback. Please try connecting again." —
 * because the component required BOTH `code` and `state`, and an App install
 * redirect carries no `state`. There is none to carry: GitHub initiated that
 * redirect, so there was no authorize request of ours for it to echo.
 *
 * WHY THE STATELESS CODE IS NOT EXCHANGED. `state` is not ceremony here. The
 * control plane's signed state is what carries the user id the credential gets
 * attributed to (oauthState.UserID → GetUserByExternalID →
 * UpsertGitCredential). A code with no state is therefore unattributable AND
 * unverified, and exchanging one would mean writing a GitHub credential on the
 * say-so of whoever opened the URL — the login-CSRF that state exists to stop.
 * GitHub says the same about the other parameter in that URL: "bad actors can
 * hit this URL with a spoofed installation_id... you should not rely on the
 * validity of the installation_id parameter."
 *
 * So the install return is treated as a signal, not as authority: refresh what
 * the server reports, tell the user their access changed, trust nothing in the
 * URL. These tests pin all three shapes.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  search: {} as Record<string, unknown>,
  exchangeGithubOAuthCode: vi.fn(),
  assign: vi.fn(),
  replace: vi.fn(),
  navigate: vi.fn(),
  session: { access_token: "t" } as { access_token: string } | null,
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useSearch: () => mocks.search,
}));

vi.mock("@/services/controlPlane/git", () => ({
  gitService: {
    exchangeGithubOAuthCode: mocks.exchangeGithubOAuthCode,
  },
}));

// Who is signed in, by the active provider (lib/auth/use-auth); the forge
// provider's case is GitHubOAuthCallback.forgeAuth.test.tsx.
vi.mock("@/lib/auth/use-auth", () => ({
  restoredAuthIdentity: async () => (mocks.session ? { id: "u-1" } : null),
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

vi.mock("../GradientBackground", () => ({
  GradientBackground: () => null,
}));

vi.mock("../icons/BrandMark", () => ({
  BrandMark: () => null,
}));

import { GitHubOAuthCallback } from "../GitHubOAuthCallback";

/** The landing hop is a full document assignment, so it is the observable
 *  outcome for every success path. */
function landedAt(): string {
  expect(mocks.assign).toHaveBeenCalledTimes(1);
  return mocks.assign.mock.calls[0][0] as string;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.search = {};
  mocks.session = { access_token: "t" };
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { origin: "https://app.reliantlabs.io", assign: mocks.assign, replace: mocks.replace },
  });
});

describe("the OAuth flow — ?code&state", () => {
  it("exchanges the code and lands back with github_connected", async () => {
    mocks.search = { code: "abc123", state: "signed-state" };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({
      ok: true,
      returnTo: "/settings",
    });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.exchangeGithubOAuthCode).toHaveBeenCalledWith(
      "abc123",
      "signed-state",
    );
    expect(landedAt()).toBe("/settings?github_connected=true");
  });
});

describe("a flow the forge site started (lib/crossShellReturn)", () => {
  // The wizard runs on the forge host too; GitHub's callback is this host's,
  // and the control plane keeps only a relative returnTo.
  it("lands back on the forge site, not on this host", async () => {
    mocks.search = { code: "abc123", state: "signed-state" };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({
      ok: true,
      returnTo: "/__forge/onboarding?returnTo=%2Fapps",
    });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(landedAt()).toBe("https://forge.reliantlabs.io/onboarding?returnTo=%2Fapps&github_connected=true");
  });

  it("never lets the prefix name another host", async () => {
    mocks.search = { code: "abc123", state: "signed-state" };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({ ok: true, returnTo: "/__forge//evil.com/x" });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    // Not the forge site and not another host: a path on this one.
    const landed = landedAt();
    expect(landed.startsWith("/")).toBe(true);
    expect(landed.startsWith("//")).toBe(false);
  });
});

/** A state the way control-plane signs one: base64url(JSON) "." base64url(HMAC). */
function signedState(payload: Record<string, unknown>): string {
  const b64 = (v: string) => btoa(v).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${b64(JSON.stringify(payload))}.${b64("not-a-real-mac")}`;
}

describe("an org install of the App — ?installation_id&setup_action&state, state of kind org_install", () => {
  // Settings › Organization GitHub (StartGitHubAppInstall) sent GitHub its own
  // state; the settings page links it, and LinkGitHubAppInstallation checks
  // the state, the caller and the installation. Nothing is exchanged here.
  it("forwards installation_id and the state to /settings/org-github, replacing this entry", async () => {
    const state = signedState({ k: "org_install", org: "org-a", uid: "u-1", n: "x", exp: 9999999999 });
    mocks.search = { code: "ghcode", installation_id: "166627811", setup_action: "install", state };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    const target = new URL(mocks.replace.mock.calls[0][0] as string, "https://app.reliantlabs.io");
    expect(target.pathname).toBe("/settings/org-github");
    expect(target.searchParams.get("installation_id")).toBe("166627811");
    expect(target.searchParams.get("link_state")).toBe(state);
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
    expect(mocks.assign).not.toHaveBeenCalled();
  });

  it("in a browser with no Reliant session (a desktop install), says it is installed and links nothing here", async () => {
    mocks.session = null;
    mocks.search = { installation_id: "166627811", setup_action: "install", state: signedState({ k: "org_install", org: "org-a", uid: "u-1" }) };

    render(<GitHubOAuthCallback />);

    expect(await screen.findByText("GitHub App installed")).toBeInTheDocument();
    expect(mocks.replace).not.toHaveBeenCalled();
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
  });

  it("leaves a connect-flow state (no kind) to the exchange", async () => {
    const state = signedState({ uid: "u-1", n: "x", exp: 9999999999, rt: "/settings" });
    mocks.search = { code: "abc123", state };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({ ok: true, returnTo: "/settings" });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.exchangeGithubOAuthCode).toHaveBeenCalledWith("abc123", state);
    expect(mocks.replace).not.toHaveBeenCalled();
  });
});

describe("the App install flow — ?installation_id&setup_action, no state", () => {
  // THE REPORTED URL. Reduced to its parameters; this exact shape produced
  // "Invalid GitHub callback" in production.
  it("treats an install return WITH a stateless code as success, and does not exchange it", async () => {
    mocks.search = {
      code: "8de4ef83aa11bb22cc33",
      installation_id: "166627811",
      setup_action: "install",
    };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());

    // The security-relevant assertion: a code with no state is never redeemed.
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
    expect(landedAt()).toBe("/?github_installed=true");
    expect(screen.queryByText(/invalid github callback/i)).toBeNull();
  });

  it("handles an install return with NO code the same way", async () => {
    mocks.search = { installation_id: "166627811", setup_action: "install" };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
    expect(landedAt()).toBe("/?github_installed=true");
  });

  it("handles setup_action=update, which is the 'changed repositories' return", async () => {
    mocks.search = { installation_id: "166627811", setup_action: "update" };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(landedAt()).toBe("/?github_installed=true");
  });

  it("never shows the error screen for an install return", async () => {
    mocks.search = { setup_action: "install", installation_id: "1" };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(screen.queryByText(/github connection failed/i)).toBeNull();
  });
});

describe("genuinely malformed callbacks still fail loudly", () => {
  // The guard has to keep its teeth: a URL with neither a usable OAuth pair nor
  // a setup_action really is broken, and saying so is correct.
  it("reports an invalid callback when there is no state and no setup_action", async () => {
    mocks.search = { code: "orphan-code" };

    render(<GitHubOAuthCallback />);

    expect(
      await screen.findByText(/invalid github callback/i),
    ).toBeInTheDocument();
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
    expect(mocks.assign).not.toHaveBeenCalled();
  });

  it("reports an invalid callback for a completely empty query", async () => {
    mocks.search = {};

    render(<GitHubOAuthCallback />);

    expect(
      await screen.findByText(/invalid github callback/i),
    ).toBeInTheDocument();
  });

  it("passes a GitHub-reported error through as an error landing", async () => {
    mocks.search = {
      error: "access_denied",
      error_description: "The user denied the request",
    };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(landedAt()).toContain("github_error=access_denied");
  });
});
