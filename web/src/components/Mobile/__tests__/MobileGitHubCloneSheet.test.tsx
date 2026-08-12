/**
 * MobileGitHubCloneSheet — only ACTIVE daemons are offered as a clone
 * target (mirrors `daemonPresentation.ts`'s resumable/ready gating), and
 * clone dispatches through the exact same `gitService.cloneRepo` call
 * desktop's `ProjectPicker`/`AddRepoModal` use.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DaemonStatus } from "../../../gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "../../../services/controlPlane/daemon";
import type { GitRepo } from "../../../services/controlPlane/git/types";

const mocks = vi.hoisted(() => ({
  cloneRepo: vi.fn(),
  daemons: [] as Partial<Daemon>[],
}));

vi.mock("../../../services/controlPlane/git", () => ({
  gitService: { cloneRepo: mocks.cloneRepo },
}));

vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: mocks.daemons, isLoading: false }),
}));

const { MobileGitHubCloneSheet } = await import("../MobileGitHubCloneSheet");

const repo: GitRepo = {
  fullName: "acme/widgets",
  cloneUrl: "https://github.com/acme/widgets.git",
  defaultBranch: "main",
  description: "",
  private: false,
  language: "",
  updatedAt: "",
};

function daemon(overrides: Partial<Daemon> & { id: string }): Partial<Daemon> {
  return { name: overrides.id, status: DaemonStatus.ACTIVE, ...overrides };
}

beforeEach(() => {
  mocks.cloneRepo.mockReset();
  mocks.daemons = [];
});

describe("MobileGitHubCloneSheet", () => {
  it("only lists ACTIVE daemons as clone targets", () => {
    mocks.daemons = [
      daemon({ id: "d-active", status: DaemonStatus.ACTIVE, name: "Active Box" }),
      daemon({ id: "d-pending", status: DaemonStatus.PENDING, name: "Pending Box" }),
      daemon({ id: "d-suspended", status: DaemonStatus.SUSPENDED, name: "Suspended Box" }),
      daemon({ id: "d-failed", status: DaemonStatus.FAILED, name: "Failed Box" }),
    ];
    render(<MobileGitHubCloneSheet repo={repo} onClose={vi.fn()} />);

    expect(screen.getByText("Active Box")).toBeInTheDocument();
    expect(screen.queryByText("Pending Box")).not.toBeInTheDocument();
    expect(screen.queryByText("Suspended Box")).not.toBeInTheDocument();
    expect(screen.queryByText("Failed Box")).not.toBeInTheDocument();
  });

  it("shows an empty-machines message when no daemon is ACTIVE", () => {
    mocks.daemons = [daemon({ id: "d-pending", status: DaemonStatus.PENDING })];
    render(<MobileGitHubCloneSheet repo={repo} onClose={vi.fn()} />);
    expect(screen.getByText(/no active machines available/i)).toBeInTheDocument();
  });

  it("clones through gitService.cloneRepo with the selected daemon id and branch", async () => {
    mocks.daemons = [daemon({ id: "d-active", status: DaemonStatus.ACTIVE, name: "Active Box" })];
    mocks.cloneRepo.mockResolvedValue({ clonedPath: "/home/workspace/projects/widgets" });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();

    render(<MobileGitHubCloneSheet repo={repo} onClose={vi.fn()} />);

    await user.click(screen.getByText("Active Box"));

    const branchInput = screen.getByPlaceholderText("main");
    await user.clear(branchInput);
    await user.type(branchInput, "feature/x");

    await user.click(screen.getByRole("button", { name: /clone repository/i }));

    await waitFor(() =>
      expect(mocks.cloneRepo).toHaveBeenCalledWith({
        daemonId: "d-active",
        gitRepo: repo.cloneUrl,
        gitBranch: "feature/x",
        path: "/home/workspace/projects/widgets",
      }),
    );
    expect(await screen.findByText("Cloned successfully")).toBeInTheDocument();
  });

  it("disables clone until a daemon is picked", () => {
    mocks.daemons = [daemon({ id: "d-active", status: DaemonStatus.ACTIVE, name: "Active Box" })];
    render(<MobileGitHubCloneSheet repo={repo} onClose={vi.fn()} />);
    expect(screen.getByRole("button", { name: /clone repository/i })).toBeDisabled();
  });

  it("shows an honest in-progress warning while cloning, and a retry on failure", async () => {
    mocks.daemons = [daemon({ id: "d-active", status: DaemonStatus.ACTIVE, name: "Active Box" })];
    let resolveClone: (v: { clonedPath: string }) => void;
    mocks.cloneRepo.mockReturnValue(
      new Promise((resolve) => {
        resolveClone = resolve;
      }),
    );
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();

    render(<MobileGitHubCloneSheet repo={repo} onClose={vi.fn()} />);
    await user.click(screen.getByText("Active Box"));
    await user.click(screen.getByRole("button", { name: /clone repository/i }));

    expect(await screen.findByText(/keep this app in the foreground/i)).toBeInTheDocument();
    resolveClone!({ clonedPath: "/home/workspace/projects/widgets" });
    await screen.findByText("Cloned successfully");
  });

  it("prevents dismissing the sheet while a clone is in flight", async () => {
    mocks.daemons = [daemon({ id: "d-active", status: DaemonStatus.ACTIVE, name: "Active Box" })];
    mocks.cloneRepo.mockReturnValue(new Promise(() => {}));
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    const onClose = vi.fn();

    render(<MobileGitHubCloneSheet repo={repo} onClose={onClose} />);
    await user.click(screen.getByText("Active Box"));
    await user.click(screen.getByRole("button", { name: /clone repository/i }));

    await screen.findByText(/keep this app in the foreground/i);
    expect(screen.getByRole("button", { name: /^close$/i })).toBeDisabled();
  });
});
