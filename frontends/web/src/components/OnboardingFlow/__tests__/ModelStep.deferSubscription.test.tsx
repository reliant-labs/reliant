/**
 * A subscription pick no longer dead-ends a user with no machine yet.
 *
 * A cloud user reaches this step before any machine exists (checkout and
 * provisioning come later), and the subscription sign-in runs through a helper
 * on their OWN computer. So choosing one records the choice and continues; the
 * first chat offers to finish it (PendingProviderBanner). Nothing is stored on
 * a machine, so no backend support is involved.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

const mocks = vi.hoisted(() => ({
  setPendingProvider: vi.fn(),
  availability: { available: false, loading: false, recheck: vi.fn(), problem: "no_machine", stopped: true } as Record<string, unknown>,
}));

vi.mock("@/lib/pendingProvider", () => ({ setPendingProvider: (...a: unknown[]) => mocks.setPendingProvider(...a) }));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: [], activeDaemon: undefined, loading: false, unreachable: false, refresh: vi.fn() }),
}));
vi.mock("@/hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => ({ data: undefined, isLoading: false, refetch: vi.fn() }),
  useRedeemCoupon: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({ eligible: true, reason: "", isLoading: false }),
}));
vi.mock("@/hooks", () => ({
  useCodexOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useClaudeOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useAntigravityOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useCopilotOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useOAuthAvailability: () => mocks.availability,
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { managedCredits: true } }));

import { ModelStep } from "../steps/ModelStep";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function renderStep() {
  const updatePlan = vi.fn().mockResolvedValue(undefined);
  const onNext = vi.fn();
  render(
    <ModelStep plan={{ compute: "cloud_free_trial" } as LaunchPlan} updatePlan={updatePlan} onNext={onNext} onBack={vi.fn()} />,
    { wrapper },
  );
  return { updatePlan, onNext };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.setPendingProvider.mockResolvedValue(undefined);
});

describe("ModelStep — choose a subscription now, connect it later", () => {
  it("records the choice and continues, instead of leaving the user on a panel that cannot succeed", async () => {
    const { updatePlan, onNext } = renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));

    fireEvent.click(await screen.findByRole("button", { name: /connect claude subscription later/i }));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(mocks.setPendingProvider).toHaveBeenCalledWith("claude");
    // The plan carries the provider, so step derivation moves on and the
    // commit knows no managed credit is involved.
    expect(updatePlan).toHaveBeenCalledWith(expect.objectContaining({ modelProvider: "anthropic" }));
    expect(updatePlan).not.toHaveBeenCalledWith(expect.objectContaining({ modelProvider: "reliant_credits" }));
  });

  it("remembers WHICH subscription was chosen", async () => {
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Codex (ChatGPT)" }));
    fireEvent.click(await screen.findByRole("button", { name: /connect codex \(chatgpt\) later/i }));

    await waitFor(() => expect(mocks.setPendingProvider).toHaveBeenCalledWith("codex"));
  });

  it("does not continue if the choice could not be saved", async () => {
    mocks.setPendingProvider.mockRejectedValue(new Error("settings unavailable"));
    const { onNext } = renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));
    fireEvent.click(await screen.findByRole("button", { name: /connect claude subscription later/i }));

    expect(await screen.findByText(/settings unavailable/i)).toBeInTheDocument();
    expect(onNext).not.toHaveBeenCalled();
  });

  it("offers 'Use an API key instead' first, and Reliant credit only as a smaller second link", async () => {
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));

    const key = await screen.findByRole("button", { name: /use an api key instead/i });
    const credit = screen.getByRole("button", { name: /or use reliant credit/i });
    expect(key.compareDocumentPosition(credit) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(credit.className).toMatch(/text-xs/);
    // The old escape, which led with credit, is gone.
    expect(screen.queryByRole("button", { name: /^use reliant credit instead$/i })).toBeNull();

    fireEvent.click(key);
    expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
  });

  it("is not offered when the helper IS available — the normal sign-in runs", () => {
    mocks.availability = { available: true, loading: false, recheck: vi.fn(), problem: null, stopped: false };
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));

    expect(screen.queryByRole("button", { name: /later/i })).toBeNull();
    expect(screen.getByRole("button", { name: /connect claude subscription/i })).toBeInTheDocument();
    mocks.availability = { available: false, loading: false, recheck: vi.fn(), problem: "no_machine", stopped: true };
  });
});
