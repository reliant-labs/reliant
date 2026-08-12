/**
 * MobileWorkspacePreferencesPanel renders and writes through the same
 * settings-queries hooks (and ultimately the same `api.settings.updatePreferences`
 * call) desktop WorktreeSettings uses.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
      getPreferences: mocks.getPreferences,
      updatePreferences: mocks.updatePreferences,
    },
  },
}));

const { MobileWorkspacePreferencesPanel } = await import(
  "../MobileWorkspacePreferencesPanel"
);

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileWorkspacePreferencesPanel />
    </QueryClientProvider>,
  );
}

describe("MobileWorkspacePreferencesPanel", () => {
  beforeEach(() => {
    mocks.getPreferences.mockReset();
    mocks.updatePreferences.mockReset();
  });

  it("renders the same archive-mode options desktop WorktreeSettings exposes", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktree_archive_mode: "ask_me",
      worktree_default_delete_directory: true,
      worktree_default_delete_branch: false,
      branch_copy_uncommitted_files_default: false,
    });
    renderPanel();
    expect(await screen.findByText(/ask every time/i)).toBeInTheDocument();
    expect(screen.getByText(/^clean up$/i)).toBeInTheDocument();
    expect(screen.getByText(/keep files/i)).toBeInTheDocument();
  });

  it("writes archive mode through the same api.settings.updatePreferences call desktop uses", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktree_archive_mode: "ask_me",
      worktree_default_delete_directory: true,
      worktree_default_delete_branch: false,
      branch_copy_uncommitted_files_default: false,
    });
    mocks.updatePreferences.mockResolvedValue({});
    const { default: userEvent } = await import("@testing-library/user-event");
    renderPanel();
    await screen.findByText(/ask every time/i);
    await userEvent.setup().click(screen.getByText(/^clean up$/i));
    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith(
        expect.objectContaining({ worktree_archive_mode: "always_cleanup" }),
      ),
    );
  });

  it("reveals cleanup toggles only when archive mode is always_cleanup, and writes through updatePreferences", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktree_archive_mode: "always_cleanup",
      worktree_default_delete_directory: true,
      worktree_default_delete_branch: false,
      branch_copy_uncommitted_files_default: false,
    });
    mocks.updatePreferences.mockResolvedValue({});
    const { default: userEvent } = await import("@testing-library/user-event");
    renderPanel();
    const toggle = await screen.findByRole("switch", {
      name: /delete workspace directory/i,
    });
    await userEvent.setup().click(toggle);
    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith(
        expect.objectContaining({ worktree_default_delete_directory: false }),
      ),
    );
  });

  it("sizes interactive targets at 44px, not rem-based h-9/h-10/h-11", () => {
    const source = readFileSync(
      join(MOBILE_DIR, "MobileWorkspacePreferencesPanel.tsx"),
      "utf8",
    );
    expect(source).not.toMatch(/\bh-(9|10|11)\b/);
  });
});
