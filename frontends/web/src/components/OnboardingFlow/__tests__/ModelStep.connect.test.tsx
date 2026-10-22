/**
 * Connecting a provider in onboarding completes the step exactly once.
 *
 * Provider connect now lands on provider accounts (a sign-in can refresh an account that was
 * already connected, and a pasted key goes through AddProviderAPIKey), but the step's contract is
 * unchanged: one successful connect records the choice (`updatePlan`) and advances (`onNext`) once
 * — never twice, and never when the sign-in did not succeed. Advancing is what the onboarding
 * heal keys on, so a double advance or a missed one would complete onboarding twice or never.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

const mocks = vi.hoisted(() => ({
  claudeStart: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
}));

vi.mock("@/hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => ({
    data: undefined,
    isLoading: false,
    refetch: vi.fn().mockResolvedValue({}),
  }),
  useRedeemCoupon: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({ eligible: true, reason: "", isLoading: false }),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/hooks", () => ({
  useCodexOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useClaudeOAuth: () => ({ reset: vi.fn(), start: mocks.claudeStart, cancel: vi.fn(), isRunning: false }),
  useAntigravityOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useCopilotOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/api/client", () => ({
  api: { settings: { updateProvider: mocks.updateProvider, validateProviderAPIKey: mocks.validateProviderAPIKey } },
}));
vi.mock("@/lib/events", () => ({
  getEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}));
vi.mock("@/store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn().mockResolvedValue(undefined) }) },
}));
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  getIsDev: () => false,
}));

import { ModelStep } from "../steps/ModelStep";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function renderStep() {
  const updatePlan = vi.fn().mockResolvedValue(undefined);
  const onNext = vi.fn();
  render(
    <ModelStep
      plan={{ compute: "cloud_free_trial" } as LaunchPlan}
      updatePlan={updatePlan}
      onNext={onNext}
      onBack={vi.fn()}
    />,
    { wrapper },
  );
  return { updatePlan, onNext };
}

describe("ModelStep connect", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.updateProvider.mockResolvedValue({ message: "API key added" });
    mocks.validateProviderAPIKey.mockResolvedValue({ valid: true, message: "ok" });
  });

  it("a Claude sign-in that added an account advances once", async () => {
    mocks.claudeStart.mockResolvedValue({
      ok: true,
      message: "Connected to Claude",
      outcome: { success: true, message: "Connected to Claude", created: true, accountLabel: "me@example.com" },
    });
    const { updatePlan, onNext } = renderStep();

    fireEvent.click(await screen.findByRole("button", { name: /connect claude subscription/i }));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(updatePlan).toHaveBeenCalledTimes(1);
    expect(mocks.claudeStart).toHaveBeenCalledTimes(1);
  });

  it("signing in as an account that was already connected still advances once", async () => {
    mocks.claudeStart.mockResolvedValue({
      ok: true,
      message: "Claude account already connected; signed in again",
      outcome: { success: true, message: "again", created: false, accountLabel: "me@example.com" },
    });
    const { updatePlan, onNext } = renderStep();

    fireEvent.click(await screen.findByRole("button", { name: /connect claude subscription/i }));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(updatePlan).toHaveBeenCalledTimes(1);
  });

  it("a failed sign-in advances nothing", async () => {
    mocks.claudeStart.mockResolvedValue({ ok: false, errorCode: "token_exchange_failed", message: "Token exchange failed" });
    const { updatePlan, onNext } = renderStep();

    fireEvent.click(await screen.findByRole("button", { name: /connect claude subscription/i }));

    expect(await screen.findByText(/token exchange failed/i)).toBeInTheDocument();
    expect(updatePlan).not.toHaveBeenCalled();
    expect(onNext).not.toHaveBeenCalled();
  });

  it("a pasted key is added as an account and advances once", async () => {
    const { updatePlan, onNext } = renderStep();

    fireEvent.click(screen.getByRole("button", { name: /anthropic api/i }));
    fireEvent.change(document.getElementById("onboarding-llm-key-input")!, { target: { value: "sk-ant-test-key" } });
    fireEvent.click(screen.getByRole("button", { name: /save key and start/i }));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    expect(mocks.updateProvider).toHaveBeenCalledWith("anthropic", "sk-ant-test-key");
    expect(updatePlan).toHaveBeenCalledTimes(1);
  });
});
