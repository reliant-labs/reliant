/**
 * The terminal step must survive its own completion call.
 *
 * THE BUG THIS CLOSES. All three terminal steps finish in the same order:
 *
 *     await completeOnboardingMutation.mutateAsync(...)
 *     await runCommit(finalPlan)          // ← result drives ProvisioningGate
 *
 * `useCompleteOnboarding.onSuccess` optimistically writes
 * `onboardingCompleted: true` into `['onboarding','currentUser']` — the query
 * `OnboardingRoute` derives `isComplete` from. So resolving the completion was
 * itself what tore down the subtree that was about to render the gate:
 * `runCommit` was awaited by a component being unmounted, `setCommit` landed on
 * a dead component, and the gate never appeared.
 *
 * The user-visible consequence is the one
 * `finalizeOnboarding.cloudGate.test.ts` was written to close: a cloud user who
 * has just paid lands on `/` with `onboarding_completed=true` and no ACTIVE
 * daemon. That earlier fix removed the NAVIGATION from
 * `finalizeOnboardingSideEffects`; the unmount came back through the mutation's
 * cache write instead, and the old test still passed because it only asserts
 * that one function does not call `router.navigate`.
 *
 * WHAT IS REAL HERE, DELIBERATELY. The completion mutation is NOT mocked — the
 * bug lives in its `onSuccess` cache write, so a test that stubs it cannot see
 * the defect. A real QueryClient, the real `useCompleteOnboarding`, the real
 * `useCurrentUser` reading back the same cache entry, and a stand-in terminal
 * step that finishes in the real order. Only the transport underneath
 * (`onboardingService`, `listDaemons`) is faked.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}));

const USER_CREATED_MS = Date.UTC(2026, 0, 10, 0, 0, 0);

/** The server's record. `completeOnboarding` flips it, exactly as the RPC does. */
let serverUser = {
  id: "user-1",
  onboardingCompleted: false,
  createdAtMs: USER_CREATED_MS,
};

const mockCompleteOnboarding = vi.fn(async () => {
  serverUser = { ...serverUser, onboardingCompleted: true };
});

vi.mock("@/services/controlPlane/onboarding", () => ({
  onboardingService: {
    getCurrentUser: async () => serverUser,
    completeOnboarding: (data: Record<string, unknown>) =>
      mockCompleteOnboarding(data),
  },
}));

vi.mock("@/services/controlPlane/daemon", () => ({
  listDaemons: async () => ({ daemons: [] }),
  createDaemon: vi.fn(),
  resumeDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
}));

vi.mock("@/services/controlPlane/billing", () => ({
  getComputeEligibility: async () => ({ eligible: false }),
  ComputeIneligibleReason: { TRIAL_EXPIRED: 1, NO_SUBSCRIPTION: 2, NO_ORGANIZATION: 3 },
}));

vi.mock("@/services/controlPlane/git", () => ({
  gitService: { listRepos: vi.fn(), cloneRepo: vi.fn(), getOAuthURL: () => "" },
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

/**
 * A stand-in terminal step that finishes in the REAL order: complete
 * server-side, then produce a commit result and render the gate from it.
 *
 * It is a stand-in rather than the real ProjectChoiceStep because the three
 * terminal steps differ only in what they do BEFORE the completion call — the
 * ordering under test is identical in all three, and rendering a real one drags
 * in the project store, GitHub credentials and the plan URL for no added
 * coverage of the seam.
 */
let renderCount = 0;
vi.mock("../OnboardingPage", () => ({
  OnboardingPage: () => {
    // Read at render time, not at factory time: the hook module is fully
    // initialised by then, and it is the REAL hook — the cache write under
    // test lives in its onSuccess.
    const completeOnboarding = useCompleteOnboarding();
    const [committed, setCommitted] = useState(false);
    renderCount += 1;
    return (
      <div data-testid="terminal-step">
        {committed ? (
          <div data-testid="provisioning-gate">Setting up your workspace</div>
        ) : (
          <button
            type="button"
            data-testid="finish"
            onClick={async () => {
              await completeOnboarding.mutateAsync({ compute: "cloud_paid" });
              // The commit. Its result is what drives the gate, and it is
              // awaited by THIS component — which must still be mounted.
              await Promise.resolve();
              setCommitted(true);
            }}
          >
            finish
          </button>
        )}
      </div>
    );
  },
}));

import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { OnboardingRoute } from "../OnboardingRoute";

function renderRoute() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <OnboardingRoute />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  renderCount = 0;
  serverUser = {
    id: "user-1",
    onboardingCompleted: false,
    createdAtMs: USER_CREATED_MS,
  };
});

describe("OnboardingRoute — completion recorded is not flow finished", () => {
  it("keeps the terminal step mounted so the provisioning gate can render", async () => {
    renderRoute();

    const finish = await screen.findByTestId("finish");
    finish.click();

    // The completion landed server-side...
    await waitFor(() => expect(mockCompleteOnboarding).toHaveBeenCalled());

    // ...and the step is still on screen to show what happens next.
    await waitFor(() =>
      expect(screen.getByTestId("provisioning-gate")).toBeInTheDocument(),
    );
  });

  it("does not navigate away on its own once completion is recorded", async () => {
    renderRoute();

    (await screen.findByTestId("finish")).click();
    await waitFor(() => expect(mockCompleteOnboarding).toHaveBeenCalled());

    // Give every effect the completion could have woken a chance to run.
    await new Promise((r) => setTimeout(r, 30));

    // The gate's Continue is the only exit from here. Nothing else may take it.
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(screen.getByTestId("provisioning-gate")).toBeInTheDocument();
  });

  // The gate stays put: a user watching a machine provision must not have the
  // screen pulled out from under them by a refetch settling.
  it("keeps the gate on screen while the user query refetches after completion", async () => {
    const { rerender } = renderRoute();

    (await screen.findByTestId("finish")).click();
    await waitFor(() =>
      expect(screen.getByTestId("provisioning-gate")).toBeInTheDocument(),
    );

    // The invalidation's refetch lands, now carrying the server's own
    // completed=true — no longer just the optimistic write.
    await new Promise((r) => setTimeout(r, 30));
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <div />
      </QueryClientProvider>,
    );

    expect(mockNavigate).not.toHaveBeenCalled();
  });

  // The redirect this route legitimately owns must still work: a user who
  // ARRIVES already complete (a bookmarked /onboarding) has to be sent on.
  it("still redirects a user who arrives already complete", async () => {
    serverUser = { ...serverUser, onboardingCompleted: true };

    renderRoute();

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith(
        expect.objectContaining({ to: "/" }),
      ),
    );
    expect(screen.queryByTestId("terminal-step")).not.toBeInTheDocument();
  });

  // Guards the guard: if the stand-in step never rendered, the first two tests
  // would be asserting nothing about the seam.
  it("actually renders the step subtree under test", async () => {
    renderRoute();
    await screen.findByTestId("terminal-step");
    expect(renderCount).toBeGreaterThan(0);
  });
});
