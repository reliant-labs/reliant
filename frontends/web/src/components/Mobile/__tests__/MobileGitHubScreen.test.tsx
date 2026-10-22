/**
 * MobileGitHubScreen composes the connection panel and the repo browser
 * behind one back-stack slot — pins that the "Clone a repo" entry point only
 * shows once a credential exists, and that it hands off to the repo list.
 */

import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  credential: { hasToken: false, scopes: "", isLoading: false, isError: false, refresh: vi.fn() },
}));

vi.mock("../../../hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => mocks.credential,
}));

vi.mock("../../../services/controlPlane/git", () => ({
  gitService: {
    getOAuthURL: vi.fn(),
    deleteCredential: vi.fn(),
    listRepos: vi.fn().mockResolvedValue({ repos: [], hasMore: false }),
  },
}));

vi.mock("../../../lib/supabase", () => ({
  supabase: { auth: { getSession: vi.fn() } },
}));

const { MobileGitHubScreen } = await import("../MobileGitHubScreen");

beforeEach(() => {
  mocks.credential.hasToken = false;
  window.history.replaceState({}, "", "/m/settings");
});

describe("MobileGitHubScreen", () => {
  it("does not offer repo browsing when there is no credential", () => {
    render(<MobileGitHubScreen onBack={vi.fn()} />);
    expect(screen.queryByText("Clone a repo")).not.toBeInTheDocument();
  });

  it("offers repo browsing once GitHub is connected", () => {
    mocks.credential.hasToken = true;
    render(<MobileGitHubScreen onBack={vi.fn()} />);
    expect(screen.getByText("Clone a repo")).toBeInTheDocument();
  });

  it("navigates into the repo list and back without leaving the screen", async () => {
    mocks.credential.hasToken = true;
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    render(<MobileGitHubScreen onBack={vi.fn()} />);

    await user.click(screen.getByText("Clone a repo"));
    expect(screen.getByRole("heading", { name: "Clone a repo" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /back to github/i }));
    expect(screen.getByText("GitHub connected")).toBeInTheDocument();
  });

  it("calls onBack from the settings drill-in header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobileGitHubScreen onBack={onBack} />);
    await userEvent.setup().click(screen.getByRole("button", { name: /back to settings/i }));
    expect(onBack).toHaveBeenCalled();
  });

  it("renders as a top-level drawer destination when onBack is omitted", () => {
    render(<MobileGitHubScreen />);
    expect(screen.getByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /open menu/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /back to settings/i })).not.toBeInTheDocument();
  });
});
