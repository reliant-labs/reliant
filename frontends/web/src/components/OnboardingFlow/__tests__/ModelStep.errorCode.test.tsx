/**
 * A failure on the model step is readable in a masked replay.
 *
 * Replay masks every text node, so the banner's sentence is invisible to a
 * reviewer. The step must put an opaque code on the banner (data-error-code)
 * and on the session timeline (`onboarding_error`), and never the provider's
 * text. Reproduces a 2026-11-12 Windows desktop sign-up: an OpenRouter key on
 * an account with no credits.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

const mocks = vi.hoisted(() => ({
  trackEvent: vi.fn(),
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
  useClaudeOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useAntigravityOAuth: () => ({ reset: vi.fn(), start: vi.fn(), cancel: vi.fn(), isRunning: false }),
  useCopilotOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: mocks.trackEvent }));
vi.mock("@/hooks/provider-account-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/provider-account-queries")>()),
  addProviderApiKey: mocks.updateProvider,
}));
vi.mock("@/hooks/settings-service-hooks_gen", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/settings-service-hooks_gen")>()),
  useValidateProviderAPIKey: () => ({ mutateAsync: mocks.validateProviderAPIKey }),
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

const OPENROUTER_402 =
  'Validation failed: POST "https://openrouter.ai/api/v1/responses": 402 Payment Required ' +
  '{"message":"Insufficient credits. This account never purchased credits.","code":402}';

describe("ModelStep error code", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("an unfunded OpenRouter key shows a no-credit banner tagged with an opaque code", async () => {
    mocks.validateProviderAPIKey.mockResolvedValue({ valid: false, message: OPENROUTER_402 });
    const onNext = vi.fn();
    render(
      <ModelStep
        plan={{ compute: "local_daemon" } as LaunchPlan}
        updatePlan={vi.fn().mockResolvedValue(undefined)}
        onNext={onNext}
        onBack={vi.fn()}
      />,
      { wrapper },
    );

    fireEvent.click(screen.getByRole("button", { name: /openrouter/i }));
    fireEvent.change(document.getElementById("onboarding-llm-key-input")!, {
      target: { value: "sk-or-test-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: /save key and start/i }));

    const banner = (await screen.findByText(/no credit left/i)).closest("[data-error-code]");
    expect(banner).toHaveAttribute("data-error-code", "key_no_credits");
    expect(banner).not.toHaveTextContent("{");

    await waitFor(() =>
      expect(mocks.trackEvent).toHaveBeenCalledWith("onboarding_error", {
        step: "model",
        code: "key_no_credits",
        provider: "openrouter",
      }),
    );
    // Only opaque values leave the browser: never the key, never the provider's body.
    const sent = JSON.stringify(mocks.trackEvent.mock.calls);
    expect(sent).not.toContain("sk-or-test-key");
    expect(sent).not.toContain("Insufficient credits");
    expect(mocks.updateProvider).not.toHaveBeenCalled();
    expect(onNext).not.toHaveBeenCalled();
  });
});
