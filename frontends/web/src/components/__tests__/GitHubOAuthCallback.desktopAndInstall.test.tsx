/**
 * The three ways the web callback page stops being "exchange and go home":
 *
 *   - a desktop app started the flow (state carries `lb`): relay the result to
 *     its loopback receiver and DO NOT exchange;
 *   - the account has no GitHub App installation: send the user to install,
 *     remembering where they were so the stateless setup return can land back;
 *   - there is no Supabase session in this browser: end on a terminal page
 *     instead of navigating into a route that would ask to sign in.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  search: {} as Record<string, unknown>,
  exchangeGithubOAuthCode: vi.fn(),
  getSession: vi.fn(),
  assign: vi.fn(),
  replace: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useSearch: () => mocks.search,
}));

vi.mock("@/services/controlPlane/git", () => ({
  gitService: { exchangeGithubOAuthCode: mocks.exchangeGithubOAuthCode },
}));

vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: mocks.getSession } },
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

vi.mock("../GradientBackground", () => ({ GradientBackground: () => null }));
vi.mock("../icons/BrandMark", () => ({ BrandMark: () => null }));

import { GitHubOAuthCallback } from "../GitHubOAuthCallback";

const b64url = (s: string) =>
  btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const stateWith = (payload: unknown) =>
  `${b64url(JSON.stringify(payload))}.${b64url("sig")}`;

const LOOPBACK = "http://127.0.0.1:5555/callback";
const INSTALL_URL = "https://github.com/apps/reliant-labs/installations/new";
const EXCHANGED = { ok: true, returnTo: "/onboarding?plan=x", error: "", needsInstall: false, installUrl: "" };

beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  mocks.search = {};
  mocks.getSession.mockResolvedValue({ data: { session: { access_token: "t" } } });
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { origin: "https://app.reliantlabs.io", assign: mocks.assign, replace: mocks.replace },
  });
});

describe("desktop relay — state carries a loopback", () => {
  it("relays code and state to the loopback and does not exchange", async () => {
    const state = stateWith({ uid: "u", lb: LOOPBACK, rt: "/" });
    mocks.search = { code: "gh-code", state };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    const relayed = new URL(mocks.replace.mock.calls[0][0] as string);
    expect(relayed.origin).toBe("http://127.0.0.1:5555");
    expect(relayed.pathname).toBe("/callback");
    expect(relayed.searchParams.get("code")).toBe("gh-code");
    expect(relayed.searchParams.get("state")).toBe(state);
    expect(mocks.exchangeGithubOAuthCode).not.toHaveBeenCalled();
    expect(mocks.assign).not.toHaveBeenCalled();
    expect(await screen.findByText("Return to the Reliant app")).toBeInTheDocument();
  });

  it("relays a GitHub denial in the shape the receiver reads", async () => {
    const state = stateWith({ lb: LOOPBACK });
    mocks.search = { error: "access_denied", error_description: "The user denied", state };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalled());
    const relayed = new URL(mocks.replace.mock.calls[0][0] as string);
    expect(relayed.searchParams.get("error")).toBe("access_denied");
    expect(relayed.searchParams.get("error_description")).toBe("The user denied");
    expect(relayed.searchParams.get("code")).toBeNull();
    expect(mocks.assign).not.toHaveBeenCalled();
  });

  it.each([
    "https://127.0.0.1:5555/callback",
    "http://evil.example:5555/callback",
    "http://127.0.0.1/callback",
    "http://user@127.0.0.1:5555/callback",
  ])("never relays to %s: exchanges as a normal flow instead", async (lb) => {
    mocks.search = { code: "c", state: stateWith({ lb }) };
    mocks.exchangeGithubOAuthCode.mockResolvedValue(EXCHANGED);

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.replace).not.toHaveBeenCalled();
    expect(mocks.exchangeGithubOAuthCode).toHaveBeenCalled();
  });

  it("exchanges normally when the state has no loopback", async () => {
    mocks.search = { code: "c", state: stateWith({ uid: "u" }) };
    mocks.exchangeGithubOAuthCode.mockResolvedValue(EXCHANGED);

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.replace).not.toHaveBeenCalled();
  });
});

describe("a user with no App installation", () => {
  it("sends them to the install page and remembers where they were", async () => {
    mocks.search = { code: "c", state: stateWith({ uid: "u" }) };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({ ...EXCHANGED, needsInstall: true, installUrl: INSTALL_URL });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalledWith(INSTALL_URL));
    expect(sessionStorage.getItem("reliant:github-install-return")).toBe("/onboarding?plan=x");
  });

  it("lands the setup return back on the remembered location", async () => {
    sessionStorage.setItem("reliant:github-install-return", "/onboarding?plan=x");
    mocks.search = { installation_id: "1", setup_action: "install" };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.assign.mock.calls[0][0]).toBe("/onboarding?plan=x&github_installed=true");
    expect(sessionStorage.getItem("reliant:github-install-return")).toBeNull();
  });

  it("will not follow a remembered location that is not an in-app path", async () => {
    sessionStorage.setItem("reliant:github-install-return", "//evil.example");
    mocks.search = { installation_id: "1", setup_action: "install" };

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalled());
    expect(mocks.assign.mock.calls[0][0]).toBe("/?github_installed=true");
  });
});

describe("no Supabase session in this browser", () => {
  beforeEach(() => {
    mocks.getSession.mockResolvedValue({ data: { session: null } });
  });

  it("ends an exchange on a terminal page instead of navigating", async () => {
    mocks.search = { code: "c", state: stateWith({ uid: "u" }) };
    mocks.exchangeGithubOAuthCode.mockResolvedValue(EXCHANGED);

    render(<GitHubOAuthCallback />);

    expect(await screen.findByText("GitHub connected")).toBeInTheDocument();
    expect(screen.getByText(/you can close this tab/i)).toBeInTheDocument();
    expect(mocks.assign).not.toHaveBeenCalled();
  });

  it("ends an install return on a terminal page", async () => {
    mocks.search = { installation_id: "1", setup_action: "install" };

    render(<GitHubOAuthCallback />);

    expect(await screen.findByText("GitHub App installed")).toBeInTheDocument();
    expect(mocks.assign).not.toHaveBeenCalled();
  });

  it("still sends a needs-install user to install: that needs no session", async () => {
    mocks.search = { code: "c", state: stateWith({ uid: "u" }) };
    mocks.exchangeGithubOAuthCode.mockResolvedValue({ ...EXCHANGED, needsInstall: true, installUrl: INSTALL_URL });

    render(<GitHubOAuthCallback />);

    await waitFor(() => expect(mocks.assign).toHaveBeenCalledWith(INSTALL_URL));
  });
});
