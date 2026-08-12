/**
 * MobileGitHubPanel — pins that it reads/writes through the exact same
 * `useGitHubCredential` hook and `gitService.getOAuthURL` /
 * `deleteCredential` calls desktop `GitConnectionsSettings` uses. A
 * divergent write path here would be a real bug, not a UI difference.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getOAuthURL: vi.fn(),
  deleteCredential: vi.fn(),
  getSession: vi.fn(),
  refresh: vi.fn(),
  credential: { hasToken: false, scopes: "", isLoading: false, isError: false },
}));

vi.mock("../../../services/controlPlane/git", () => ({
  gitService: {
    getOAuthURL: mocks.getOAuthURL,
    deleteCredential: mocks.deleteCredential,
  },
}));

vi.mock("../../../lib/supabase", () => ({
  supabase: { auth: { getSession: mocks.getSession } },
}));

vi.mock("../../../hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({ ...mocks.credential, refresh: mocks.refresh }),
}));

const { MobileGitHubPanel } = await import("../MobileGitHubPanel");

beforeEach(() => {
  mocks.getOAuthURL.mockReset().mockReturnValue("https://cp.example.com/auth/github/authorize");
  mocks.deleteCredential.mockReset().mockResolvedValue(undefined);
  mocks.getSession.mockReset().mockResolvedValue({
    data: { session: { access_token: "tok-123" } },
  });
  mocks.refresh.mockReset();
  mocks.credential.hasToken = false;
  mocks.credential.scopes = "";
  mocks.credential.isLoading = false;
  window.history.replaceState({}, "", "/m/settings");
});

describe("MobileGitHubPanel", () => {
  it("shows a not-connected state when there is no token", () => {
    render(<MobileGitHubPanel />);
    expect(screen.getByText("Not connected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /connect with github/i })).toBeInTheDocument();
  });

  it("shows a connected state with scopes when a token is present", () => {
    mocks.credential.hasToken = true;
    mocks.credential.scopes = "repo";
    render(<MobileGitHubPanel />);
    expect(screen.getByText("GitHub connected")).toBeInTheDocument();
    expect(screen.getByText(/scopes: repo/i)).toBeInTheDocument();
  });

  it("starts OAuth through the same gitService.getOAuthURL + Supabase session desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();

    render(<MobileGitHubPanel />);
    await user.click(screen.getByRole("button", { name: /connect with github/i }));

    await waitFor(() => expect(mocks.getOAuthURL).toHaveBeenCalled());
    expect(mocks.getSession).toHaveBeenCalled();
  });

  it("disconnects through the exact same gitService.deleteCredential('github') call desktop uses", async () => {
    mocks.credential.hasToken = true;
    mocks.credential.scopes = "repo";
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    render(<MobileGitHubPanel />);

    await user.click(screen.getByRole("button", { name: /^disconnect$/i }));
    await user.click(screen.getByRole("button", { name: /^disconnect$/i }));

    await waitFor(() => expect(mocks.deleteCredential).toHaveBeenCalledWith("github"));
    await waitFor(() => expect(mocks.refresh).toHaveBeenCalled());
  });

  it("consumes and strips github_connected on return from the OAuth callback", async () => {
    window.history.replaceState({}, "", "/m/settings?github_connected=true");
    render(<MobileGitHubPanel />);

    await waitFor(() => expect(mocks.refresh).toHaveBeenCalled());
    expect(window.location.search).toBe("");
  });

  it("surfaces and strips github_error on a failed OAuth return", () => {
    window.history.replaceState(
      {},
      "",
      "/m/settings?github_error=access_denied&github_error_msg=User+cancelled",
    );
    render(<MobileGitHubPanel />);

    expect(screen.getByText("User cancelled")).toBeInTheDocument();
    expect(window.location.search).toBe("");
  });

  it("warns and offers reauthorize when connected without repo scope", () => {
    mocks.credential.hasToken = true;
    mocks.credential.scopes = "read:user";
    render(<MobileGitHubPanel />);
    expect(screen.getByText(/this token has no/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /reauthorize github/i })).toBeInTheDocument();
  });
});
