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
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
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
      const tile = screen.getByRole("button", { name: new RegExp(`^${size} `) });
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

    const local = screen.getByRole("button", { name: /use your own computer/i });
    expect(local).toHaveTextContent(/CPU and memory it already has/i);
    // Still on the same price axis as the paid rows — the property the
    // one-list layout exists to preserve.
    expect(within(local).getByText(/free/i)).toBeInTheDocument();
  });

  /**
   * Picking the free option must visibly DO something.
   *
   * The connect instructions used to render at the bottom of the step, which
   * was adjacent while the free option was itself last. Moving the option to
   * the top left the two separated by the entire cloud card — observed in the
   * browser: clicking "Use your own computer" looked inert, because what it
   * revealed was most of a screen further down. Adjacency is what makes the
   * click legible, so it is asserted rather than left to layout.
   */
  it("opens the connect instructions next to the option that opens them", () => {
    renderStep();

    // fireEvent, not a bare .click(): the latter dispatches outside React's
    // act() scope, so `showLocal` never re-renders and the instructions this
    // test is looking for are legitimately absent.
    fireEvent.click(screen.getByRole("button", { name: /use your own computer/i }));

    const connect = screen.getByTestId("self-hosted-connect");
    const cloudCard = screen
      .getByRole("button", { name: /use a reliant machine/i })
      .closest("div.rounded-xl");

    expect(connect).toBeInTheDocument();
    // Before the cloud card, not after it — the whole point of the move.
    expect(
      connect.compareDocumentPosition(cloudCard!) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
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
      expect(
        spec.memoryBurstGb / spec.memoryReservedGb,
        `${size} memory`,
      ).toBe(2);
    }
  });
});
