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
  getComputeEligibility: vi.fn(async () => ({}) as unknown),
  listDaemonTokens: vi.fn(async () => []),
  createEnvironment: vi.fn(async () => ({})),
  navigate: vi.fn(),
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

import { MachinesSection } from "@/components/Settings/cloud/machines";

/**
 * Server eligibility for a SUBSCRIBED caller whose plan allows these sizes.
 *
 * Sizes come off the eligibility response, not off the subscription. The
 * subscription is still fetched, but only for the overage rate it states —
 * see `withOverageRate`.
 */
function subscribedTo(allowedDaemonSizes: string[]) {
  return {
    eligible: true,
    reason: 0,
    hasActiveSubscription: true,
    grantedMinutesRemaining: 0,
    planName: "Beta",
    allowedDaemonSizes,
  };
}

/**
 * Server eligibility for a COUPON-funded caller: granted minutes, no
 * subscription, and the free plan's size list. This is the shape the machines
 * page used to be unable to see at all.
 */
function couponFundedWith(
  grantedMinutesRemaining: number,
  allowedDaemonSizes = ["small"],
) {
  return {
    eligible: true,
    reason: 0,
    hasActiveSubscription: false,
    grantedMinutesRemaining,
    planName: "",
    allowedDaemonSizes,
  };
}

/** No funding at all — no subscription, no grant. */
function notFunded() {
  return {
    eligible: false,
    reason: 2, // NO_SUBSCRIPTION
    hasActiveSubscription: false,
    grantedMinutesRemaining: 0,
    planName: "",
    allowedDaemonSizes: ["small"],
  };
}

/** The subscription payload, which exists here only to state an overage rate. */
function withOverageRate(overageCentsPerMinute: number) {
  return {
    plan: {
      id: "tier_beta",
      name: "Beta",
      priceCents: 4700n,
      displayOrder: 2,
      structuredLimits: {
        allowedDaemonSizes: ["small", "medium"],
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
      <MachinesSection />
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
  // The subscription is no longer the gate, so it defaults to "none" and each
  // test that cares about the overage rate opts in explicitly.
  mocks.getComputeSubscription.mockResolvedValue(null);
});

describe("create-machine sizes follow the server's answer", () => {
  it("offers only the sizes the server says are allowed", async () => {
    mocks.getComputeEligibility.mockResolvedValue(
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
    mocks.getComputeEligibility.mockResolvedValue(
      subscribedTo(["small", "large"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeInTheDocument();
    expect(within(sizes).getByRole("radio", { name: /large/i })).toBeInTheDocument();
    expect(within(sizes).queryByRole("radio", { name: /medium/i })).not.toBeInTheDocument();
  });

  /**
   * Fails CLOSED. An eligible caller the server names no runnable size for
   * must offer nothing and refuse to create, rather than defaulting to a size
   * CreateDaemon will reject.
   */
  it("offers no sizes and refuses to create when the server names none", async () => {
    mocks.getComputeEligibility.mockResolvedValue(subscribedTo([]));
    renderSection();

    expect(
      screen.queryAllByRole("button", { name: /new machine/i }),
    ).toHaveLength(0);
    expect(mocks.createEnvironment).not.toHaveBeenCalled();
  });

  it("creates with the size the user picked", async () => {
    mocks.getComputeEligibility.mockResolvedValue(
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
    // DaemonSize.DAEMON_SIZE_SMALL === 1 in the mocked enum.
    expect(mocks.createEnvironment.mock.calls[0][0]).toMatchObject({ size: 1 });
  });

  /**
   * The default must be inside the allowed set. It used to be a hardcoded
   * MEDIUM, so a small-only plan opened the modal with a size the server
   * would refuse already selected.
   */
  it("defaults to an allowed size, not a hardcoded one", async () => {
    mocks.getComputeEligibility.mockResolvedValue(subscribedTo(["small"]));
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
    mocks.getComputeEligibility.mockResolvedValue(
      subscribedTo(["small", "medium"]),
    );
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    for (const invented of ["$0.02/min", "$0.04/min", "$0.08/min", "$0.16/min"]) {
      expect(within(sizes).queryByText(invented)).not.toBeInTheDocument();
    }
  });

  it("shows the plan's own overage rate, which the server does state", async () => {
    mocks.getComputeEligibility.mockResolvedValue(
      subscribedTo(["small", "medium"]),
    );
    mocks.getComputeSubscription.mockResolvedValue(withOverageRate(0.47));
    const user = userEvent.setup();
    const { modal } = await openCreateModal(user);

    // $0.0047/min — a value no hardcoded tier table ever held.
    expect(within(modal).getByText(/\$0\.005\/min/)).toBeInTheDocument();
  });
});

/**
 * THE REGRESSION THIS FILE NOW OWNS.
 *
 * "I added a coupon for a user from this page, but when i go to machines i see
 * 'Subscribe to a compute plan to create machines' with no ability to actually
 * spawn a machine."
 *
 * The gate was `getComputeSubscription()` — a strictly narrower rule than the
 * server's. A redeemed compute coupon grants machine MINUTES and no
 * subscription, so a fully-entitled user was locked out of a machine the
 * server (internal/svcdaemon.checkDaemonSizeAllowed) would have created for
 * them. These tests pin the server's answer as the only gate, and pin that the
 * sizes a coupon buys are the free plan's — a coupon buys TIME, not a bigger
 * machine, so over-promising a medium here would just move the denial to
 * after the user commits.
 */
describe("a coupon with no subscription can create a machine", () => {
  it("offers New Machine to a grant-funded caller with no subscription", async () => {
    mocks.getComputeEligibility.mockResolvedValue(couponFundedWith(1200));
    mocks.getComputeSubscription.mockResolvedValue(null);
    renderSection();

    expect(
      (await screen.findAllByRole("button", { name: /new machine/i })).length,
    ).toBeGreaterThan(0);
    expect(
      screen.queryByText(/subscribe to a compute plan to create machines/i),
    ).not.toBeInTheDocument();
  });

  it("offers small only — a coupon buys machine time, not a bigger machine", async () => {
    mocks.getComputeEligibility.mockResolvedValue(couponFundedWith(1200));
    mocks.getComputeSubscription.mockResolvedValue(null);
    const user = userEvent.setup();
    const { sizes } = await openCreateModal(user);

    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeChecked();
    for (const denied of [/medium/i, /large/i, /^xl$/i]) {
      expect(
        within(sizes).queryByRole("radio", { name: denied }),
      ).not.toBeInTheDocument();
    }
  });

  it("creates the machine at small", async () => {
    mocks.getComputeEligibility.mockResolvedValue(couponFundedWith(1200));
    mocks.getComputeSubscription.mockResolvedValue(null);
    const user = userEvent.setup();
    const { modal } = await openCreateModal(user);

    await user.type(within(modal).getByLabelText(/^name$/i), "coupon-box");
    await user.click(within(modal).getByRole("button", { name: /^create$/i }));

    await waitFor(() => expect(mocks.createEnvironment).toHaveBeenCalled());
    // DaemonSize.DAEMON_SIZE_SMALL === 1 in the mocked enum.
    expect(mocks.createEnvironment.mock.calls[0][0]).toMatchObject({ size: 1 });
  });
});

/**
 * The prompt shown to an un-funded user must be a real control that goes
 * somewhere, and must name BOTH remedies.
 *
 * It was a `<Badge variant="neutral">` reading "Subscribe to a compute plan to
 * create machines": a bordered pill that looked like a button, did nothing on
 * click, and told a user holding a coupon code that their only option was to
 * pay. The server's own denial names the coupon first; so does this.
 */
describe("the un-funded prompt is a real control", () => {
  /**
   * BOTH prompts must be real buttons. The header one is the one that was a
   * dead Badge, and the empty state renders a second — asserting on only the
   * first match would let the header regress to a pill while this still
   * passed against the empty state's button.
   */
  it("routes to billing's plans tab when clicked", async () => {
    mocks.getComputeEligibility.mockResolvedValue(notFunded());
    const user = userEvent.setup();
    renderSection();

    const ctas = await screen.findAllByRole("button", {
      name: /redeem a coupon/i,
    });
    expect(ctas).toHaveLength(2);
    await user.click(ctas[0]);

    expect(mocks.navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: "/settings/$section",
        params: { section: "billing" },
        search: expect.objectContaining({ tab: "plans" }),
      }),
    );
  });

  it("names the coupon path, not only subscribing", async () => {
    mocks.getComputeEligibility.mockResolvedValue(notFunded());
    renderSection();

    expect(
      (await screen.findAllByText(/redeem a coupon/i)).length,
    ).toBeGreaterThan(0);
    // The old copy offered subscribing as the sole remedy.
    expect(
      screen.queryByText(/^subscribe to a compute plan to create machines$/i),
    ).not.toBeInTheDocument();
  });
});
