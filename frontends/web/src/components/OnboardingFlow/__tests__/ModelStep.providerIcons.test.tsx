import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { ModelStep } from "../steps/ModelStep";
import type { LaunchPlan } from "../types";

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useWalletOverview: () => ({ data: { wallet: { balanceUsdNanos: 0n } }, isLoading: false }),
  useCloudEligibility: () => ({ data: { eligible: false }, isLoading: false }),
}));
vi.mock("@/hooks/useOAuthAvailability", () => ({
  useOAuthAvailability: () => ({ available: true, isLoading: false }),
}));
vi.mock("@/lib/claude-oauth", () => ({ useClaudeOAuth: () => ({}) }));
vi.mock("@/lib/codex-oauth", () => ({ useCodexOAuth: () => ({}) }));
vi.mock("@/lib/copilot-oauth", () => ({ useCopilotOAuth: () => ({ reset: vi.fn() }) }));

function wrapper({ children }: { children: React.ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("ModelStep — provider tiles", () => {
  beforeEach(() => vi.clearAllMocks());

  it("draws a mark on every provider tile", () => {
    const { container } = render(
      <ModelStep
        plan={{ compute: "cloud_free_trial" } as LaunchPlan}
        updatePlan={vi.fn()}
        onNext={vi.fn()}
        onBack={vi.fn()}
      />,
      { wrapper },
    );

    const marks = [...container.querySelectorAll("[data-provider-logo]")].map((el) =>
      el.getAttribute("data-provider-logo"),
    );
    expect(marks).toEqual([
      "reliant",
      "claude",
      "codex",
      "antigravity",
      "copilot",
      "anthropic",
      "openai",
      "openrouter",
    ]);
    expect(marks).not.toContain("fallback");
    // The names stay: the mark is decoration.
    for (const name of ["Reliant", "Claude subscription", "Codex (ChatGPT)", "Antigravity", "GitHub Copilot", "Anthropic API", "OpenAI", "OpenRouter"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });
});
