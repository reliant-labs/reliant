/**
 * The first-run state's add-project actions in WEB mode, per daemon set
 * (they are the Add project dialog's too: both read useAddProject).
 *
 * The reported bug this guards: in web mode with no ACTIVE daemon, the old
 * project picker replaced BOTH add actions with a "Resume a daemon" screen
 * that only handled SUSPENDED daemons. A user whose only machine had FAILED
 * saw no action at all. See docs/findings/add-github-project-2026-09-30.md.
 *
 * These render the real component against a failed-only, suspended-only and
 * active daemon set, and assert the user always has a way forward.
 */
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to: _to, params: _params, ...props }: { children?: ReactNode; to?: string; params?: unknown }) => (
    <a {...props}>{children}</a>
  ),
}));

vi.mock("@/store/openChat", () => ({ openNewChat: vi.fn() }));
// The Add machine sheet is WS6's (its own tests); here only whether it opens.
vi.mock("@/components/Machines", () => ({
  AddMachineSheet: ({ open }: { open: boolean }) => (open ? <div data-testid="add-machine-sheet" /> : null),
}));

import { NoProjectsState } from "../NoProjectsState";
import { useAddProjectDialogStore } from "@/store/addProjectDialogStore";
import { markMachineGone, resetGoneMachinesForTest } from "@/lib/goneMachines";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport } from "@/test/daemonRegistry";

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: () => mockListDaemons() })));
afterEach(() => setTransport(null));

const DAEMON_STATUS_ACTIVE = DaemonStatus.ACTIVE;
const DAEMON_STATUS_FAILED = DaemonStatus.FAILED;
const DAEMON_STATUS_SUSPENDED = DaemonStatus.SUSPENDED;

function daemon(id: string, status: number, extra: Record<string, unknown> = {}) {
  return {
    daemonId: id,
    status,
    // The control plane's name for the machine; a managed machine's hostname
    // is its pod's and is never what the UI calls it.
    name: id,
    hostname: `ws-ws-${id}`,
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
  const clone = await screen.findByTestId("add-project-clone");
  await waitFor(() => expect(clone.textContent ?? "").not.toBe(""));
  return clone;
}

function renderFirstRun() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<NoProjectsState />, { wrapper: Wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
  registryDaemons = [];
  resetGoneMachinesForTest();
  useAddProjectDialogStore.setState({ isOpen: false, method: null });
});

describe("first run — copy", () => {
  it("says what a project is, and offers exactly the two ways to add one", async () => {
    registryDaemons = [daemon("machine-c", DAEMON_STATUS_ACTIVE)];
    renderFirstRun();
    expect(screen.getByText(/A project is a codebase Reliant works in/)).toBeInTheDocument();
    expect(screen.getByText(/clone a repository or open a folder/i)).toBeInTheDocument();
    expect(await screen.findByTestId("add-project-clone")).toHaveTextContent("Clone a repository");
    expect(screen.getByTestId("add-project-open-folder")).toHaveTextContent("Open a folder");
    expect(screen.queryByText(/new project/i)).toBeNull();
  });

  it("opens the Add project dialog on its clone step", async () => {
    registryDaemons = [daemon("machine-c", DAEMON_STATUS_ACTIVE)];
    renderFirstRun();
    (await findSettledCloneButton()).click();
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: "clone" });
  });
});

describe("first run — failed-only daemons", () => {
  beforeEach(() => {
    registryDaemons = [
      daemon("machine-a", DAEMON_STATUS_FAILED, {
        lastStatusMessage: "exceeded storage quota",
      }),
    ];
  });

  it("still offers Clone a repository rather than hiding every add-project entry point", async () => {
    renderFirstRun();
    expect(await screen.findByTestId("add-project-clone")).toBeInTheDocument();
  });

  it("disables Clone a repository and says why", async () => {
    renderFirstRun();
    const clone = await findSettledCloneButton();
    expect(clone).toBeDisabled();
    expect(clone).toHaveTextContent(/failed to start/i);
  });

  it("shows the failure reason, a delete action, and where to add a machine", async () => {
    renderFirstRun();
    const row = await screen.findByTestId("failed-daemon-machine-a");
    expect(row).toHaveTextContent("exceeded storage quota");
    expect(within(row).getByRole("button", { name: /delete/i })).toBeInTheDocument();
    // Adding a machine opens the one Add machine sheet in place.
    const add = screen.getByTestId("no-active-machine-add");
    expect(add).toHaveTextContent("Add a machine");
    expect(screen.queryByTestId("add-machine-sheet")).not.toBeInTheDocument();
    fireEvent.click(add);
    expect(screen.getByTestId("add-machine-sheet")).toBeInTheDocument();
  });

  it("explains why Open a folder is unavailable instead of hiding it", async () => {
    renderFirstRun();
    const open = await screen.findByTestId("add-project-open-folder");
    expect(open).toBeDisabled();
    expect(open).toHaveTextContent(/connect a machine first/i);
  });
});

describe("first run — suspended-only daemons", () => {
  beforeEach(() => {
    registryDaemons = [daemon("machine-b", DAEMON_STATUS_SUSPENDED)];
  });

  it("enables Clone a repository — the command queues until the machine wakes", async () => {
    renderFirstRun();
    const clone = await findSettledCloneButton();
    expect(clone).toBeEnabled();
    expect(clone).toHaveTextContent(/queue/i);
    expect(clone).toHaveTextContent(/machine-b/);
  });

  it("still offers Resume for the suspended machine", async () => {
    renderFirstRun();
    expect(await screen.findByRole("button", { name: /Resume machine-b/ })).toBeInTheDocument();
  });
});

describe("first run — an active daemon", () => {
  beforeEach(() => {
    registryDaemons = [daemon("machine-c", DAEMON_STATUS_ACTIVE)];
  });

  it("shows no machine panel: a project is not tied to the connected machine", async () => {
    renderFirstRun();
    await findSettledCloneButton();
    expect(screen.queryByTestId("no-active-machine")).toBeNull();
    expect(screen.queryByText(/Projects open on this machine/)).toBeNull();
  });

  it("leads with Clone a repository, ahead of Open a folder, for a cloud account", async () => {
    renderFirstRun();
    const clone = await findSettledCloneButton();
    expect(clone).toHaveAttribute("data-variant", "primary");
    const openFolder = screen.getByTestId("add-project-open-folder");
    expect(clone.compareDocumentPosition(openFolder) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("first run — clone target", () => {
  beforeEach(() => {
    registryDaemons = [
      daemon("ghost", DaemonStatus.PENDING, { createdAt: { seconds: 2_000n, nanos: 0 } }),
      daemon("live", DAEMON_STATUS_SUSPENDED, { createdAt: { seconds: 1_000n, nanos: 0 } }),
    ];
  });

  it("never defaults to a machine the control plane says is gone", async () => {
    markMachineGone("ghost");
    renderFirstRun();
    const clone = await findSettledCloneButton();
    expect(clone).toHaveTextContent(/live/);
    expect(clone).not.toHaveTextContent(/ghost/);
  });
});
