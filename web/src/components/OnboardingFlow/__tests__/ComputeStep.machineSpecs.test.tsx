/**
 * What a machine size actually GIVES you, on the screen that asks you to pick
 * one.
 *
 * ── The report ────────────────────────────────────────────────────────
 *
 * > "compute sizes on the page should describe what they're getting in terms
 * > of cpu/mem"
 *
 * The tiles said "Small", "Medium", "Large", "XL" and a price. Those are names
 * for machines, not descriptions of them, so the only way to choose was to
 * guess — and the consequence of guessing low is an OOM-killed build after
 * you have paid, which is the failure this screen is positioned to prevent.
 *
 * ── Why the numbers are a constant, and what pins them ────────────────
 *
 * They are not on the wire. `ListPlans` returns `PlanLimits`, which carries
 * allowed sizes, included minutes and an overage rate — no CPU, no memory. The
 * message that has cpu_request/memory_limit is `ResourceRequirements`, and it
 * describes a machine that has already been provisioned. Nothing to read
 * before purchase, so `machineSpecs.ts` mirrors control-plane's
 * `daemonSizeResources`.
 *
 * A mirror needs a tripwire, and this is not it — a unit test in this repo
 * cannot see control-plane's Go. What it CAN do is pin the mirror's contract:
 * every offered size renders a spec, the figures are the reserved ones, and
 * the burst claim is stated exactly once. If someone edits the ladder in
 * control-plane and not here, the guard is
 * TestDaemonSizeResourcesLadder over there plus the source-of-truth comment in
 * machineSpecs.ts pointing at it. That asymmetry is worth naming rather than
 * pretending otherwise.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import type { LaunchPlan } from "../types";

const mockUseDaemonStatus = vi.fn(() => ({
  daemons: [] as DaemonInfo[],
  activeDaemon: undefined as DaemonInfo | undefined,
  loading: false,
  refresh: vi.fn(),
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));

const mockUseCloudEligibility = vi.fn(() => ({
  eligible: false,
  reason: null as string | null,
  isLoading: false,
  grantedMinutesRemaining: 0,
  refetch: vi.fn(),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => mockUseCloudEligibility(),
  computeIneligibleCopy: () => "Redeem a coupon code or choose a plan.",
}));

/**
 * All four sizes the client can label, so the ladder is exercised end to end
 * rather than at its cheapest rung. Each plan allows every size at or below
 * its own — which is what makes `smallestPlanAllowingSize` pick one plan per
 * size, the way production's catalog does.
 */
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_small",
          name: "Small",
          displayOrder: 1,
          priceCents: 2000n,
          structuredLimits: {
            allowedDaemonSizes: ["small"],
            daemonComputeIncludedMinutes: 1020,
            daemonOveragePerMinuteCents: 2,
          },
        },
        {
          id: "plan_compute_medium",
          name: "Medium",
          displayOrder: 2,
          priceCents: 4000n,
          structuredLimits: {
            allowedDaemonSizes: ["small", "medium"],
            daemonComputeIncludedMinutes: 2520,
            daemonOveragePerMinuteCents: 2,
          },
        },
        {
          id: "plan_compute_large",
          name: "Large",
          displayOrder: 3,
          priceCents: 8000n,
          structuredLimits: {
            allowedDaemonSizes: ["small", "medium", "large"],
            daemonComputeIncludedMinutes: 2520,
            daemonOveragePerMinuteCents: 2,
          },
        },
        {
          id: "plan_compute_xl",
          name: "XL",
          displayOrder: 4,
          priceCents: 16000n,
          structuredLimits: {
            allowedDaemonSizes: ["small", "medium", "large", "xl"],
            daemonComputeIncludedMinutes: 2520,
            daemonOveragePerMinuteCents: 2,
          },
        },
      ],
    },
  }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/components/Projects/SelfHostedDaemonConnect", () => ({
  SelfHostedDaemonConnect: () => <div data-testid="self-hosted-connect" />,
}));

vi.mock("@/hooks/useBundledDaemonPending", () => ({
  useBundledDaemonPending: () => false,
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: {
    cloudDaemons: true,
    managedCredits: true,
    gitConnections: true,
  },
}));

vi.mock("@/services/controlPlane/reliantAI", () => ({
  RedeemedCouponKind: { COMPUTE_MINUTES: 1, WALLET_CREDIT: 2 },
}));

import { ComputeStep } from "../steps/ComputeStep";
import { MACHINE_SPECS } from "../machineSpecs";

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function renderStep(updatePlan = vi.fn(async () => {})) {
  return render(
    <ComputeStep
      plan={{} as Partial<LaunchPlan>}
      updatePlan={updatePlan}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper: wrapper() },
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockUseDaemonStatus.mockReturnValue({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  });
  mockUseCloudEligibility.mockReturnValue({
    eligible: false,
    reason: null,
    isLoading: false,
    grantedMinutesRemaining: 0,
    refetch: vi.fn(),
  });
});

describe("ComputeStep — every size says what it gives you", () => {
  // THE REPORT. A size name with a price and no specification.
  it.each([
    ["Small", "2 GB RAM", "0.5 CPU"],
    ["Medium", "4 GB RAM", "1 CPU"],
    ["Large", "8 GB RAM", "2 CPU"],
    ["XL", "16 GB RAM", "4 CPU"],
  ])("labels %s with its memory and CPU", (size, memory, cpu) => {
    renderStep();

    const tile = screen.getByRole("button", { name: new RegExp(`^${size} `) });
    expect(tile).toHaveTextContent(memory);
    expect(tile).toHaveTextContent(cpu);
  });

  /**
   * The RESERVED figures, not the burst ceiling.
   *
   * Getting this backwards is the expensive direction: a tile promising "8 GB"
   * on a machine that reserves 2 and merely bursts to 8 would be selling a
   * guarantee we do not make, and the user finds out when a build is OOM
   * killed. Small reserves 2Gi and bursts to 4Gi, so "4 GB RAM" on the Small
   * tile is precisely the wrong answer and worth pinning against.
   */
  it("shows what a machine reserves, never what it can burst to", () => {
    renderStep();

    const small = screen.getByRole("button", { name: /^Small / });
    expect(small).toHaveTextContent("2 GB RAM");
    expect(small).not.toHaveTextContent("4 GB RAM");
  });

  /**
   * Burst is real and it is a selling point — 0.5 CPU undersells a machine
   * that compiles on two full cores — but it holds at every tier, so it
   * belongs under the list once rather than in four rows of it.
   *
   * CPU and memory are named separately because they burst by DIFFERENT
   * multiples: 4x and 2x. This assertion originally required a single shared
   * "4×", trusting control-plane's own comment ("Limits are deliberately 4x
   * requests"), and it failed — the memory column is 2Gi/4Gi, 4Gi/8Gi and so
   * on the whole way up. One number here would overstate memory headroom by
   * double, on the axis a build actually dies on.
   */
  it("states both burst multiples exactly once, below the sizes", () => {
    renderStep();

    const note = screen.getByTestId("compute-step-burst-note");
    expect(note).toHaveTextContent(/4× the CPU/);
    expect(note).toHaveTextContent(/2× the memory/);
    expect(note).toHaveTextContent(/reserve/i);

    for (const size of ["Small", "Medium", "Large", "XL"]) {
      const tile = screen.getByRole("button", {
        name: new RegExp(`^${size} `),
      });
      expect(tile).not.toHaveTextContent(/burst/i);
    }
  });

  /**
   * The coverage note is prose and has to read like it.
   *
   * `coveredSizeLabel` used to be the tile's `label`, which now carries the
   * specs — so reading it from there would produce "covers the Small — 2 GB
   * RAM · 0.5 CPU machine". It reads the `size` instead and formats a bare
   * name. This is the one place the two representations must NOT be the same
   * string.
   */
  it("keeps the coupon coverage note free of spec text", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 6000,
      refetch: vi.fn(),
    });
    renderStep();

    const note = screen.getByTestId("compute-step-coverage-note");
    expect(note).toHaveTextContent(/covers the Small machine/i);
    expect(note).not.toHaveTextContent(/GB RAM/);
  });

  /**
   * The free option answers the same question, honestly.
   *
   * It cannot promise a figure — the machine is the user's and we do not
   * provision it — but a blank spec column beside four filled ones reads as an
   * option with nothing to offer, which is the demotion the owner asked to
   * remove. Naming the trade-off is both truthful and more useful than
   * silence.
   */
  it("describes the free option's compute in the user's own terms", () => {
    renderStep();

    const local = screen.getByRole("button", {
      name: /use your own computer/i,
    });
    expect(local).toHaveTextContent(/CPU and memory it already has/i);
    // Still on the same price axis as the paid rows — the property the
    // one-list layout exists to preserve.
    expect(within(local).getByText(/free/i)).toBeInTheDocument();
  });

  /**
   * A size is a machine SHAPE. It is not an hours allowance.
   *
   * ── The report ────────────────────────────────────────────────────────
   *
   * > "they're still saying the wrong number of hours. the size is purely the
   * > cpu/mem shape. remove the hours note" — and, decisively: "they all get
   * > 160 hours / month".
   *
   * The rows read "17 hours included each month" / "42 hours" / "83 hours" /
   * "Unlimited hours included". Every one of those was wrong, and the shape of
   * the error is why the per-row line cannot come back. control-plane grants
   * `daemon_compute_included_minutes: 9600` — 160 h — at small, medium, large
   * and xl alike (internal/plansconfig/plans.yaml). The figures on screen came
   * from a dev database still seeded with migration 00054's superseded
   * per-plan values, so the UI rendered stale data faithfully and stated a
   * product claim nobody had made: that a bigger machine buys more time.
   *
   * A quantity that does not vary per row must not be PRINTED per row. Stated
   * four times it implies four allowances, and it turns any lag between config
   * and a database into user-visible pricing copy. The fixture above keeps
   * that hazard alive on purpose — its plans carry 1020 and 2520 minutes, so
   * a reintroduced per-row line would render "17 hours" beside "42 hours"
   * again and fail here.
   */
  it("does not put an hours figure on any machine row", () => {
    renderStep();

    for (const size of ["Small", "Medium", "Large", "XL"]) {
      const row = screen.getByRole("button", { name: new RegExp(`^${size} `) });
      expect(row).not.toHaveTextContent(/hours/i);
      expect(row).not.toHaveTextContent(/included/i);
    }
  });

  /**
   * The allowance is still SAID — once, and as a property of every machine.
   *
   * Deleting the per-row line must not delete the fact. It moves beside the
   * burst ceiling, which is stated once for exactly the same reason, and it is
   * still read from the catalog rather than hardcoded so it tracks 9600 if
   * control-plane changes it.
   */
  it("states the included hours once, for every size", () => {
    renderStep();

    const note = screen.getByTestId("compute-step-hours-note");
    // 1020 min = 17 h, from the fixture's own catalog — not a constant.
    expect(note).toHaveTextContent(/17 machine hours/i);
    expect(note).toHaveTextContent(/every size/i);
    expect(screen.getAllByTestId("compute-step-hours-note")).toHaveLength(1);
  });

  /**
   * The free option is a ROW IN the machine list, under the one heading.
   *
   * Two earlier attempts moved it nearer the paid options — to the top of the
   * step, then inside the bordered card — and neither made it one of them,
   * because the thing separating it was never the border. It was the heading:
   * "Choose your machine" titled the hosted tiles alone, so the list of
   * machines was by construction the list the free option was not in.
   *
   * That makes this a structural assertion, not a styling one. The free row
   * and the priced rows must be SIBLINGS in one list that the heading
   * introduces. Asserted by walking from the heading to the element it labels
   * and requiring both rows inside it, so restyling is free but re-dividing
   * the list is not.
   */
  it("makes the free option a row in the machine list, under the heading", () => {
    renderStep();

    const heading = screen.getByText(/choose your machine/i);
    const list = heading.nextElementSibling as HTMLElement | null;
    expect(list).not.toBeNull();

    const rows = within(list as HTMLElement);
    const local = rows.getByRole("button", { name: /use your own computer/i });
    const small = rows.getByRole("button", { name: /^Small / });

    // Siblings in the same list — not one nested inside a block that holds
    // the other, which is what every "move it closer" version produced.
    expect(local.parentElement).toBe(small.parentElement);
    // Free leads.
    expect(
      local.compareDocumentPosition(small) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  /**
   * There is exactly ONE heading over the machines.
   *
   * The hosted rows used to carry their own ("In the Cloud"). A second
   * heading over the rest of the list re-draws the division this layout
   * removes, however the boxes are nested — so its absence is part of the
   * requirement rather than a side effect of deleting the card.
   */
  it("does not head the hosted rows separately", () => {
    renderStep();

    expect(screen.queryByText(/in the cloud/i)).toBeNull();
    expect(screen.queryByText(/^or use a machine we host$/i)).toBeNull();
  });

  /**
   * One question, one selection.
   *
   * `selectedPlanId` falls back to the first plan so committing cloud always
   * carries a size. In a list where the free row and the priced rows sit
   * together, that default paints Small as selected beside an equally
   * selected free row — the user sees two chosen machines and no way to tell
   * which one they get.
   */
  it("shows no machine selected while the user's own computer is chosen", () => {
    renderStep();

    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );

    expect(
      screen.getByRole("button", { name: /use your own computer/i }),
    ).toHaveAttribute("aria-pressed", "true");
    for (const size of ["Small", "Medium", "Large", "XL"]) {
      expect(
        screen.getByRole("button", { name: new RegExp(`^${size} `) }),
      ).toHaveAttribute("aria-pressed", "false");
    }
  });

  /**
   * Picking the free option must visibly DO something — and what it does now
   * happens at the BOTTOM of the step.
   *
   * This assertion previously required the opposite: the instructions above
   * the hosted option, because bottom-anchoring them had been observed to make
   * the click look inert, with the whole cloud card in between. The owner
   * asked for the bottom, and the reason it holds this time is the change in
   * the same commit — the cloud CTA is hidden while the free option is
   * selected, so what separates the row from its instructions is a short list
   * and two lines of prose rather than a card and a primary button.
   *
   * So the ordering is no longer the thing worth pinning; the two facts that
   * make the bottom position safe are. They are asserted directly below and in
   * the CTA test that follows.
   */
  it("opens the connect instructions when the free option is chosen", () => {
    renderStep();

    // fireEvent, not a bare .click(): the latter dispatches outside React's
    // act() scope, so `showLocal` never re-renders and the instructions this
    // test is looking for are legitimately absent.
    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );

    const connect = screen.getByTestId("self-hosted-connect");
    expect(connect).toBeInTheDocument();

    // Last thing on the step: after the machine list, and outside the box
    // that holds the options — it is the consequence of choosing, not one of
    // the choices.
    const heading = screen.getByText(/choose your machine/i);
    const list = heading.nextElementSibling as HTMLElement;
    expect(
      list.compareDocumentPosition(connect) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(list.contains(connect)).toBe(false);
  });

  /**
   * The cloud CTA must be GONE while the user's own computer is selected.
   *
   * This is what buys back the distance the bottom-anchored panel costs, and
   * on its own it fixes a worse problem: "Use a Reliant machine" was a primary
   * button sitting under a visibly selected free row, offering to undo the
   * selection the user had just made. It was also the only primary button on
   * the step, so it read as the way forward — and clicking it discarded the
   * free choice and committed to a paid machine.
   */
  it("hides the cloud CTA while the free option is selected", () => {
    renderStep();

    expect(
      screen.getByRole("button", { name: /use a reliant machine/i }),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );

    expect(
      screen.queryByRole("button", { name: /use a reliant machine/i }),
    ).toBeNull();
  });

  /**
   * ...and it must come BACK when a hosted machine is picked instead.
   *
   * Hiding on `showLocal` is only correct if selecting a size clears it.
   * Otherwise the free option's instructions stay open under a deselected row
   * with no way to commit the hosted choice, which is a dead end reachable in
   * two clicks.
   */
  it("restores the cloud CTA when a hosted machine is picked", () => {
    renderStep();

    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^Medium / }));

    expect(
      screen.getByRole("button", { name: /use a reliant machine/i }),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("self-hosted-connect")).toBeNull();
  });

  /**
   * The coupon field goes too, and for a DIFFERENT reason than the CTA.
   *
   * The CTA had to go because it contradicted the selection and destroyed it
   * on click. This field does neither — redeeming leaves `showLocal` alone,
   * so the choice survives. It is hidden on relevance: a compute coupon buys
   * machine time the user has just declined.
   *
   * That weaker justification is exactly why this needs a test. The field
   * accepts WALLET_CREDIT codes as well as compute codes, so hiding it is
   * only safe while the local path still reaches a coupon field later, on the
   * model step. If that ever stops being true, this hiding becomes a trap and
   * the reasoning above is where to start.
   */
  it("hides the coupon field while the free option is selected", () => {
    renderStep();

    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );

    expect(screen.queryByTestId("redeem-coupon")).toBeNull();
  });

  it("restores the coupon field when a hosted machine is picked", () => {
    renderStep();

    fireEvent.click(
      screen.getByRole("button", { name: /use your own computer/i }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^Medium / }));

    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
  });
});

describe("machineSpecs mirrors control-plane's ladder", () => {
  /**
   * The multiples are uniform DOWN each column and different ACROSS them:
   * CPU 4x at every tier, memory 2x at every tier.
   *
   * The uniformity is what lets the burst claim be one sentence instead of
   * four. The difference between the columns is what stops that sentence
   * being one number — and it is the thing to be suspicious of, because
   * control-plane's own comment asserts 4x for both while its numbers say
   * otherwise.
   *
   * If control-plane ever makes a column non-uniform and this mirror follows,
   * MACHINE_BURST goes undefined and the sentence disappears rather than
   * lying. This test says that was a designed property, not an accident.
   */
  it("keeps each axis uniform, at its own multiple", () => {
    for (const [size, spec] of Object.entries(MACHINE_SPECS)) {
      expect(spec.cpuBurst / spec.cpuReserved, `${size} cpu`).toBe(4);
      expect(spec.memoryBurstGb / spec.memoryReservedGb, `${size} memory`).toBe(
        2,
      );
    }
  });
});
