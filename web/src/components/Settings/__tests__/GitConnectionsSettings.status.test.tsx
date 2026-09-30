/**
 * Settings → GitHub connection status.
 *
 * The page used to show "GitHub connected", a scope string, a generic SSO
 * note and a delete button — nothing about WHICH account was connected,
 * whether the token still worked, or (for a GitHub App) which orgs it could
 * actually reach. For an App the scope string is worse than useless: GitHub
 * ignores OAuth scopes for Apps entirely, so "Scopes: user:email repo" claims
 * an access model that does not apply.
 */
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { GitCredentialStatus } from "@/services/controlPlane/git/types";

const mockGetCredential = vi.fn<() => Promise<GitCredentialStatus>>();

vi.mock("@/services/controlPlane/git", () => ({
  gitService: {
    getCredential: () => mockGetCredential(),
    saveCredential: vi.fn(),
    deleteCredential: vi.fn(),
    getOAuthURL: () => "https://admin.example.com/auth/github/authorize",
  },
}));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { gitConnections: true },
}));

vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: async () => ({ data: { session: null } }) } },
}));

import { GitConnectionsSettings } from "../GitConnectionsSettings";

function credential(overrides: Partial<GitCredentialStatus> = {}): GitCredentialStatus {
  return {
    available: true,
    hasToken: true,
    provider: "github",
    scopes: "user:email repo",
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("GitConnectionsSettings", () => {
  it("names the connected GitHub account", async () => {
    mockGetCredential.mockResolvedValue(
      credential({ accountLogin: "octocat", kind: "github_app", health: "valid" }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText(/Connected as octocat/)).toBeInTheDocument();
  });

  it("does not show OAuth scopes for a GitHub App, whose access they do not describe", async () => {
    mockGetCredential.mockResolvedValue(
      credential({ accountLogin: "octocat", kind: "github_app", health: "valid" }),
    );
    render(<GitConnectionsSettings />);

    await screen.findByText(/Connected as octocat/);
    expect(screen.queryByText(/Scopes:/)).not.toBeInTheDocument();
    // The kind is stated instead — "GitHub App" also appears in the
    // installations copy, so match the kind line exactly.
    expect(screen.getByText("GitHub App")).toBeInTheDocument();
  });

  it("still shows scopes for a PAT, where they are meaningful", async () => {
    mockGetCredential.mockResolvedValue(
      credential({ kind: "pat", health: "valid", scopes: "repo" }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText(/Scopes: repo/)).toBeInTheDocument();
  });

  it("says the connection needs reconnecting when the refresh token was rejected", async () => {
    mockGetCredential.mockResolvedValue(
      credential({ accountLogin: "octocat", kind: "github_app", health: "needsReconnect" }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText(/reconnect GitHub/i)).toBeInTheDocument();
  });

  it("presents an automatically-renewed token as valid, not as a deadline", async () => {
    mockGetCredential.mockResolvedValue(
      credential({
        accountLogin: "octocat",
        kind: "github_app",
        health: "valid",
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText(/renews automatically/i)).toBeInTheDocument();
  });

  it("lists App installations with a link to manage repository access", async () => {
    mockGetCredential.mockResolvedValue(
      credential({
        accountLogin: "octocat",
        kind: "github_app",
        health: "valid",
        installations: [
          {
            accountLogin: "reliant-labs",
            accountType: "Organization",
            avatarUrl: "",
            configureUrl: "https://github.com/organizations/reliant-labs/settings/installations/1",
            repositorySelection: "selected",
          },
        ],
      }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText("reliant-labs")).toBeInTheDocument();
    const configure = screen.getByRole("link", { name: /configure/i });
    expect(configure).toHaveAttribute(
      "href",
      "https://github.com/organizations/reliant-labs/settings/installations/1",
    );
  });

  it("explains an empty installation list — the reason private repos are invisible", async () => {
    mockGetCredential.mockResolvedValue(
      credential({
        accountLogin: "octocat",
        kind: "github_app",
        health: "valid",
        installations: [],
      }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByText(/isn't installed on any account/i)).toBeInTheDocument();
  });

  it("offers a Reconnect action", async () => {
    mockGetCredential.mockResolvedValue(
      credential({ accountLogin: "octocat", kind: "github_app", health: "valid" }),
    );
    render(<GitConnectionsSettings />);

    expect(await screen.findByRole("button", { name: /reconnect/i })).toBeInTheDocument();
  });

  it("calls the manual fallback a personal access token, not a 'recovery token'", async () => {
    // "Recovery token" named nothing: it is a GitHub PAT you paste in to
    // replace the OAuth connection while debugging an authorization problem.
    mockGetCredential.mockResolvedValue(
      credential({ accountLogin: "octocat", kind: "github_app", health: "valid" }),
    );
    render(<GitConnectionsSettings />);

    await screen.findByText(/Connected as octocat/);
    await waitFor(() => {
      expect(screen.queryByText(/recovery token/i)).not.toBeInTheDocument();
    });
  });
});
