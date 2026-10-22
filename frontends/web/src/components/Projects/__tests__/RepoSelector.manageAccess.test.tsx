/**
 * The reported dead end: a user's clone dialog listed three repos from one
 * org, the private repo they wanted lived on a different account, and the
 * only affordance on screen was "Reconnect GitHub" — which re-authorizes the
 * USER and changes nothing about which accounts or repos the GitHub App is
 * installed on. They clicked it and got the same three repos back.
 *
 * These tests pin the fix: the picker always offers the GitHub installation
 * flow, and it re-reads the list when the user comes back from it.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GitRepo } from "../../../services/controlPlane/git/types";

const mocks = vi.hoisted(() => ({
  fetchRepos: vi.fn(),
  refreshCredential: vi.fn(),
  reposQuery: {
    data: { repos: [] as GitRepo[], hasMore: false },
    isLoading: false,
    isError: false,
    error: null as unknown,
    refetch: vi.fn(),
  },
  credential: {
    hasToken: true,
    scopes: "",
    installUrl: "https://github.com/apps/reliant-labs/installations/new",
    installations: [] as Array<{
      accountLogin: string;
      accountType: string;
      avatarUrl: string;
      configureUrl: string;
      repositorySelection: string;
    }>,
    isLoading: false,
    isError: false,
    refresh: vi.fn(),
  },
}));

vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useGitRepos: () => mocks.reposQuery,
}));

vi.mock("../../../hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => mocks.credential,
}));

vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("../../../services/controlPlane/git", () => ({
  gitService: { getOAuthURL: () => "https://cp.example/auth/github/authorize" },
}));

vi.mock("../../../lib/supabase", () => ({
  supabase: { auth: { getSession: vi.fn() } },
}));

const { RepoSelector } = await import("../RepoSelector");

function repo(fullName: string): GitRepo {
  return {
    fullName,
    cloneUrl: `https://github.com/${fullName}.git`,
    defaultBranch: "main",
    description: "",
    private: true,
    language: "",
    updatedAt: "",
  };
}

const INSTALL_URL = "https://github.com/apps/reliant-labs/installations/new";

beforeEach(() => {
  mocks.reposQuery.data = { repos: [], hasMore: false };
  mocks.reposQuery.isLoading = false;
  mocks.reposQuery.isError = false;
  mocks.reposQuery.error = null;
  mocks.reposQuery.refetch = vi.fn();
  mocks.credential.installUrl = INSTALL_URL;
  mocks.credential.installations = [];
  mocks.credential.refresh = vi.fn();
});

/** The link that takes the user to GitHub's installation flow, wherever the
 *  component chose to render it. */
function installLink(): HTMLAnchorElement | undefined {
  return screen
    .queryAllByRole("link")
    .find((el) => el.getAttribute("href") === INSTALL_URL) as
    | HTMLAnchorElement
    | undefined;
}

describe("RepoSelector — managing GitHub repository access", () => {
  it("offers the install flow even when the list is full — the reported case", () => {
    // The user HAD repos. That is exactly why nothing looked wrong, and why
    // an affordance shown only on an empty list would not have helped them.
    mocks.reposQuery.data = {
      repos: [repo("reliant-labs/forge"), repo("reliant-labs/reliant")],
      hasMore: false,
    };
    mocks.credential.installations = [
      {
        accountLogin: "reliant-labs",
        accountType: "Organization",
        avatarUrl: "",
        configureUrl: "https://github.com/organizations/reliant-labs/settings/installations/1",
        repositorySelection: "selected",
      },
    ];

    render(<RepoSelector onSelect={vi.fn()} />);

    expect(screen.getByText("Missing a repository?")).toBeInTheDocument();
    expect(installLink()).toBeDefined();
  });

  it("names the installation's scope, which is why a repo can be missing", () => {
    mocks.reposQuery.data = { repos: [repo("reliant-labs/forge")], hasMore: false };
    mocks.credential.installations = [
      {
        accountLogin: "reliant-labs",
        accountType: "Organization",
        avatarUrl: "",
        configureUrl: "https://github.com/organizations/reliant-labs/settings/installations/1",
        repositorySelection: "selected",
      },
    ];

    render(<RepoSelector onSelect={vi.fn()} />);

    expect(screen.getByText("reliant-labs")).toBeInTheDocument();
    expect(screen.getByText(/Selected repositories/)).toBeInTheDocument();
    // A per-installation Configure link is how a user widens an existing
    // "selected repositories" grant without reinstalling.
    expect(
      screen.getByRole("link", { name: "Configure" }).getAttribute("href"),
    ).toBe("https://github.com/organizations/reliant-labs/settings/installations/1");
  });

  it("leads with installing the App when no account is connected", () => {
    mocks.credential.installations = [];
    render(<RepoSelector onSelect={vi.fn()} />);

    expect(screen.getByText("No GitHub accounts connected")).toBeInTheDocument();
    expect(installLink()).toBeDefined();
  });

  it("renders no link at all when the control plane has no App slug", () => {
    // github.com/apps//installations/new is a 404; a dead CTA is worse than
    // an absent one.
    mocks.credential.installUrl = undefined;
    mocks.reposQuery.data = { repos: [repo("reliant-labs/forge")], hasMore: false };

    render(<RepoSelector onSelect={vi.fn()} />);

    expect(screen.queryByText("Missing a repository?")).not.toBeInTheDocument();
    expect(installLink()).toBeUndefined();
  });

  it("re-reads repos and installations when the user returns from GitHub", async () => {
    // Granting access happens in another tab. Coming back to the same stale
    // list reads as "it didn't work".
    mocks.reposQuery.data = { repos: [repo("reliant-labs/forge")], hasMore: false };
    render(<RepoSelector onSelect={vi.fn()} />);

    mocks.reposQuery.refetch.mockClear();
    mocks.credential.refresh.mockClear();

    window.dispatchEvent(new Event("focus"));

    await waitFor(() => {
      expect(mocks.reposQuery.refetch).toHaveBeenCalled();
      expect(mocks.credential.refresh).toHaveBeenCalled();
    });
  });

  it("distinguishes reconnecting from changing repository access", () => {
    // The two actions are different and the copy has to say so, or the user
    // reaches for the wrong one — which is how this bug was reported.
    mocks.reposQuery.error = new Error("no git credential found for provider");
    mocks.reposQuery.isError = true;

    render(<RepoSelector onSelect={vi.fn()} />);

    expect(
      screen.getByText(/doesn't change which repositories Reliant can see/i),
    ).toBeInTheDocument();
  });
});
