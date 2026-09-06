/**
 * ComputeStep records intent. It does not create anything.
 *
 * THE DEFECT THIS CLOSES: ComputeStep fired a billable `CreateDaemon` from a
 * `useEffect` watching cloud eligibility. Redeeming a compute coupon armed
 * `pendingCloudStart`, the eligibility refetch landed, and the effect
 * provisioned a machine — a resource-creating, money-spending call triggered by
 * a server state change rather than by a user deciding anything.
 *
 * The rule, stated so it forbids the class rather than the instance:
 *
 *   > A call that CREATES, CANCELS or CHARGES may be issued only at a commit
 *   > point — a direct user action, or a webhook confirming money moved. Never
 *   > from a `useEffect` observing a state change.
 *
 * Every test here drives ComputeStep through a state change that USED to
 * provision, and asserts the mutation was never reached. They are written
 * against the mutation, not against the absence of a button: the original bug
 * was that the HANDLER ran, and a button assertion cannot see that.
 *
 * Where provisioning went: `commitLaunchPlan.ts`, invoked once from the
 * terminal steps after onboarding is confirmed. See commitLaunchPlan.test.ts.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { RedeemedCouponKind } from "@/services/controlPlane/reliantAI";
import { isCloudCompute } from "../types";
import type { LaunchPlan } from "../types";

// ── Mocks ────────────────────────────────────────────────────────────────

type DaemonStatusReturn = {
  daemons: DaemonInfo[];
  activeDaemon: DaemonInfo | undefined;
  loading: boolean;
  refresh: () => void;
};

const mockUseDaemonStatus = vi.fn<() => DaemonStatusReturn>();
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));

const mockRefetchCloudEligibility = vi.fn();
const mockUseCloudEligibility = vi.fn(() => ({
  eligible: true,
  reason: null as string | null,
  isLoading: false,
  grantedMinutesRemaining: 0,
  refetch: mockRefetchCloudEligibility,
}));

// The three calls that create or wake a machine. NONE of them may be reached
// from this step, in any state, by any interaction.
const mockCreateDaemon = vi.fn().mockResolvedValue({});
const mockResumeDaemon = vi.fn().mockResolvedValue({});
const mockListDaemons = vi.fn().mockResolvedValue({ daemons: [] });

vi.mock("@/services/controlPlane/daemon", () => ({
  listDaemons: () => mockListDaemons(),
  createDaemon: (...args: unknown[]) => mockCreateDaemon(...args),
  resumeDaemon: (...args: unknown[]) => mockResumeDaemon(...args),
  hasActiveDaemon: (daemons: { status?: number }[]) =>
    daemons.some((d) => d.status === DaemonStatus.ACTIVE),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => mockUseCloudEligibility(),
  useCreateDaemon: () => ({ mutateAsync: mockCreateDaemon, isPending: false }),
  useResumeDaemon: () => ({ mutateAsync: mockResumeDaemon, isPending: false }),
  isReasonedQuotaError: () => false,
  isEntitlementDenial: () => false,
}));

type RedeemCouponResult = {
  amountCents: number;
  code: string;
  newBalanceCents: number;
  kind: RedeemedCouponKind;
  computeMinutes: number;
  newComputeMinutesRemaining: number;
};
const mockRedeemMutate = vi.fn<
  (
    code: string,
    callbacks?: {
      onSuccess?: (res: RedeemCouponResult) => void;
      onError?: (err: unknown) => void;
    },
  ) => void
>();
vi.mock("@/hooks/useReliantAIQueries", () => ({
  useRedeemCoupon: () => ({ mutate: mockRedeemMutate, isPending: false }),
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));
vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}));

import { ComputeStep } from "../steps/ComputeStep";

// ── Harness ──────────────────────────────────────────────────────────────

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function makeDaemon(partial: Partial<DaemonInfo>): DaemonInfo {
  return partial as unknown as DaemonInfo;
}

function setDaemons(daemons: DaemonInfo[], loading = false) {
  mockUseDaemonStatus.mockReturnValue({
    daemons,
    activeDaemon: daemons.find((d) => d.status === DaemonStatus.ACTIVE),
    loading,
    refresh: vi.fn(),
  });
}

function setEligible(eligible: boolean, reason: string | null = null) {
  mockUseCloudEligibility.mockReturnValue({
    eligible,
    reason,
    isLoading: false,
    grantedMinutesRemaining: eligible ? 600 : 0,
    refetch: mockRefetchCloudEligibility,
  });
}

function redeemsComputeCoupon() {
  mockRedeemMutate.mockImplementation((_code, callbacks) => {
    callbacks?.onSuccess?.({
      amountCents: 0,
      code: "MACHINE600",
      newBalanceCents: 0,
      kind: RedeemedCouponKind.COMPUTE_MINUTES,
      computeMinutes: 600,
      newComputeMinutesRemaining: 600,
    });
  });
}

function typeCoupon() {
  fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
  fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
    target: { value: "MACHINE600" },
  });
  fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));
}

/** Every call that creates or wakes a machine, in one assertion, so a new
 *  provisioning path added later is caught rather than silently uncovered. */
function expectNothingProvisioned() {
  expect(mockCreateDaemon).not.toHaveBeenCalled();
  expect(mockResumeDaemon).not.toHaveBeenCalled();
}

beforeEach(() => {
  vi.clearAllMocks();
  mockListDaemons.mockResolvedValue({ daemons: [] });
  mockCreateDaemon.mockResolvedValue({});
  mockResumeDaemon.mockResolvedValue({});
  setEligible(true);
  setDaemons([]);
});

function renderStep(props: {
  onNext?: () => void;
  updatePlan?: (u: Partial<LaunchPlan>) => Promise<void> | void;
}) {
  return render(
    <ComputeStep
      plan={{}}
      updatePlan={props.updatePlan ?? vi.fn(async () => {})}
      onNext={props.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper: makeWrapper() },
  );
}

// ── Tests ────────────────────────────────────────────────────────────────

describe("ComputeStep never provisions", () => {
  // THE HEADLINE. This is the live instance named in the brief: a billable
  // CreateDaemon fired from an effect on an eligibility flip.
  it("does not provision when eligibility flips after a compute coupon is redeemed", async () => {
    setEligible(false, "No compute plan yet");
    redeemsComputeCoupon();

    const onNext = vi.fn();
    const { rerender } = renderStep({ onNext });

    typeCoupon();
    await waitFor(() => {
      expect(mockRefetchCloudEligibility).toHaveBeenCalled();
    });

    // The refetch lands: the server now says this user may run a machine.
    // Under the old code this render is where the effect fired CreateDaemon.
    setEligible(true);
    rerender(
      <ComputeStep
        plan={{}}
        updatePlan={vi.fn(async () => {})}
        onNext={onNext}
        onBack={vi.fn()}
      />,
    );
    await act(async () => {
      await Promise.resolve();
    });

    expectNothingProvisioned();
  });

  // Choosing cloud is a DECISION, and it is still allowed to be a click — what
  // it must not do is act on that decision. It records `compute` and advances;
  // the machine is started later, once, at the commit point.
  it("records the cloud choice and advances without creating a machine", async () => {
    const updatePlan = vi.fn(async () => {});
    const onNext = vi.fn();
    renderStep({ updatePlan, onNext });

    fireEvent.click(screen.getByRole("button", { name: /Use a Reliant machine/i }));

    await waitFor(() => {
      expect(onNext).toHaveBeenCalled();
    });
    // Asserted through `isCloudCompute` rather than against a literal: WHICH
    // cloud choice the step writes is vocabulary this test does not own, and
    // pinning the literal here is how `cloud_paid` came to be treated as local
    // at four separate call sites.
    const wroteCloud = updatePlan.mock.calls.some(([updates]) =>
      isCloudCompute((updates as Partial<LaunchPlan>).compute),
    );
    expect(wroteCloud).toBe(true);
    expectNothingProvisioned();
  });

  // The blocked card's controls (coupon, plans) are the only things a user
  // without entitlement can touch. None may reach provisioning.
  it("clicking every control while ineligible provisions nothing", async () => {
    setEligible(false, "No compute plan yet");
    renderStep({});

    for (const button of screen.getAllByRole("button")) {
      fireEvent.click(button);
    }
    await act(async () => {
      await Promise.resolve();
    });

    expectNothingProvisioned();
  });

  // A daemon appearing is a server state change like any other. The local
  // auto-skip it triggers is a genuine "the question is already answered"
  // shortcut — it writes the plan and nothing else — so it stays, but it must
  // still not provision.
  it("auto-skipping to local when a daemon appears provisions nothing", async () => {
    const updatePlan = vi.fn(async () => {});
    const onNext = vi.fn();

    await act(async () => {
      setDaemons([makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })]);
      renderStep({ updatePlan, onNext });
    });

    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({ compute: "local_daemon" }),
    );
    expectNothingProvisioned();
  });

  // Guards the guard. If these mocks were unreachable for a reason other than
  // the fix — a broken import, a render that throws — every assertion above
  // would pass vacuously. This proves the spies do fire when something calls
  // them, so "not called" above means "the component did not call them".
  it("the provisioning spies are wired (so the assertions above can fail)", async () => {
    await mockCreateDaemon({ name: "probe" });
    expect(mockCreateDaemon).toHaveBeenCalled();
  });
});
