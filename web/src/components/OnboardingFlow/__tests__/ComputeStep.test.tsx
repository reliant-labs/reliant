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
 *
 * WHAT MOVED OUT OF THIS FILE: everything that asserted the step PROVISIONS.
 * The step no longer creates, resumes or charges anything — it records the
 * user's choice and advances, and provisioning happens once at the commit
 * point. The guarantee that it never provisions is now pinned by
 * `ComputeStep.noSpeculativeProvisioning.test.tsx`, and the provisioning
 * outcomes themselves (denial, resume, idempotency, ordering) by
 * `commitLaunchPlan.test.ts`.
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
      screen.queryByRole("button", { name: /Use a Reliant machine/i }),
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
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
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
  // ineligible. This card has since been through two answers to that. First
  // "no start button at all when ineligible, with coupon and 'Set up billing'
  // promoted in its place" — which fixed the inert control by replacing it
  // with an exit out of the wizard to /settings/billing.
  //
  // Now there is a checkout step inside the flow, so the answer is simpler and
  // better: the choice is ALWAYS offered, because offering it is the only
  // thing this step does, and an un-entitled choice routes to checkout rather
  // than to a dead end. Entitlement changes what the card says, not what it
  // permits.
  it("offers the cloud choice even when ineligible, and keeps the coupon field", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
    ).toBeEnabled();
    expect(
      screen.queryByText(/No cloud credits available/i),
    ).not.toBeInTheDocument();

    // Coupon redemption survives — a code still skips the checkout step.
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeEnabled();
  });

  // The owner's ask, at this step: onboarding must not leave the flow. Both
  // labels this button ever wore are checked, because the exit was the same
  // navigation under either one.
  it("has no route out to the billing settings page, eligible or not", () => {
    for (const eligible of [false, true]) {
      mockUseCloudEligibility.mockReturnValue({
        eligible,
        reason: eligible ? null : "No compute plan yet",
        isLoading: false,
        grantedMinutesRemaining: 0,
        refetch: mockRefetchCloudEligibility,
      });

      const { unmount } = renderComputeStep({ daemons: [], loading: false });
      expect(
        screen.queryByRole("button", { name: /Set up billing/i }),
        `eligible=${eligible}`,
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: /View plans/i }),
        `eligible=${eligible}`,
      ).toBeNull();
      expect(mockGoToBilling).not.toHaveBeenCalled();
      unmount();
    }
  });

  // Choosing cloud while un-entitled must RECORD the choice, so derivation has
  // something to route to checkout. A silently swallowed click would be the
  // old dead end wearing a different disguise.
  it("records a cloud choice made while ineligible", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "No compute plan yet",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    const updatePlan = vi.fn();
    renderComputeStep({ daemons: [], loading: false, updatePlan });

    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Use a Reliant machine/i }),
      );
    });

    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({ compute: "cloud_paid" }),
    );
    // Recording a choice is not provisioning. The commit point owns that, and
    // it must stay true on the newly-reachable un-entitled path.
    expect(mockCreateDaemon).not.toHaveBeenCalled();
    expect(mockResumeDaemon).not.toHaveBeenCalled();
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

  it("offers the cloud choice when eligible", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
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

  it("redeem failure shows the server's message and does not refetch eligibility", async () => {
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
    // The cloud choice is offered regardless of entitlement now, so its
    // presence says nothing about the redemption. What a failed redemption
    // must NOT do is claim entitlement changed — which is the refetch
    // assertion above, and the reason the old "no start button" assertion here
    // was removed rather than inverted: it would have been testing the card's
    // unconditional rendering, not the redemption's outcome.
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
  // The guarantee is now structural in a stronger sense than "the button is
  // absent". This step does not provision AT ALL — no listDaemons, no
  // CreateDaemon, no ResumeDaemon — so there is no handler to reach, however
  // it is activated. That property is what let the cloud choice be re-enabled
  // for un-entitled users without reopening this bug: clicking it records a
  // plan field and nothing else, and the commit point owns provisioning.
  //
  // Asserting on the MUTATIONS is the point: the original bug was that the
  // HANDLER ran, and an absent-button assertion would now be checking the
  // card's layout rather than that.
  it("clicking around while ineligible never starts provisioning", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "Your free trial has ended",
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    // Click EVERY control on the card, including the cloud choice itself,
    // which is now reachable in this state.
    for (const button of screen.getAllByRole("button")) {
      fireEvent.click(button);
    }
    await act(async () => {
      await Promise.resolve();
    });

    expect(mockCreateDaemon).not.toHaveBeenCalled();
    expect(mockResumeDaemon).not.toHaveBeenCalled();
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
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
    ).toBeEnabled();
    // ...and no "you have no credits" scare copy.
    expect(
      screen.queryByText(/No cloud credits available/i),
    ).not.toBeInTheDocument();
  });

  // The card must offer the same controls whatever the server says about
  // entitlement, including for a reason string this build has no copy for. The
  // billing block used to be gated on `reason` being non-empty, so an
  // unrecognised reason hid the only escape route on the screen — a bug that
  // is now unreachable, because there is no escape route to hide and the
  // choice itself is never withheld.
  it("offers the same controls for an unrecognised ineligibility reason", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: null, // server sent a reason this build has no copy for
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeInTheDocument();
  });

  // The user's report, verbatim: "I signed in with a brand new user... why
  // doesn't it show pricing".
  //
  // The answer moved: pricing is on the checkout step now, which a new cloud
  // user reaches immediately after this one, rather than behind a "View plans"
  // link that navigated out of the wizard. What this step still owes an
  // already-entitled user is the coupon field — someone with a running plan
  // may still be holding a code, and hiding the field until they run out means
  // redeeming requires first spending down.
  it("keeps the coupon affordance for an eligible user", () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 0,
      refetch: mockRefetchCloudEligibility,
    });

    renderComputeStep({ daemons: [], loading: false });

    expect(
      screen.getByRole("button", { name: /Use a Reliant machine/i }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: /Have a coupon code\?/i }),
    ).toBeInTheDocument();
  });
});
