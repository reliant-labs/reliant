/**
 * MobileWorkspacePreferencesPanel renders and writes through the same
 * settings-queries hooks (and ultimately the same generated UpdatePreferences
 * mutation) desktop WorktreeSettings uses. SettingsService is answered at the
 * transport.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
}));

const { setTransport } = await import("@/lib/connect");
const { mockTransport } = await import("@/lib/test-utils");

// `usePreferences` is gated on having a session — GetPreferences is an
// authenticated RPC and must not fire on the signed-out sign-in screen. This
// panel is only reachable signed in, so the session here is what the real
// surface always has; without it the hook correctly serves DEFAULT_PREFERENCES
// and never reads the mocked remote values these tests are about.
vi.mock("@/store/authStore", () => {
  const state = { session: { access_token: "test-token" } };
  const useAuthStore = <T,>(selector: (s: typeof state) => T): T => selector(state);
  useAuthStore.getState = () => state;
  return { useAuthStore };
});

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
    setTransport(
      mockTransport({
        "SettingsService.GetPreferences": () => mocks.getPreferences(),
        "SettingsService.UpdatePreferences": (req) => mocks.updatePreferences(req),
      }),
    );
  });
  afterEach(() => setTransport(null));

  it("renders the same archive-mode options desktop WorktreeSettings exposes", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktreeArchiveMode: "ask_me",
      worktreeDefaultDeleteBranch: false,
      branchCopyUncommittedFilesDefault: false,
    });
    renderPanel();
    expect(await screen.findByText(/ask every time/i)).toBeInTheDocument();
    expect(screen.getByText(/^clean up$/i)).toBeInTheDocument();
    expect(screen.getByText(/keep files/i)).toBeInTheDocument();
  });

  it("writes archive mode through the same UpdatePreferences call desktop uses", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktreeArchiveMode: "ask_me",
      worktreeDefaultDeleteBranch: false,
      branchCopyUncommittedFilesDefault: false,
    });
    mocks.updatePreferences.mockResolvedValue({ success: true });
    const { default: userEvent } = await import("@testing-library/user-event");
    renderPanel();
    await screen.findByText(/ask every time/i);
    await userEvent.setup().click(screen.getByText(/^clean up$/i));
    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith(
        expect.objectContaining({ worktreeArchiveMode: "always_cleanup" }),
      ),
    );
  });

  it("reveals the branch toggle only when archive mode is always_cleanup, and writes through updatePreferences", async () => {
    mocks.getPreferences.mockResolvedValue({
      worktreeArchiveMode: "always_cleanup",
      worktreeDefaultDeleteBranch: false,
      branchCopyUncommittedFilesDefault: false,
    });
    mocks.updatePreferences.mockResolvedValue({ success: true });
    const { default: userEvent } = await import("@testing-library/user-event");
    renderPanel();
    // The directory is no longer a preference: the machine decides.
    await screen.findByRole("switch", { name: /delete git branch/i });
    expect(screen.queryByRole("switch", { name: /delete workspace directory/i })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("switch", { name: /delete git branch/i }));
    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith(
        expect.objectContaining({ worktreeDefaultDeleteBranch: true }),
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
