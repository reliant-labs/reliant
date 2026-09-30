/**
 * ProjectPicker's add-project affordances in WEB mode, per daemon set.
 *
 * The reported bug: in web mode with no ACTIVE daemon, the picker replaced
 * BOTH "Open Project" and "Clone repo" with a "Resume a daemon" screen that
 * only handled SUSPENDED daemons. A user whose only machine had FAILED
 * therefore saw no action at all, and no surface anywhere in the app offered
 * "add project". See docs/findings/add-github-project-2026-09-30.md.
 *
 * These render the real component against a failed-only, suspended-only and
 * active daemon set, and assert the user always has a way forward.
 */
import { render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

vi.stubGlobal(
  "matchMedia",
  vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })),
);

// Cloud daemons ON — this is the hosted shape where clone exists at all.
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));

const mockListDaemons = vi.fn(async () => ({ daemons: [] as unknown[] }));

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/daemon")>()),
  listDaemons: (...args: unknown[]) => mockListDaemons(...(args as [])),
  resumeDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
  hasActiveDaemon: () => false,
}));

// Web mode with no ACTIVE registry daemon: activeDaemon undefined is exactly
// the condition that used to blank the whole action card.
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({ hasToken: true }),
}));

vi.mock("@/hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useOnboardingQueries")>()),
  useResumeDaemon: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useCreateDaemon: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/services/settingsSync", () => ({
  settingsSync: { getSetting: () => "", setSetting: vi.fn() },
  SETTINGS_KEYS: { THEME: "appearance.theme" },
}));

vi.mock("@/store/projectStore", () => {
  const useProjectStore = (selector: (s: unknown) => unknown) =>
    selector({
      projects: [],
      loadProjects: vi.fn(),
      createProject: vi.fn(),
      selectProject: vi.fn(),
      updateProject: vi.fn(),
      deleteProject: vi.fn(),
    });
  useProjectStore.getState = () => ({ projects: [] });
  return { useProjectStore };
});

vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: (selector: (s: unknown) => unknown) =>
    selector({ ensureApiKeyOrShowModal: vi.fn() }),
}));

import { ProjectPicker } from "../ProjectPicker";
import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_FAILED,
  DAEMON_STATUS_SUSPENDED,
} from "@/services/controlPlane/daemon";

function daemon(id: string, status: number, extra: Record<string, unknown> = {}) {
  return {
    id,
    name: id,
    status,
    hostname: id,
    lastStatusMessage: "",
    ...extra,
  };
}

// The clone button renders immediately, in a not-yet-loaded state, and only
// settles once the daemon list resolves. Asserting on the first match would
// therefore test the loading state rather than the gating rule.
async function findSettledCloneButton(): Promise<HTMLElement> {
  const clone = await screen.findByTestId("project-picker-clone-repo");
  await waitFor(() => expect(clone.textContent ?? "").not.toBe(""));
  return clone;
}

function renderPicker() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<ProjectPicker onProjectSelected={vi.fn()} />, { wrapper: Wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
  mockListDaemons.mockResolvedValue({ daemons: [] });
});

describe("ProjectPicker clone gating — failed-only daemons", () => {
  beforeEach(() => {
    mockListDaemons.mockResolvedValue({
      daemons: [
        daemon("machine-a", DAEMON_STATUS_FAILED, {
          lastStatusMessage: "exceeded storage quota",
        }),
      ],
    });
  });

  it("still offers Clone repo rather than hiding every add-project entry point", async () => {
    renderPicker();
    // The regression was an ABSENT control, so presence is the assertion.
    expect(await screen.findByTestId("project-picker-clone-repo")).toBeInTheDocument();
  });

  it("disables Clone repo and says why", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toBeDisabled();
    expect(clone).toHaveTextContent(/failed to start/i);
  });

  it("shows the failure reason and a delete action for the failed machine", async () => {
    renderPicker();
    const row = await screen.findByTestId("failed-daemon-machine-a");
    // Without the reason the user cannot tell a quota problem from a crash.
    expect(row).toHaveTextContent("exceeded storage quota");
    expect(within(row).getByRole("button", { name: /delete/i })).toBeInTheDocument();
  });

  it("does not offer a resume action for a machine that cannot be resumed", async () => {
    renderPicker();
    await screen.findByTestId("failed-daemon-machine-a");
    expect(screen.queryByText(/^Resume machine-a$/)).not.toBeInTheDocument();
  });
});

describe("ProjectPicker clone gating — suspended-only daemons", () => {
  beforeEach(() => {
    mockListDaemons.mockResolvedValue({
      daemons: [daemon("machine-b", DAEMON_STATUS_SUSPENDED)],
    });
  });

  it("enables Clone repo — the command queues until the machine wakes", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toBeEnabled();
  });

  it("tells the user the clone is queued, not done", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toHaveTextContent(/queue/i);
    expect(clone).toHaveTextContent(/machine-b/);
  });

  it("still offers Resume for the suspended machine", async () => {
    renderPicker();
    expect(await screen.findByText(/Resume machine-b/)).toBeInTheDocument();
  });
});

describe("ProjectPicker clone gating — an active daemon", () => {
  beforeEach(() => {
    mockListDaemons.mockResolvedValue({
      daemons: [daemon("machine-c", DAEMON_STATUS_ACTIVE)],
    });
  });

  it("enables Clone repo and presents it as an immediate pull", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toBeEnabled();
    expect(clone).toHaveTextContent(/Pull a GitHub repo/i);
    expect(clone).not.toHaveTextContent(/queue/i);
  });
});
