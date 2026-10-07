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

// One array drives both the shared registry poll (useDaemonStatus) and the
// raw client, because the picker reads ONE list and a fixture that set them
// differently would be testing a state the app cannot be in.
let registryDaemons: unknown[] = [];

const mockListDaemons = vi.fn(async () => ({ daemons: registryDaemons }));

// The picker now reads ONE list, from the registry
// (docs/design/one-daemon-list.md), so the grpc client is what gets stubbed.
// It used to poll this list AND control-plane's, and reconcile them by hand.
vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: (...args: unknown[]) => mockListDaemons(...(args as [])),
    }),
  },
}));

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/daemon")>()),
  resumeDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
}));

// Web mode with no ACTIVE registry daemon: activeDaemon undefined is exactly
// the condition that used to blank the whole action card.
// Partial: useDaemonList shares this module's cache key and fetcher.
vi.mock("@/hooks/useDaemonStatus", async (importOriginal) => {
  const { DaemonStatus } = await import("@/gen/reliant/v1/daemon_registry_pb");
  return {
    ...(await importOriginal<typeof import("@/hooks/useDaemonStatus")>()),
    useDaemonStatus: () => ({
      daemons: registryDaemons,
      activeDaemon: (registryDaemons as Array<{ status: number }>).find(
        (d) => d.status === DaemonStatus.ACTIVE,
      ),
      loading: false,
      refresh: vi.fn(),
    }),
  };
});

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
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

const DAEMON_STATUS_ACTIVE = DaemonStatus.ACTIVE;
const DAEMON_STATUS_FAILED = DaemonStatus.FAILED;
const DAEMON_STATUS_SUSPENDED = DaemonStatus.SUSPENDED;

function daemon(id: string, status: number, extra: Record<string, unknown> = {}) {
  return {
    daemonId: id,
    status,
    hostname: id,
    // Cloud rows only: the picker filters the one list by daemon type, and a
    // self-hosted machine is not a clone target.
    daemonType: "managed",
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
  registryDaemons = [];
});

describe("ProjectPicker clone gating — failed-only daemons", () => {
  beforeEach(() => {
    registryDaemons = [
        daemon("machine-a", DAEMON_STATUS_FAILED, {
          lastStatusMessage: "exceeded storage quota",
        }),
      ];
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
    registryDaemons = [daemon("machine-b", DAEMON_STATUS_SUSPENDED)];
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
    expect(
      await screen.findByRole("button", { name: /Resume machine-b/ }),
    ).toBeInTheDocument();
  });
});

describe("ProjectPicker clone gating — an active daemon", () => {
  beforeEach(() => {
    registryDaemons = [daemon("machine-c", DAEMON_STATUS_ACTIVE)];
  });

  it("enables Clone repo and presents it as an immediate pull", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toBeEnabled();
    expect(clone).toHaveTextContent(/Pull a GitHub repo/i);
    expect(clone).not.toHaveTextContent(/queue/i);
  });

  // For a cloud user the directory picker reads the BROWSER HOST's filesystem
  // and cannot see the cloud machine's disk at all, so leading with "Open
  // Project" pointed them at an action that could not work. Clone leads
  // instead — and "Open folder" is demoted, never removed.
  it("leads with Clone from GitHub, ahead of Open folder", async () => {
    renderPicker();
    const clone = await findSettledCloneButton();
    expect(clone).toHaveTextContent(/Clone from GitHub/i);

    const openFolder = screen.queryByTestId("project-picker-open-folder");
    if (openFolder) {
      // DOCUMENT_POSITION_FOLLOWING: clone comes first in the DOM.
      expect(
        clone.compareDocumentPosition(openFolder) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    }
  });
});
