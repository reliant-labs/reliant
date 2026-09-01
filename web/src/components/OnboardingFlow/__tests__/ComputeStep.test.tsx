/**
 * ComputeStep render-time tests.
 *
 * Focus: the loading gate that blocks the radio + Continue button until the
 * first `listDaemons` query has settled. Before this gate existed, a fast
 * user could click "Use my own computer" before the auto-skip useEffect
 * ever read `hasUsableDaemonForOnboarding(daemons)`, leaving them stranded
 * on the local-daemon path even though a cloud daemon was already wired up.
 *
 * We mock `useDaemonStatus` so we can deterministically drive its loading
 * shape — the gate is keyed on `loading` (TanStack's `isLoading`, true ONLY
 * during the initial fetch). Mocking the hook is preferred over mocking the
 * underlying gRPC client because the contract under test IS the hook return
 * shape, not the network layer.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import type { ReactNode } from "react";

import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { RedeemedCouponKind } from "@/services/controlPlane/reliantAI";
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

// NOTE: there is deliberately no getIsDev() mock here any more.
//
// ComputeStep used to short-circuit eligibility to `true` under getIsDev()
// (true by default in vitest via import.meta.env.DEV), and this file mocked it
// to false so the eligibility tests could exercise the real branch. That mock
// was hiding a real bug: in an actual dev build the button was ENABLED for an
// unfunded user and the click proceeded into provisioning, failing later at
// the daemon service's own gate. The tests passed the whole time.
//
// getIsDev() is no longer an input to eligibility, so the mock is gone and
// these tests run the same code path a real build does.

type CloudEligibilityReturn = {
  eligible: boolean;
  reason: string | null;
  isLoading: boolean;
  grantedMinutesRemaining: number;
  refetch: () => void;
};

const mockRefetchCloudEligibility = vi.fn();
const mockUseCloudEligibility = vi.fn<() => CloudEligibilityReturn>(() => ({
  eligible: true,
  reason: null,
  isLoading: false,
  grantedMinutesRemaining: 0,
  refetch: mockRefetchCloudEligibility,
}));

// Stub the onboarding query hooks. The loading gate sits ahead of any of
// these in the render path, but they still mount for the non-loading
// branches of the test, so they need stable, network-free returns.
// mockCreateDaemon is asserted on: an ineligible user clicking "Start cloud
// daemon" must not reach it. That is the regression guard for the dev-bypass
// bug — provisioning used to start for users the server would refuse.
const mockCreateDaemon = vi.fn().mockResolvedValue({});
const mockResumeDaemon = vi.fn().mockResolvedValue({});

// handleCloud dynamically imports this module to decide between
// create / resume / reuse, so it has to be mocked for the eligible path to
// reach the create mutation at all.
const mockListDaemons = vi.fn().mockResolvedValue({ daemons: [] });

vi.mock("@/services/controlPlane/daemon", () => ({
  listDaemons: () => mockListDaemons(),
  // Mirrors the real implementation (`d.status === ACTIVE`). An earlier
  // `daemons.length > 0` stub made a SUSPENDED daemon look active, which sent
  // the resume test down the wrong branch and made it pass for the wrong
  // reason — the class of thing this whole file now exists to catch.
  hasActiveDaemon: (daemons: { status?: number }[]) =>
    daemons.some((d) => d.status === DaemonStatus.ACTIVE),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => mockUseCloudEligibility(),
  useCreateDaemon: () => ({
    mutateAsync: mockCreateDaemon,
    isPending: false,
  }),
  useResumeDaemon: () => ({
    mutateAsync: mockResumeDaemon,
    isPending: false,
  }),
  isReasonedQuotaError: () => false,
  // Must be mocked even though most tests do not exercise it: a module mock
  // REPLACES the module, so any export ComputeStep imports and this factory
  // omits is `undefined` at runtime. Leaving it out made the resume-denial
  // path throw "isEntitlementDenial is not a function" as an unhandled error.
  //
  // Real behavior (keyed on the x-reliant-reason header the server sets on
  // every deliberate denial) rather than a constant, so the resume-denial
  // test exercises the same branch production does.
  isEntitlementDenial: (err: unknown) =>
    err instanceof ConnectError && !!err.metadata.get("x-reliant-reason"),
}));

/** Builds the shape the server really returns for an entitlement denial. */
function deniedError(message: string, code: Code, reason: string): ConnectError {
  const headers = new Headers();
  headers.set("x-reliant-reason", reason);
  return new ConnectError(message, code, headers);
}

// Redeem-coupon mutation, mocked so the coupon UI can be driven
// deterministically without a network layer. `mutate` mimics TanStack's
// callback contract (onSuccess/onError) the component relies on.
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
  useRedeemCoupon: () => ({
    mutate: mockRedeemMutate,
    isPending: false,
  }),
}));

// trackEvent fires on the auto-skip path; stub so the test doesn't depend
// on the analytics module being initialized.
vi.mock("@/lib/analytics", () => ({
  trackEvent: vi.fn(),
}));

// "Set up billing" navigates via TanStack Router, which has no router context
// in this render. Where it SENDS people (including the anonymous-user detour)
// is covered by useGoToBilling's own tests; here we only care that the control
// is offered and does not provision a machine.
const mockGoToBilling = vi.fn();
vi.mock("@/hooks/useGoToBilling", () => ({
  useGoToBilling: () => mockGoToBilling,
}));

// Capabilities flag controls whether the cloud option renders; force it on
// so the form's "Reliant Cloud" CTA is unambiguously present when we expect
// the form to be visible.
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));

// EventBus context needs a provider; the simplest path is to mock the hook
// to return a no-op bus so we don't pull in the real provider's setup.
vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({
    emit: vi.fn(),
    on: vi.fn(() => () => {}),
  }),
}));

// Imported AFTER mocks above so ComputeStep picks them up.
import { ComputeStep } from "../steps/ComputeStep";

// ── Test harness ─────────────────────────────────────────────────────────

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
    },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function makeDaemon(partial: Partial<DaemonInfo>): DaemonInfo {
  return partial as unknown as DaemonInfo;
}

function renderComputeStep(opts: {
  daemons: DaemonInfo[];
  loading: boolean;
  onNext?: () => void;
  updatePlan?: (updates: Partial<LaunchPlan>) => Promise<void> | void;
}) {
  const activeDaemon = opts.daemons.find(
    (d) => d.status === DaemonStatus.ACTIVE,
  );
  mockUseDaemonStatus.mockReturnValue({
    daemons: opts.daemons,
    activeDaemon,
    loading: opts.loading,
    refresh: vi.fn(),
  });

  return render(
    <ComputeStep
      plan={{}}
      updatePlan={opts.updatePlan ?? vi.fn(async () => {})}
      onNext={opts.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper: makeWrapper() },
  );
}

beforeEach(() => {
  // clearAllMocks() also clears mockResolvedValue, so any mock whose RESOLVED
  // VALUE matters has to be re-armed here — otherwise it returns undefined and
  // the handler under test throws on the first await. (This is why these two
  // are set here rather than only at declaration.)
  vi.clearAllMocks();
  mockListDaemons.mockResolvedValue({ daemons: [] });
  mockCreateDaemon.mockResolvedValue({});
  mockResumeDaemon.mockResolvedValue({});
  mockUseCloudEligibility.mockReturnValue({
    eligible: true,
    reason: null,
    isLoading: false,
    grantedMinutesRemaining: 0,
    refetch: mockRefetchCloudEligibility,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("ComputeStep loading gate", () => {
  it("hides the form behind the loading state while daemonLoading is true", () => {
    renderComputeStep({ daemons: [], loading: true });

    // The deterministic loading marker is present.
    expect(screen.getByTestId("compute-step-loading")).toBeInTheDocument();
    expect(screen.getByText(/Checking your setup/i)).toBeInTheDocument();

    // The form's CTAs are NOT in the DOM — there is no path through which
    // a click can race the auto-skip evaluation.
    expect(
      screen.queryByRole("button", { name: /Start my machine/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Use my own computer/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the form once daemonLoading is false and no usable daemon is registered", () => {
    renderComputeStep({ daemons: [], loading: false });

    // Loading marker is gone, form is back.
    expect(screen.queryByTestId("compute-step-loading")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Start my machine/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Use my own computer/i }),
    ).toBeInTheDocument();
  });

  it("auto-skips when daemonLoading is false and a usable (ACTIVE) daemon is present", async () => {
    const onNext = vi.fn();
    const updatePlan = vi.fn(async () => {});

    await act(async () => {
      renderComputeStep({
        daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
        loading: false,
        onNext,
        updatePlan,
      });
    });

    // The auto-skip effect commits the local-daemon plan and advances.
    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({
        compute: "local_daemon",
      }),
    );
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("auto-skips for IDLE daemons too (transitional cloud / local reconnect state)", async () => {
    const onNext = vi.fn();
    const updatePlan = vi.fn(async () => {});

    await act(async () => {
      renderComputeStep({
        daemons: [makeDaemon({ id: "d-idle", status: DaemonStatus.IDLE })],
        loading: false,
        onNext,
        updatePlan,
      });
    });

    expect(onNext).toHaveBeenCalledTimes(1);
    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({ compute: "local_daemon" }),
    );
  });
});

describe("ComputeStep cloud eligibility + coupon redemption", () => {
  // THE USER'S REPORT: "there's this 'start daemon' button, but it will always
  // be grayed out because a user never has billing."
  //
  // It really was ALWAYS grey for a new account: the signup compute auto-grant
  // is gone, so every new user resolves to NO_SUBSCRIPTION and lands here
  // ineligible. The card's most prominent control was one that could never be
  // clicked, with the two that fix it demoted to links underneath.
  //
  // So the contract is now absence, not disabled-ness: when the user cannot
  // start a machine there is no start button on the screen at all, and the
  // coupon and billing actions are the card's primary controls.
  it("omits the start button entirely when ineligible, and promotes coupon + billing instead", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.queryByRole("button", { name: /Start my machine/i }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("No compute plan yet")).toBeInTheDocument();
    expect(
      screen.queryByText(/No cloud credits available/i),
    ).not.toBeInTheDocument();

    // The two controls that can actually change the user's state are present.
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: /Set up billing/i }),
    ).toBeEnabled();
  });

  // The card must never present a control the user cannot use. Absence of the
  // start button is the fix for the reported grey button, so it is worth
  // pinning that NOTHING on the cloud card is disabled in this state — a
  // future disabled affordance here would recreate the same dead end.
  it("leaves no disabled control on the card when ineligible", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    for (const button of screen.getAllByRole("button")) {
      expect(button).toBeEnabled();
    }
  });

  it("enables Start my machine when eligible", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Start my machine/i }),
    ).not.toBeDisabled();
  });

  it("redeeming a compute coupon shows the granted minutes and refetches eligibility so the button enables in place", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
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

    renderComputeStep({ daemons: [], loading: false });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "MACHINE600" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    await waitFor(() => {
      expect(
        screen.getByText(/600 machine minutes \(10 hours\)/),
      ).toBeInTheDocument();
    });
    expect(mockRefetchCloudEligibility).toHaveBeenCalled();
  });

  // Typing a compute code into the cloud card IS the user asking for a cloud
  // machine. Requiring a second click on a button that did not exist a moment
  // ago re-asks a question they just answered, so redemption starts the daemon
  // and advances the step.
  //
  // The mechanism this pins: eligibility is server-owned and false at the
  // instant onSuccess fires, so the redeem callback cannot simply call
  // handleCloud — its own `if (!eligible) return` guard would swallow it. The
  // start must happen on a later render, once the refetch reports eligible.
  it("auto-starts the cloud daemon and advances after a compute coupon is redeemed", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
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

    const onNext = vi.fn();
    const { rerender } = renderComputeStep({
      daemons: [],
      loading: false,
      onNext,
    });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "MACHINE600" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    // Still ineligible as far as the client knows: nothing may be provisioned
    // yet, or the guard is being bypassed rather than waited out.
    await waitFor(() => {
      expect(mockRefetchCloudEligibility).toHaveBeenCalled();
    });
    expect(mockCreateDaemon).not.toHaveBeenCalled();
    expect(onNext).not.toHaveBeenCalled();

    // The refetch lands and the server now says the user may start a machine.
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 600,
      refetch: mockRefetchCloudEligibility,
    });
    rerender(
      <ComputeStep
        plan={{}}
        updatePlan={vi.fn(async () => {})}
        onNext={onNext}
        onBack={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockCreateDaemon).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(onNext).toHaveBeenCalled();
    });
  });

  // THE USER'S REPORT: redeeming a compute coupon "skipped all steps and took
  // me to project picker page".
  //
  // project-picker is the LOCAL branch of the flow (deriveStep sends
  // compute==='local_daemon' there), which is the tell: the cloud commit never
  // happened. In the desktop app a bundled local daemon is usually already
  // registered, so the auto-skip effect is sitting armed on
  // hasUsableDaemon===true. Auto-start made that effect reachable in a state
  // it was never designed for.
  //
  // The auto-skip effect guards on `startingCloud`, which only goes true once
  // handleCloud RUNS. Between the render where eligibility flips and the
  // effect that starts the daemon, startingCloud is still false — so the
  // auto-skip effect can fire FIRST, commit compute:'local_daemon', set
  // hasAdvanced, and derivation jumps straight past model to project-picker.
  it("does not fall through to the local auto-skip when a compute coupon is redeemed with a local daemon present", async () => {
    const localDaemon = makeDaemon({
      daemonId: "local-1",
      status: DaemonStatus.ACTIVE,
    });
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
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

    const updatePlan = vi.fn(async () => {});
    const onNext = vi.fn();

    // No daemon at first render, so the card (and its coupon form) is shown.
    const { rerender } = renderComputeStep({
      daemons: [],
      loading: false,
      onNext,
      updatePlan,
    });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "MACHINE600" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    await waitFor(() => {
      expect(mockRefetchCloudEligibility).toHaveBeenCalled();
    });

    // The eligibility refetch lands AND the bundled local daemon shows up in
    // the same render — the desktop-app case.
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 600,
      refetch: mockRefetchCloudEligibility,
    });
    mockUseDaemonStatus.mockReturnValue({
      daemons: [localDaemon],
      activeDaemon: localDaemon,
      loading: false,
      refresh: vi.fn(),
    });
    rerender(
      <ComputeStep
        plan={{}}
        updatePlan={updatePlan}
        onNext={onNext}
        onBack={vi.fn()}
      />,
    );

    // The redemption asked for a CLOUD machine. Committing local here is the
    // reported bug: it lands the user on project-picker having answered
    // nothing.
    await waitFor(() => {
      expect(mockCreateDaemon).toHaveBeenCalled();
    });
    const committedLocal = updatePlan.mock.calls.some(
      ([updates]) =>
        (updates as Partial<LaunchPlan>).compute === "local_daemon",
    );
    expect(committedLocal).toBe(false);
  });

  // A wallet-credit code funds LLM usage and buys no compute, so the user is
  // exactly as unable to start a machine as before. Auto-starting here would
  // fire a request the server refuses and put an error on screen for a
  // redemption that SUCCEEDED.
  it("does not auto-start after a wallet-credit coupon", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
    mockRedeemMutate.mockImplementation((_code, callbacks) => {
      callbacks?.onSuccess?.({
        amountCents: 2000,
        code: "LAUNCH20",
        newBalanceCents: 2000,
        kind: RedeemedCouponKind.WALLET_CREDIT,
        computeMinutes: 0,
        newComputeMinutesRemaining: 0,
      });
    });

    const onNext = vi.fn();
    const { rerender } = renderComputeStep({
      daemons: [],
      loading: false,
      onNext,
    });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "LAUNCH20" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    await waitFor(() => {
      expect(screen.getByText(/Added \$20\.00 to your balance\./)).toBeInTheDocument();
    });

    // Even if something else makes the user eligible on a later render, this
    // redemption must not be what starts a machine.
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
    rerender(
      <ComputeStep
        plan={{}}
        updatePlan={vi.fn(async () => {})}
        onNext={onNext}
        onBack={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: /Start my machine/i }),
      ).toBeInTheDocument();
    });
    expect(mockCreateDaemon).not.toHaveBeenCalled();
    expect(onNext).not.toHaveBeenCalled();
  });

  // The refetch is async, and in that window the user may pick "I'll connect
  // my own". Provisioning a cloud machine under them then would hijack a
  // decision they have since changed.
  it("does not auto-start if the user switches to local while eligibility is refetching", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
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

    const onNext = vi.fn();
    const { rerender } = renderComputeStep({
      daemons: [],
      loading: false,
      onNext,
    });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "MACHINE600" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    // User changes their mind before the refetch settles.
    fireEvent.click(screen.getByRole("button", { name: /Use my own computer/i }));

    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 600,
      refetch: mockRefetchCloudEligibility,
    });
    rerender(
      <ComputeStep
        plan={{}}
        updatePlan={vi.fn(async () => {})}
        onNext={onNext}
        onBack={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockRefetchCloudEligibility).toHaveBeenCalled();
    });
    expect(mockCreateDaemon).not.toHaveBeenCalled();
  });

  it("redeem failure shows the server's message and no start button appears", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
    mockRedeemMutate.mockImplementation((_code, callbacks) => {
      callbacks?.onError?.(new Error("[coupon_not_found] Unknown coupon code."));
    });

    renderComputeStep({ daemons: [], loading: false });

    fireEvent.click(screen.getByRole("button", { name: /Have a coupon code\?/i }));
    fireEvent.change(screen.getByPlaceholderText(/Enter code/i), {
      target: { value: "BOGUS" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Redeem$/i }));

    await waitFor(() => {
      expect(screen.getByText("Unknown coupon code.")).toBeInTheDocument();
    });
    expect(mockRefetchCloudEligibility).not.toHaveBeenCalled();
    // A failed redemption leaves the user ineligible, so the start button must
    // not have appeared.
    expect(
      screen.queryByRole("button", { name: /Start my machine/i }),
    ).not.toBeInTheDocument();
  });
  // REGRESSION: the dev bypass (`getIsDev() ? true : cloudEligible`) enabled
  // this button for an unfunded user, so the click ran the whole
  // listDaemons → createDaemon sequence and failed at the server's daemon
  // gate — after the UI had committed to provisioning. The user reported it as
  // "start cloud daemon continued when I clicked start without a coupon code".
  //
  // Asserting on the mutation rather than only on `disabled` is the point:
  // `disabled` is a rendering concern, and the bug was that the HANDLER ran.
  //
  // The guarantee is now structural rather than a `disabled` attribute: while
  // the user is ineligible there is no start control in the tree, so there is
  // no element a stale render, a keyboard activation, or devtools can activate
  // to reach provisioning. (`handleCloud` keeps its own `if (!eligible)`
  // guard as defense in depth; it is simply no longer reachable from the UI in
  // this state, which is the stronger position.)
  //
  // Asserting on the MUTATIONS, not just on the absent button, is the point:
  // the original bug was that the HANDLER ran.
  it("clicking around while ineligible never starts provisioning", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "Your free trial has ended",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.queryByRole("button", { name: /Start my machine/i }),
    ).not.toBeInTheDocument();

    // Click every control the blocked card does offer. None of them may
    // provision a machine — the only paths out are the coupon form and
    // billing navigation.
    for (const button of screen.getAllByRole("button")) {
      fireEvent.click(button);
    }
    await act(async () => {
      await Promise.resolve();
    });

    expect(mockCreateDaemon).not.toHaveBeenCalled();
    expect(mockResumeDaemon).not.toHaveBeenCalled();
  });

  // The mirror image: an eligible user must still be able to start one.
  it("clicking Start while eligible provisions a daemon", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 600,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    const start = screen.getByRole("button", { name: /Start my machine/i });
    expect(start).toBeEnabled();
    fireEvent.click(start);

    await waitFor(() => {
      expect(mockCreateDaemon).toHaveBeenCalled();
    });
  });
  // ── The original complaint ──────────────────────────────────────────────
  //
  // "I can still hit Start my machine which CONTINUES the onboarding."
  //
  // The button being disabled was never the whole story. Once the handler runs
  // — and it can, via a stale render or any of the paths below — a server
  // DENIAL still fell through to onNext(), so onboarding advanced as if a
  // machine had been provisioned. The user lands in the app with no daemon.
  //
  // The server denies with PermissionDenied / FailedPrecondition
  // ("no compute subscription — subscribe to a compute plan to create
  // daemons"), NOT ResourceExhausted, so isReasonedQuotaError() does not match
  // it and the upgrade modal never fires either.
  it("does NOT advance onboarding when the server denies daemon creation", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true, // client thinks it is fine; the SERVER disagrees
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
    mockCreateDaemon.mockRejectedValue(
      // Carries x-reliant-reason exactly as daemonDenied() does server-side —
      // that header, not the status code, is what marks a deliberate denial.
      deniedError(
        "no compute subscription — subscribe to a compute plan to create daemons",
        Code.FailedPrecondition,
        "no_compute_subscription",
      ),
    );

    const onNext = vi.fn();
    renderComputeStep({ daemons: [], loading: false, onNext });

    fireEvent.click(screen.getByRole("button", { name: /Start my machine/i }));

    await waitFor(() => {
      expect(mockCreateDaemon).toHaveBeenCalled();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(onNext).not.toHaveBeenCalled();
  });

  // Same guarantee on the RESUME path. That catch block swallowed every error
  // ("Resume failure is non-fatal"), including an entitlement denial, and then
  // fell through to onNext() — so a user with a suspended daemon they are no
  // longer entitled to run also sailed through onboarding.
  it("does NOT advance onboarding when resuming an existing daemon is denied", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });
    // One suspended daemon → the resume branch.
    mockListDaemons.mockResolvedValue({
      daemons: [{ id: "d1", status: DaemonStatus.SUSPENDED }],
    });
    mockResumeDaemon.mockRejectedValue(
      deniedError(
        "your free trial has ended — subscribe to a compute plan",
        Code.PermissionDenied,
        "trial_expired",
      ),
    );

    const onNext = vi.fn();
    renderComputeStep({ daemons: [], loading: false, onNext });

    fireEvent.click(screen.getByRole("button", { name: /Start my machine/i }));

    await waitFor(() => {
      expect(mockResumeDaemon).toHaveBeenCalled();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(onNext).not.toHaveBeenCalled();
  });

  // The happy path must still advance, or the fix above is just a new bug.
  it("DOES advance onboarding when daemon creation succeeds", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    const onNext = vi.fn();
    renderComputeStep({ daemons: [], loading: false, onNext });

    fireEvent.click(screen.getByRole("button", { name: /Start my machine/i }));

    await waitFor(() => {
      expect(onNext).toHaveBeenCalled();
    });
  });
  // The user's report: "why is the CreateDaemon button even clickable just to
  // return an error to the user" — and its twin, a DISABLED button for a user
  // whose CreateDaemon actually succeeds (their real state: an active
  // plan_compute_small subscription, 0 minutes used, but no
  // reliant_user_entitlements row, which the OLD gate read as "no credits").
  //
  // Both are the same defect: the button's enabled state must track what the
  // server will actually do. These pin both directions.
  it("eligible server response => button enabled (never blocks an entitled user)", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true, // the server says this user may run a machine
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Start my machine/i }),
    ).toBeEnabled();
    // ...and no "you have no credits" scare copy.
    expect(
      screen.queryByText(/No cloud credits available/i),
    ).not.toBeInTheDocument();
  });

  // An ineligible user must always get BOTH ways out: the billing link and the
  // coupon field. The billing block used to be gated on `reason` being a
  // non-empty string, so an unrecognised reason hid the only escape route.
  it("ineligible => shows the billing action AND the coupon field, even with no reason text", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: null, // server sent a reason this build has no copy for
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.queryByRole("button", { name: /Start my machine/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Set up billing/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeInTheDocument();
  });
  // The user's report, verbatim: "I signed in with a brand new user... why
  // doesn't it show pricing".
  //
  // A new user lands on the free compute trial, so the server correctly says
  // eligible=true (verified against the live dev stack: eligible=true,
  // hasActiveSubscription=true, planName="Compute Small"). Pricing and the
  // coupon field used to be gated on !eligible, so this — the MOST COMMON
  // state a real new user is in — was the one state that showed neither.
  it("shows View plans and the coupon affordance even when the user IS eligible", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Start my machine/i }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: /View plans/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeInTheDocument();
  });
});
