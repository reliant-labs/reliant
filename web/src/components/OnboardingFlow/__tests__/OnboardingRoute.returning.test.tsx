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

function daemon(status: DaemonStatus): DaemonInfo {
  return { id: "d1", status } as unknown as DaemonInfo;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockUseCurrentUser.mockReturnValue({
    data: { onboardingCompleted: false },
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
});
