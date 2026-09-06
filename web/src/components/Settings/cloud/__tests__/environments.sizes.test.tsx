/**
 * The create-machine size picker obeys the SAME server rule the purchase grid
 * does.
 *
 * Sizing is asked twice, and the two questions are genuinely different:
 * billing asks "which plan do I have to buy to run a Medium?", environments
 * asks "which sizes may I run right now?". So the CONTROL is mirrored — but
 * the RULE must not be. `PlanLimits.allowed_daemon_sizes` is the one answer,
 * and this file pins that environments reads it rather than re-deriving it.
 *
 * It also pins the removal of a second invented price. The size tiers here
 * carried a hardcoded "$0.02/min … $0.16/min" table — the same defect class as
 * the per-plan-id price tables that were deleted from billingUtils, and worse
 * placed, because it sat on the button that spends the money. The only rate
 * the server states is the plan's overage rate, so that is what may be shown.
 */

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  caps: { cloudDaemons: true },
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  getComputeSubscription: vi.fn(async () => ({}) as unknown),
  listDaemonTokens: vi.fn(async () => []),
  createEnvironment: vi.fn(async () => ({})),
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

vi.mock("@tanstack/react-router", () => ({ useSearch: () => ({}) }));

vi.mock("@/services/controlPlane/environments", () => ({
  DaemonStatus: {
    UNSPECIFIED: 0,
    PENDING: 1,
    ACTIVE: 2,
    SUSPENDED: 3,
    FAILED: 4,
    DISCONNECTED: 5,
  },
  DaemonSize: { UNSPECIFIED: 0, SMALL: 1, MEDIUM: 2, LARGE: 3, XL: 4 },
  DaemonType: { UNSPECIFIED: 0, MANAGED: 1, EXTERNAL: 2 },
  PortAccessMode: { UNSPECIFIED: 0, PUBLIC: 1, AUTHENTICATED: 2, TOKEN: 3 },
  describeError: (_e: unknown, fallback = "error") => fallback,
  portAccessRulesQueryKey: (id: string) => ["cp", "ports", id],
  listDaemons: mocks.listDaemons,
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
  createDaemonToken: vi.fn(),
  revokeDaemonToken: vi.fn(),
}));

import { EnvironmentsSection } from "@/components/Settings/cloud/environments";

/** A compute subscription whose plan allows exactly these sizes. */
function subscribedTo(allowedDaemonSizes: string[], overageCentsPerMinute = 0) {
  return {
    plan: {
      id: "tier_beta",
      name: "Beta",
      priceCents: 4700n,
      displayOrder: 2,
      structuredLimits: {
        allowedDaemonSizes,
        daemonComputeIncludedMinutes: 2600,
        daemonOveragePerMinuteCents: overageCentsPerMinute,
      },
    },
  };
}

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <EnvironmentsSection />
    </QueryClientProvider>,
  );
}

/**
 * Open the create-machine modal, which is where sizes are picked. Returns the
 * modal so every assertion is scoped INSIDE it — the page renders its own
 * "New Machine" button in two places, and unscoped queries match both.
 */
async function openCreateModal(user: ReturnType<typeof userEvent.setup>) {
  renderSection();
  const openButtons = await screen.findAllByRole("button", {
    name: /new machine/i,
  });
  await user.click(openButtons[0]);
  const modal = await screen.findByRole("dialog");
  return {
    modal,
    sizes: within(modal).getByRole("radiogroup", { name: /size/i }),
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.caps.cloudDaemons = true;
  mocks.listDaemons.mockResolvedValue({ daemons: [] });
  mocks.listDaemonTokens.mockResolvedValue([]);
});

describe("create-machine sizes follow the plan", () => {
  it("offers only the sizes the plan's limits allow", async () => {
    mocks.getComputeSubscription.mockResolvedValue(
      subscribedTo(["small", "medium"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeInTheDocument();
    expect(within(sizes).getByRole("radio", { name: /medium/i })).toBeInTheDocument();
    expect(within(sizes).queryByRole("radio", { name: /large/i })).not.toBeInTheDocument();
    expect(within(sizes).queryByRole("radio", { name: /^xl$/i })).not.toBeInTheDocument();
  });

  /**
   * The rule is the plan's payload, not a tier ladder: a plan that allows
   * `large` but NOT `medium` must offer exactly that, however unusual. A
   * client that treated size as an ordered ladder ("everything up to your
   * tier") would offer medium here and be wrong.
   */
  it("honours a non-contiguous allowed set rather than assuming a ladder", async () => {
    mocks.getComputeSubscription.mockResolvedValue(
      subscribedTo(["small", "large"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeInTheDocument();
    expect(within(sizes).getByRole("radio", { name: /large/i })).toBeInTheDocument();
    expect(within(sizes).queryByRole("radio", { name: /medium/i })).not.toBeInTheDocument();
  });

  /**
   * Fails CLOSED. A plan whose limits never reached the wire must offer
   * nothing and refuse to create, rather than defaulting to a size the
   * server will reject at CreateDaemon time.
   */
  it("offers no sizes and refuses to create when the plan names none", async () => {
    mocks.getComputeSubscription.mockResolvedValue(subscribedTo([]));
    renderSection();

    // With no allowed sizes there is no active plan, so the empty state says
    // to subscribe rather than offering a create button at all.
    expect(
      (await screen.findAllByText(/subscribe to a compute plan/i)).length,
    ).toBeGreaterThan(0);
    expect(
      screen.queryAllByRole("button", { name: /new machine/i }),
    ).toHaveLength(0);
    expect(mocks.createEnvironment).not.toHaveBeenCalled();
  });

  it("creates with the size the user picked", async () => {
    mocks.getComputeSubscription.mockResolvedValue(
      subscribedTo(["small", "medium"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    await user.click(within(sizes).getByRole("radio", { name: /small/i }));
    const modal = sizes.closest('[role="dialog"]') as HTMLElement;
    await user.type(within(modal).getByLabelText(/^name$/i), "box");
    await user.click(
      within(modal).getByRole("button", { name: /^create$/i }),
    );

    await waitFor(() => expect(mocks.createEnvironment).toHaveBeenCalled());
    // DaemonSize.SMALL === 1 in the mocked enum.
    expect(mocks.createEnvironment.mock.calls[0][0]).toMatchObject({ size: 1 });
  });

  /**
   * The default must be inside the allowed set. It used to be a hardcoded
   * MEDIUM, so a small-only plan opened the modal with a size the server
   * would refuse already selected.
   */
  it("defaults to an allowed size, not a hardcoded one", async () => {
    mocks.getComputeSubscription.mockResolvedValue(subscribedTo(["small"]));
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeChecked();
  });
});

describe("no invented per-size price", () => {
  /**
   * These four rates ($0.02 / $0.04 / $0.08 / $0.16 per minute) were a
   * client-side table sitting on the button that spends money — the same
   * defect as the per-plan-id price tables, one step closer to the charge.
   * Nothing on the wire states a per-size rate, so nothing may render one.
   */
  it("shows no hardcoded per-minute rate on the size tiers", async () => {
    mocks.getComputeSubscription.mockResolvedValue(
      subscribedTo(["small", "medium"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    for (const invented of ["$0.02/min", "$0.04/min", "$0.08/min", "$0.16/min"]) {
      expect(within(sizes).queryByText(invented)).not.toBeInTheDocument();
    }
  });

  it("shows the plan's own overage rate, which the server does state", async () => {
    mocks.getComputeSubscription.mockResolvedValue(
      subscribedTo(["small", "medium"], 0.47),
    );
    const user = userEvent.setup();
    const { modal } = await openCreateModal(user);

    // $0.0047/min — a value no hardcoded tier table ever held.
    expect(within(modal).getByText(/\$0\.005\/min/)).toBeInTheDocument();
  });
});
