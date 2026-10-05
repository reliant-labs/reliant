/**
 * ProjectPicker's page-level structure: the header's add-project actions, the
 * zero-projects start panel, and the machine status strip.
 *
 * The list and clone-gating suites cover the table and the per-daemon clone
 * rules; this one pins what the redesign added on top — that every way to
 * start is offered when there are no projects, that the header names the
 * lead action, and that the connected machine is stated in one place.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { Project } from "@/store/projectStore";

vi.stubGlobal(
  "matchMedia",
  vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })),
);

// Mutable per test: cloud on/off and the registry's daemon set.
const env = vi.hoisted(() => ({
  cloudDaemons: false,
  daemons: [] as Array<Record<string, unknown>>,
  activeDaemon: undefined as Record<string, unknown> | undefined,
  projects: [] as unknown[],
}));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: {
    get cloudDaemons() {
      return env.cloudDaemons;
    },
    managedCredits: false,
    gitConnections: false,
  },
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: vi.fn(async () => ({ daemons: env.daemons })),
    }),
  },
}));

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/daemon")>()),
  resumeDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: env.daemons,
    activeDaemon: env.activeDaemon,
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
      projects: env.projects,
      loadProjects: vi.fn(),
      createProject: vi.fn(),
      selectProject: vi.fn(),
      updateProject: vi.fn(),
      deleteProject: vi.fn(),
    });
  useProjectStore.getState = () => ({ projects: env.projects });
  return { useProjectStore };
});

vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: (selector: (s: unknown) => unknown) =>
    selector({ ensureApiKeyOrShowModal: vi.fn() }),
}));

import { ProjectPicker } from "../ProjectPicker";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

function makeProject(name: string, path: string): Project {
  const now = new Date().toISOString();
  return {
    id: `id-${name}`,
    name,
    path,
    is_git_repo: true,
    worktree_count: 0,
    last_active: now,
    created_at: now,
    updated_at: now,
    is_forge: false,
  } as Project;
}

function renderPicker() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<ProjectPicker onProjectSelected={vi.fn()} />, { wrapper });
}

const cloudMachine = {
  daemonId: "cloud-1",
  hostname: "cloud-1",
  status: DaemonStatus.ACTIVE,
  daemonType: "managed",
  lastStatusMessage: "",
};

beforeEach(() => {
  env.cloudDaemons = false;
  env.daemons = [];
  env.activeDaemon = { daemonId: "local-1", hostname: "my-laptop", status: DaemonStatus.ACTIVE, daemonType: "self_hosted" };
  env.projects = [];
});

describe("ProjectPicker with no projects", () => {
  it("offers all three ways to start for a cloud account", () => {
    env.cloudDaemons = true;
    env.daemons = [cloudMachine];
    env.activeDaemon = cloudMachine;
    renderPicker();

    const empty = screen.getByTestId("project-picker-empty");
    expect(within(empty).getByTestId("project-picker-clone-repo")).toBeEnabled();
    expect(within(empty).getByTestId("project-picker-open-folder")).toBeEnabled();
    expect(within(empty).getByTestId("project-picker-new-project")).toBeEnabled();
  });

  it("does not duplicate the start actions in the page header", () => {
    renderPicker();
    // Exactly one of each: the tiles are the actions on this state.
    expect(screen.getAllByTestId("project-picker-open-folder")).toHaveLength(1);
    expect(screen.getAllByTestId("project-picker-new-project")).toHaveLength(1);
  });

  it("omits clone on a local-only install, where cloning does not exist", () => {
    renderPicker();
    expect(screen.queryByTestId("project-picker-clone-repo")).not.toBeInTheDocument();
    expect(screen.getByTestId("project-picker-open-folder")).toBeInTheDocument();
  });

  it("opens the create-project dialog from New project", async () => {
    const user = userEvent.setup();
    renderPicker();
    await user.click(screen.getByTestId("project-picker-new-project"));
    expect(screen.getByRole("button", { name: /create project/i })).toBeInTheDocument();
  });
});

describe("ProjectPicker header actions", () => {
  beforeEach(() => {
    env.projects = [makeProject("alpha", "/Users/dev/src/alpha")];
  });

  it("marks Open folder as the primary action for a local machine", () => {
    renderPicker();
    expect(screen.getByTestId("project-picker-open-folder")).toHaveAttribute("data-variant", "primary");
    expect(screen.getByTestId("project-picker-new-project")).toHaveAttribute("data-variant", "secondary");
  });

  it("marks Clone as the primary action on a cloud machine", () => {
    env.cloudDaemons = true;
    env.daemons = [cloudMachine];
    env.activeDaemon = cloudMachine;
    renderPicker();
    expect(screen.getByTestId("project-picker-clone-repo")).toHaveAttribute("data-variant", "primary");
    expect(screen.getByTestId("project-picker-open-folder")).toHaveAttribute("data-variant", "secondary");
  });

  it("names the connected machine in one status strip", () => {
    renderPicker();
    const strip = screen.getByTestId("machine-status-strip");
    expect(strip).toHaveTextContent("my-laptop");
    expect(strip).toHaveTextContent(/self-hosted/i);
  });

  it("tags a project cloned onto a cloud machine", () => {
    env.projects = [
      makeProject("alpha", "/Users/dev/src/alpha"),
      makeProject("cloudy", "/home/workspace/projects/cloudy"),
    ];
    renderPicker();
    const [first, second] = screen.getAllByTestId("project-item");
    const rows = [first, second];
    const cloudRow = rows.find((r) => r.textContent?.includes("cloudy"))!;
    const localRow = rows.find((r) => r.textContent?.includes("alpha"))!;
    expect(within(cloudRow).getByText("Cloud")).toBeInTheDocument();
    expect(within(localRow).queryByText("Cloud")).not.toBeInTheDocument();
  });
});

describe("ProjectPicker with no active machine (web)", () => {
  it("shows the connect call to action on a local-only install, and hides Open folder", () => {
    env.activeDaemon = undefined;
    renderPicker();
    const panel = screen.getByTestId("no-active-machine");
    expect(panel).toHaveTextContent(/no machine connected/i);
    expect(within(panel).getByRole("button", { name: /connect a machine/i })).toBeInTheDocument();
    // There is no filesystem to browse without a machine.
    expect(screen.queryByTestId("project-picker-open-folder")).not.toBeInTheDocument();
  });
});
