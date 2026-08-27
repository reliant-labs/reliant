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
  mockUseCurrentUser.mockReturnValue({
    data: { onboardingCompleted: false, createdAtMs: USER_CREATED_MS },
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
  it("does NOT heal when the user's creation time is unknown", async () => {
    mockUseCurrentUser.mockReturnValue({
      data: { onboardingCompleted: false, createdAtMs: undefined },
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
