/**
 * The create-machine dialog's repository and price, as the owner reported
 * them:
 *
 *   "when creating a new machine we say 'Automatic cloning is coming in a
 *    follow-up release.' and have a text box for a repo... that's not what we
 *    do elsewhere, and is a bad ux. and we do support cloning. it should reuse
 *    UI components we use elsewhere. The different sizes don't say their
 *    price...."
 *
 * So this pins three things:
 *
 * 1. The repository is chosen with RepoSelector — the picker onboarding and
 *    the project picker's Clone dialog use — not a free-text box.
 * 2. The picked repo is actually cloned: after CreateDaemon, the new
 *    machine's id goes to CreateProjectFromRepo, the one add-a-repo path.
 *    "No repository" stays a valid choice and clones nothing.
 * 3. Each size card states its hourly price, read from ListPlans'
 *    daemon_pricing rather than from a client table.
 *
 * FAILS ON main: there is no "Choose repository" control, nothing calls
 * CreateProjectFromRepo, and no card renders a price.
 */

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GitRepo } from "@/services/controlPlane/git";

const WIDGETS: GitRepo = {
  fullName: "acme/widgets",
  cloneUrl: "https://github.com/acme/widgets.git",
  defaultBranch: "develop",
  description: "The widget service",
  private: true,
  language: "Go",
  updatedAt: "",
};

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getComputeSubscription: vi.fn(async () => null as unknown),
  getComputeEligibility: vi.fn(async () => ({
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedMinutesRemaining: 0,
    planName: "Compute Medium",
    allowedDaemonSizes: ["small", "medium"],
  })),
  listDaemonTokens: vi.fn(async () => []),
  createEnvironment: vi.fn(async () => ({ id: "d-new", name: "box" }) as unknown),
  createProjectFromRepo: vi.fn(async () => ({
    project: { id: "p-1" },
    projectDaemon: undefined,
    queued: true,
    daemonName: "box",
  }) as unknown),
  loadProjects: vi.fn(async () => {}),
  toast: { info: vi.fn(), success: vi.fn(), error: vi.fn() },
  navigate: vi.fn(),
  repos: [] as GitRepo[],
  daemonPricing: undefined as unknown,
}));

// ListPlans: the per-size price list the cards read.
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    data: { plans: [], daemonPricing: mocks.daemonPricing },
    isLoading: false,
  }),
}));

vi.mock("@/services/controlPlane/billing", () => ({
  getComputeEligibility: mocks.getComputeEligibility,
}));

vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: mocks.caps,
}));

vi.mock("@tanstack/react-router", () => ({
  useSearch: () => ({}),
  useNavigate: () => mocks.navigate,
  useParams: () => ({}),
}));

vi.mock("@/services/controlPlane/environments", () => ({
  DaemonStatus: {
    UNSPECIFIED: 0,
    PENDING: 1,
    ACTIVE: 2,
    SUSPENDED: 3,
    FAILED: 4,
    DISCONNECTED: 5,
  },
  DaemonSize: {
    DAEMON_SIZE_UNSPECIFIED: 0,
    DAEMON_SIZE_SMALL: 1,
    DAEMON_SIZE_MEDIUM: 2,
    DAEMON_SIZE_LARGE: 3,
    DAEMON_SIZE_XL: 4,
    DAEMON_SIZE_2XL: 5,
  },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (e: unknown, fallback = "error") =>
    e instanceof Error && e.message ? e.message : fallback,
  portAccessRulesQueryKey: (id: string) => ["cp", "ports", id],
  getComputeSubscription: mocks.getComputeSubscription,
  listDaemonTokens: mocks.listDaemonTokens,
  getDaemon: vi.fn(),
  createEnvironment: mocks.createEnvironment,
  deleteDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  resumeEnvironment: vi.fn(),
  listPortAccessRules: vi.fn(async () => []),
  setPortAccess: vi.fn(),
  removePortAccess: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: async () => mocks.listDaemons(),
    }),
  },
}));

// The one add-a-repo RPC, shared with onboarding and the project picker.
vi.mock("@/api/project-grpc", () => ({
  projectGrpc: { createProjectFromRepo: mocks.createProjectFromRepo },
}));

vi.mock("@/store/projectStore", () => ({
  useProjectStore: { getState: () => ({ loadProjects: mocks.loadProjects }) },
}));

vi.mock("@/lib/toast-manager", () => ({ toast: mocks.toast }));

// RepoSelector's own dependencies — the real picker renders, fed a repo list.
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useGitRepos: () => ({
    data: { repos: mocks.repos, hasMore: false },
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({
    hasToken: true,
    scopes: "",
    installUrl: "https://github.com/apps/reliant-labs/installations/new",
    installations: [],
    isLoading: false,
    isError: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/services/controlPlane/git", () => ({
  gitService: { getOAuthURL: () => "https://cp.example/auth/github/authorize" },
}));

vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: vi.fn() } },
}));

import { MachinesSection } from "@/components/Settings/cloud/machines";

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MachinesSection />
    </QueryClientProvider>,
  );
}

async function openCreateModal(user: ReturnType<typeof userEvent.setup>) {
  renderSection();
  const openButtons = await screen.findAllByRole("button", { name: /new machine/i });
  await user.click(openButtons[0]);
  return screen.findByRole("dialog");
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.repos = [WIDGETS];
  mocks.daemonPricing = undefined;
});

describe("create-machine repository", () => {
  it("has no free-text repo box and no 'coming soon' copy", async () => {
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    expect(within(modal).queryByText(/automatic cloning is coming/i)).not.toBeInTheDocument();
    expect(within(modal).queryByPlaceholderText(/github\.com\/owner\/repo/i)).not.toBeInTheDocument();
    expect(within(modal).getByRole("button", { name: /choose repository/i })).toBeInTheDocument();
  });

  it("clones the picked repo onto the new machine through CreateProjectFromRepo", async () => {
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    await user.type(within(modal).getByLabelText(/^name$/i), "box");
    await user.click(within(modal).getByRole("button", { name: /choose repository/i }));
    await user.click(await within(modal).findByRole("button", { name: /acme\/widgets/i }));

    // The picker closes onto the choice.
    expect(within(modal).getByTestId("create-machine-selected-repo")).toHaveTextContent("acme/widgets");

    await user.click(within(modal).getByRole("button", { name: /^create$/i }));

    await waitFor(() => expect(mocks.createProjectFromRepo).toHaveBeenCalledTimes(1));
    // The machine first, then the clone — onto THAT machine.
    expect(mocks.createEnvironment).toHaveBeenCalledTimes(1);
    expect(mocks.createEnvironment.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.createProjectFromRepo.mock.invocationCallOrder[0],
    );
    expect(mocks.createEnvironment.mock.calls[0][0]).toMatchObject({
      name: "box",
      gitRepo: WIDGETS.cloneUrl,
      gitBranch: "develop",
    });
    expect(mocks.createProjectFromRepo).toHaveBeenCalledWith({
      cloneUrl: WIDGETS.cloneUrl,
      daemonId: "d-new",
      name: "widgets",
      branch: "develop",
      path: "/home/workspace/projects/widgets",
    });
    // Queued, because the machine is still booting — said, not hidden.
    expect(mocks.toast.info).toHaveBeenCalledWith(expect.stringMatching(/widgets will clone when box is ready/i));
    expect(mocks.loadProjects).toHaveBeenCalled();
  });

  it("keeps 'no repository' a valid choice that clones nothing", async () => {
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    // Open the picker, then back out of it.
    await user.click(within(modal).getByRole("button", { name: /choose repository/i }));
    await user.click(within(modal).getByRole("button", { name: /start without a repository/i }));

    await user.type(within(modal).getByLabelText(/^name$/i), "empty-box");
    await user.click(within(modal).getByRole("button", { name: /^create$/i }));

    await waitFor(() => expect(mocks.createEnvironment).toHaveBeenCalledTimes(1));
    expect(mocks.createEnvironment.mock.calls[0][0]).toMatchObject({ name: "empty-box", gitRepo: undefined });
    expect(mocks.createProjectFromRepo).not.toHaveBeenCalled();
  });

  it("removing a picked repo means no clone", async () => {
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    await user.click(within(modal).getByRole("button", { name: /choose repository/i }));
    await user.click(await within(modal).findByRole("button", { name: /acme\/widgets/i }));
    await user.click(within(modal).getByRole("button", { name: /remove repository/i }));

    await user.type(within(modal).getByLabelText(/^name$/i), "box");
    await user.click(within(modal).getByRole("button", { name: /^create$/i }));

    await waitFor(() => expect(mocks.createEnvironment).toHaveBeenCalledTimes(1));
    expect(mocks.createProjectFromRepo).not.toHaveBeenCalled();
  });

  /**
   * The machine exists by the time the clone is asked for. A failed add must
   * not read as a failed create — that invites a second Create and a second
   * machine — so the dialog closes and the failure is reported beside it.
   */
  it("reports a failed clone without un-creating the machine", async () => {
    mocks.createProjectFromRepo.mockRejectedValueOnce(new Error("no git credential found"));
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    await user.type(within(modal).getByLabelText(/^name$/i), "box");
    await user.click(within(modal).getByRole("button", { name: /choose repository/i }));
    await user.click(await within(modal).findByRole("button", { name: /acme\/widgets/i }));
    await user.click(within(modal).getByRole("button", { name: /^create$/i }));

    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(
        expect.stringMatching(/box was created, but widgets could not be added: no git credential found/i),
      ),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});

describe("create-machine size prices", () => {
  /**
   * Odd numbers on purpose — 17¢ and 41¢ are in no client-side table, so a
   * card that shows them read them from the server's list.
   */
  it("shows each size's hourly price from the server's price list", async () => {
    mocks.daemonPricing = {
      sizes: [
        { size: "small", multiplier: 1n, hourlyPriceCents: "17.0000", storageGib: 25n },
        { size: "medium", multiplier: 2n, hourlyPriceCents: "41", storageGib: 50n },
      ],
      suspendedDiskCentsPerGibMonth: "25",
      placeholder: false,
    };
    const user = userEvent.setup();
    const modal = await openCreateModal(user);
    const sizes = within(modal).getByRole("radiogroup", { name: /size/i });

    const small = within(sizes).getByRole("radio", { name: /^small$/i });
    const medium = within(sizes).getByRole("radio", { name: /^medium$/i });
    expect(within(small).getByTestId("size-price-small")).toHaveTextContent("$0.17/hr");
    expect(within(medium).getByTestId("size-price-medium")).toHaveTextContent("$0.41/hr");
    // The burn rate the price is paired with, from the same row.
    expect(medium).toHaveTextContent(/uses included hours 2× as fast/i);
    // The flat plan rate is not restated under per-size prices.
    expect(within(modal).queryByText(/\/min/)).not.toBeInTheDocument();
    expect(within(modal).getByTestId("size-pricing-note")).not.toHaveTextContent(/provisional/i);
  });

  it("says the prices are provisional while the server marks them placeholders", async () => {
    mocks.daemonPricing = {
      sizes: [
        { size: "small", multiplier: 1n, hourlyPriceCents: "13", storageGib: 25n },
        { size: "medium", multiplier: 2n, hourlyPriceCents: "26", storageGib: 50n },
      ],
      suspendedDiskCentsPerGibMonth: "25",
      placeholder: true,
    };
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    expect(within(modal).getByTestId("size-pricing-note")).toHaveTextContent(/provisional/i);
  });

  it("shows no price for a size the server did not price", async () => {
    mocks.daemonPricing = {
      sizes: [{ size: "small", multiplier: 1n, hourlyPriceCents: "13", storageGib: 25n }],
      suspendedDiskCentsPerGibMonth: "25",
      placeholder: false,
    };
    const user = userEvent.setup();
    const modal = await openCreateModal(user);

    expect(within(modal).getByTestId("size-price-small")).toHaveTextContent("$0.13/hr");
    expect(within(modal).queryByTestId("size-price-medium")).not.toBeInTheDocument();
  });
});
