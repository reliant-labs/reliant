/**
 * OnboardingRoute — returning users must not be re-onboarded.
 *
 * THE TRAP THIS CLOSES: `onboarding_completed_at` is written ONLY by the final
 * project step. A user who set up a working daemon and then closed the tab is
 * still flagged incomplete, and ModernApp bounces every landing on `/` back to
 * `/onboarding` — forever.
 *
 * That was survivable while every signup was auto-granted compute: the user
 * could click through the flow again. Now that entitlement requires a coupon or
 * a paid plan, such a user is permanently stuck — blocked at step 1, unable to
 * reach the step that marks them complete, unable to reach the app they already
 * have a running machine for.
 *
 * Real evidence from the dev DB: users with 1 daemon and completed=false.
 *
 * THE COUNTER-TRAP: the desktop app's bundled daemon RE-REGISTERS under
 * whoever signs in — the post-sign-in restart hands it the new principal and
 * the control-plane row is reassigned. Verified in dev: daemon 42906ac5,
 * created 2026-07-22, owned by whoever last signed in. So "this user has a
 * usable daemon" is true within a second of ANY sign-in, including a
 * first-ever one, and healing on that alone wrote onboarding_completed_at for
 * users who had never seen a single step.
 *
 * The heal therefore requires a daemon that POSTDATES the user's account: one
 * created before the account existed cannot be the product of that account's
 * onboarding. Fixtures below carry both timestamps because the rule is a
 * comparison between them.
 */
import { render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}));

const mockUseCurrentUser = vi.fn();
const mockUseDaemonList = vi.fn();
const mockCompleteMutate = vi.fn();

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCurrentUser: () => mockUseCurrentUser(),
  useDaemonList: () => mockUseDaemonList(),
  useCompleteOnboarding: () => ({ mutate: mockCompleteMutate }),
}));

// The page itself is irrelevant here; we assert on routing, not rendering.
vi.mock("../OnboardingPage", () => ({
  OnboardingPage: () => <div data-testid="onboarding-page" />,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQueryClient: () => ({ invalidateQueries: vi.fn(), setQueryData: vi.fn() }),
  };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { OnboardingRoute } from "../OnboardingRoute";

/** When the fixture user's account was created. */
const USER_CREATED_MS = Date.UTC(2026, 0, 10, 0, 0, 0);

/**
 * A daemon, created AFTER the user by default — i.e. one this user could
 * plausibly have set up during onboarding.
 */
function daemon(status: DaemonStatus, createdAtMs = USER_CREATED_MS + 60_000): DaemonInfo {
  return {
    id: "d1",
    status,
    createdAt: { seconds: BigInt(Math.floor(createdAtMs / 1000)) },
  } as unknown as DaemonInfo;
}

beforeEach(() => {
  vi.clearAllMocks();
  // The in-progress mark is per-user session state; a leak across tests would
  // make one test's mid-flow observation suppress another's genuine heal.
  sessionStorage.clear();
  mockUseCurrentUser.mockReturnValue({
    data: { id: "user-1", onboardingCompleted: false, createdAtMs: USER_CREATED_MS },
    isLoading: false,
  });
  mockUseDaemonList.mockReturnValue({ data: [], isLoading: false });
  // Default: the completion write succeeds.
  mockCompleteMutate.mockImplementation((_vars, opts) => opts?.onSettled?.());
});

describe("OnboardingRoute — returning user", () => {
  it("sends a user with a working daemon into the app instead of onboarding", async () => {
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });

    render(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/", search: {} });
    });
  });

  it("records the completion so the flag is repaired for good", async () => {
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.IDLE)],
      isLoading: false,
    });

    render(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockCompleteMutate).toHaveBeenCalled();
    });
  });

  // A failed write must not strand the user on a screen they cannot leave —
  // they still land in the app, and the next visit retries.
  it("still enters the app when recording completion fails", async () => {
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });
    mockCompleteMutate.mockImplementation((_vars, opts) => opts?.onSettled?.());

    render(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/", search: {} });
    });
  });

  // The genuinely-new user must still be onboarded — this is the guard against
  // "fixing" the trap by letting everyone skip onboarding.
  it("keeps a user with NO daemon in onboarding", async () => {
    mockUseDaemonList.mockReturnValue({ data: [], isLoading: false });

    render(<OnboardingRoute />);

    await new Promise((r) => setTimeout(r, 20));
    expect(mockCompleteMutate).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalledWith({ to: "/", search: {} });
  });

  // Don't act on a half-loaded daemon list — that would bounce a real new user
  // out of onboarding on a slow first fetch.
  it("waits for the daemon list before deciding", async () => {
    mockUseDaemonList.mockReturnValue({ data: undefined, isLoading: true });

    render(<OnboardingRoute />);

    await new Promise((r) => setTimeout(r, 20));
    expect(mockCompleteMutate).not.toHaveBeenCalled();
  });

  // ── The counter-trap: a brand-new user must still be onboarded ──────────
  //
  // This is the bug users actually hit. Sign out, sign in as someone new, and
  // the bundled desktop daemon re-registers under the new principal within a
  // second — so ListDaemons returns an ACTIVE daemon for an account that has
  // never completed a step. Healing on that skipped onboarding entirely.
  it("does NOT heal for a new user whose only daemon predates their account", async () => {
    mockUseDaemonList.mockReturnValue({
      // Created six months before this account existed: the bundled daemon,
      // reassigned at sign-in. It cannot be the product of this user's
      // onboarding.
      data: [daemon(DaemonStatus.ACTIVE, USER_CREATED_MS - 180 * 24 * 3600_000)],
      isLoading: false,
    });

    render(<OnboardingRoute />);

    await new Promise((r) => setTimeout(r, 20));
    expect(mockCompleteMutate).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalledWith({ to: "/", search: {} });
  });

  // Absent evidence, prefer the recoverable outcome. Wrongly healing silently
  // skips onboarding for a new account; wrongly onboarding a returning user
  // costs them a few clicks they can complete.
  // THE BUG: onboarding provisions a daemon MID-FLOW (the compute step starts a
  // cloud machine, and redeeming a compute coupon now does it automatically).
  // That daemon postdates the account, which is exactly the heal's trigger — so
  // the heal fired on a user who was actively onboarding, called
  // CompleteOnboarding, and navigated to "/". Verified in admin-server.log:
  //
  //   17:24:40.809  daemon created     name=onboarding-daemon
  //   17:24:40.848  onboarding completed          (39ms later, no step between)
  //
  // The user asked for the NEXT STEP and got ejected from the flow instead.
  //
  // The heal is for someone ARRIVING with a daemon from a previous session. A
  // daemon that appeared while this page was mounted is this session's work, so
  // it is not evidence of a past completion.
  it("does NOT heal on a daemon that appeared while onboarding was open", async () => {
    // Mount with no daemon: the user is genuinely mid-onboarding.
    mockUseDaemonList.mockReturnValue({ data: [], isLoading: false });
    const { rerender } = render(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockCompleteMutate).not.toHaveBeenCalled();
    });

    // The compute step provisions a machine — it postdates the account, so it
    // satisfies daemonsPostdating.
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });
    rerender(<OnboardingRoute />);

    // Onboarding must continue to its next step, not be completed and exited.
    await waitFor(() => {
      expect(mockNavigate).not.toHaveBeenCalled();
    });
    expect(mockCompleteMutate).not.toHaveBeenCalled();
  });

  // THE RACE that made the mount-latch fix look like it worked and then fail
  // in the browser.
  //
  // useCurrentUser and useDaemonList are INDEPENDENT queries issued in
  // parallel. The daemon list is a fast local call; the user fetch is slower.
  // So the first settled daemon list routinely arrives while currentUser is
  // still undefined — and daemonsPostdating(daemons, undefined) returns [] by
  // design. The latch therefore captured an EMPTY snapshot and froze it.
  //
  // A latch stuck on [] never heals, which is harmless. The damage is the
  // opposite ordering: latch on the pre-daemon list, then the real daemon
  // arrives later and... also never heals. Both orderings looked fine in the
  // Electron test. What the browser hit is that the latch is only correct if
  // it captures a list computed against a KNOWN user — otherwise it is
  // latching a placeholder, and the guard silently stops meaning anything.
  //
  // Pin the invariant directly: the snapshot must not be taken until the user
  // is loaded, or the heal decision is made on data that cannot be right.
  it("does not latch the daemon snapshot until the user has loaded", async () => {
    // User still loading; daemon list already settled WITH a usable daemon.
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: true });
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });

    const { rerender } = render(<OnboardingRoute />);
    await waitFor(() => {
      expect(mockCompleteMutate).not.toHaveBeenCalled();
    });

    // The user resolves. This daemon PREDATES nothing — it postdates the
    // account and was present before onboarding opened, so this is the genuine
    // returning-user case and the heal SHOULD fire.
    mockUseCurrentUser.mockReturnValue({
      data: { id: "user-1", onboardingCompleted: false, createdAtMs: USER_CREATED_MS },
      isLoading: false,
    });
    rerender(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockCompleteMutate).toHaveBeenCalled();
    });
  });

  // THE GITHUB-RETURN BUG. Connecting GitHub is a full page navigation: the
  // app is unloaded, the provider redirects back, and everything remounts. A
  // useRef cannot survive that, so the "what did this user arrive with"
  // snapshot was retaken on the way back — by which time the daemon the flow
  // itself provisioned exists, and the heal fired.
  //
  // Captured end to end in the logs:
  //   04:30:19.263  snapshot taken  {snapshot:false, totalDaemons:0}
  //   04:31:59.863  mounted                              ← OAuth return
  //   04:31:59.878  snapshot taken  {snapshot:true, totalDaemons:1}
  //   04:31:59.883  HEALING — completing onboarding
  //
  // The snapshot must therefore be keyed to the USER and outlive the
  // navigation, not the component instance.
  it("does not heal after a remount caused by the GitHub OAuth round trip", async () => {
    // First mount: mid-onboarding, no daemon yet.
    mockUseDaemonList.mockReturnValue({ data: [], isLoading: false });
    const first = render(<OnboardingRoute />);
    await waitFor(() => {
      expect(mockCompleteMutate).not.toHaveBeenCalled();
    });

    // The user clicks "Connect GitHub". The page navigates away entirely.
    first.unmount();

    // Meanwhile the compute step's daemon has finished provisioning, so it is
    // present when the app reloads on the OAuth return.
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });

    // Back from GitHub: a completely fresh mount.
    render(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockNavigate).not.toHaveBeenCalled();
    });
    expect(mockCompleteMutate).not.toHaveBeenCalled();
  });

  // The daemon list and the user are independent parallel queries, and the
  // list (a fast local call) routinely settles first. Judging a daemon against
  // a not-yet-loaded account dates every daemon out, so the user reads as
  // "arrived with nothing" and gets marked mid-flow — which then suppresses
  // the heal they were entitled to once the account actually loads.
  it("waits for the user before judging the daemon list", async () => {
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: true });
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });

    const { rerender } = render(<OnboardingRoute />);
    await waitFor(() => {
      expect(mockCompleteMutate).not.toHaveBeenCalled();
    });

    // Account resolves. This is a genuine returning user — the daemon predates
    // this page load — so the heal must still fire.
    mockUseCurrentUser.mockReturnValue({
      data: {
        id: "user-1",
        onboardingCompleted: false,
        createdAtMs: USER_CREATED_MS,
      },
      isLoading: false,
    });
    rerender(<OnboardingRoute />);

    await waitFor(() => {
      expect(mockCompleteMutate).toHaveBeenCalled();
    });
  });

  it("does NOT heal when the user's creation time is unknown", async () => {
    mockUseCurrentUser.mockReturnValue({
      data: { id: "user-1", onboardingCompleted: false, createdAtMs: undefined },
      isLoading: false,
    });
    mockUseDaemonList.mockReturnValue({
      data: [daemon(DaemonStatus.ACTIVE)],
      isLoading: false,
    });

    render(<OnboardingRoute />);

    await new Promise((r) => setTimeout(r, 20));
    expect(mockCompleteMutate).not.toHaveBeenCalled();
  });

  // Same reasoning from the daemon's side: a row with no creation time cannot
  // be shown to postdate the account.
  it("does NOT heal on a daemon with no creation time", async () => {
    mockUseDaemonList.mockReturnValue({
      data: [{ id: "d1", status: DaemonStatus.ACTIVE } as unknown as DaemonInfo],
      isLoading: false,
    });

    render(<OnboardingRoute />);

    await new Promise((r) => setTimeout(r, 20));
    expect(mockCompleteMutate).not.toHaveBeenCalled();
  });
});
