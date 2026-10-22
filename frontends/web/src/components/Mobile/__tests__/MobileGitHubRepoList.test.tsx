/**
 * MobileGitHubRepoList — pagination via `gitService.listRepos`, the same
 * page/perPage/sort call desktop's `RepoSelector`/`useGitRepos` make.
 *
 * `VirtuosoMockContext` is react-virtuoso's escape hatch for jsdom's missing
 * ResizeObserver — without it every row would render as empty.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { VirtuosoMockContext } from "react-virtuoso";
import type { GitRepo } from "../../../services/controlPlane/git/types";

const mocks = vi.hoisted(() => ({
  listRepos: vi.fn(),
  credential: { hasToken: true, isLoading: false },
}));

vi.mock("../../../services/controlPlane/git", () => ({
  gitService: { listRepos: mocks.listRepos },
}));

vi.mock("../../../hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => mocks.credential,
}));

const { MobileGitHubRepoList } = await import("../MobileGitHubRepoList");

function repo(overrides: Partial<GitRepo> & { fullName: string }): GitRepo {
  return {
    cloneUrl: `https://github.com/${overrides.fullName}.git`,
    defaultBranch: "main",
    description: "",
    private: false,
    language: "",
    updatedAt: "",
    ...overrides,
  };
}

function renderList(onBack = vi.fn()) {
  return render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 800, itemHeight: 80 }}>
      <MobileGitHubRepoList onBack={onBack} />
    </VirtuosoMockContext.Provider>,
  );
}

beforeEach(() => {
  mocks.listRepos.mockReset();
  mocks.credential.hasToken = true;
  mocks.credential.isLoading = false;
});

describe("MobileGitHubRepoList", () => {
  it("shows a connect-first empty state when there is no credential", () => {
    mocks.credential.hasToken = false;
    renderList();
    expect(screen.getByText("Connect GitHub first")).toBeInTheDocument();
    expect(mocks.listRepos).not.toHaveBeenCalled();
  });

  it("loads the first page on mount via gitService.listRepos", async () => {
    mocks.listRepos.mockResolvedValue({
      repos: [repo({ fullName: "acme/one" }), repo({ fullName: "acme/two" })],
      hasMore: false,
    });
    renderList();

    await waitFor(() => expect(mocks.listRepos).toHaveBeenCalledWith(1, 30, "updated"));
    expect(await screen.findByText("acme/one")).toBeInTheDocument();
    expect(screen.getByText("acme/two")).toBeInTheDocument();
  });

  it("appends the next page when Virtuoso reaches the end", async () => {
    mocks.listRepos
      .mockResolvedValueOnce({ repos: [repo({ fullName: "acme/one" })], hasMore: true })
      .mockResolvedValueOnce({ repos: [repo({ fullName: "acme/two" })], hasMore: false });
    renderList();

    await screen.findByText("acme/one");
    // The mocked viewport renders every item, which drives Virtuoso's
    // endReached the same way scrolling to the bottom would in a browser.
    await waitFor(() => expect(mocks.listRepos).toHaveBeenCalledTimes(2));
    expect(mocks.listRepos).toHaveBeenNthCalledWith(2, 2, 30, "updated");
    expect(await screen.findByText("acme/two")).toBeInTheDocument();
  });

  it("shows the private badge and description for a repo", async () => {
    mocks.listRepos.mockResolvedValue({
      repos: [
        repo({
          fullName: "acme/secret",
          private: true,
          description: "Internal tooling",
          language: "Go",
        }),
      ],
      hasMore: false,
    });
    renderList();

    await screen.findByText("acme/secret");
    expect(screen.getByText("Private")).toBeInTheDocument();
    expect(screen.getByText("Internal tooling")).toBeInTheDocument();
    expect(screen.getByText("Go")).toBeInTheDocument();
  });

  it("filters the loaded list by the search box", async () => {
    mocks.listRepos.mockResolvedValue({
      repos: [repo({ fullName: "acme/one" }), repo({ fullName: "acme/two" })],
      hasMore: false,
    });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderList();

    await screen.findByText("acme/one");
    await user.type(screen.getByPlaceholderText(/search repositories/i), "two");

    expect(screen.queryByText("acme/one")).not.toBeInTheDocument();
    expect(screen.getByText("acme/two")).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    mocks.listRepos.mockResolvedValue({ repos: [], hasMore: false });
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    renderList(onBack);

    await waitFor(() => expect(mocks.listRepos).toHaveBeenCalled());
    await userEvent.setup().click(screen.getByRole("button", { name: /back to github/i }));
    expect(onBack).toHaveBeenCalled();
  });
});
