/**
 * The step opens on the user's OWN model, and opens on it PASSIVELY.
 *
 * Product decision (owner, on reliant#2639): bring-your-own comes first and
 * Reliant credit is never preselected.
 *
 *  1. Desktop app, or a self-hosted machine connected: the subscription
 *     sign-in is the default — it is free, and the helper it needs is there.
 *  2. A browser or cloud user with no machine: the Anthropic API-key form is
 *     the default, because it is the one thing they can finish right now.
 *  3. Reliant credit is the LAST tile, quieter than the rest, never selected
 *     by default — but still there.
 *  4. Pre-selecting must write NOTHING. `deriveStep` routes on
 *     `plan.modelProvider`, so writing it on mount would derive the user
 *     straight past the question.
 */

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import {
  ModelStep,
  defaultProviderFor,
  orderedProviderIds,
  SUBSCRIPTION_NEEDS_LOCAL_HINT,
} from "../steps/ModelStep";
import type { LaunchPlan } from "../types";

const mockUseWalletOverview = vi.fn();
const mockDaemonStatus = vi.fn();
const mockCapabilities = vi.hoisted(() => ({ managedCredits: true }));

vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: mockCapabilities }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => mockDaemonStatus() }));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useWalletOverview: () => mockUseWalletOverview(),
  useCloudEligibility: () => ({ data: { eligible: false }, isLoading: false }),
}));
vi.mock("@/hooks/useOAuthAvailability", () => ({
  useOAuthAvailability: () => ({ available: true, isLoading: false }),
}));
vi.mock("@/lib/claude-oauth", () => ({ useClaudeOAuth: () => ({}) }));
vi.mock("@/lib/codex-oauth", () => ({ useCodexOAuth: () => ({}) }));
vi.mock("@/lib/copilot-oauth", () => ({
  useCopilotOAuth: () => ({ reset: vi.fn() }),
}));

function wrapper({ children }: { children: React.ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const machines = (type?: string) => ({
  daemons: type ? [{ status: 1, daemonType: type }] : [],
  activeDaemon: type ? { status: 1, daemonType: type } : undefined,
  loading: false,
  unreachable: false,
  refresh: vi.fn(),
});

function renderStep(props: { updatePlan?: () => void; onNext?: () => void } = {}) {
  return render(
    <ModelStep
      plan={{ compute: "cloud_free_trial" } as LaunchPlan}
      updatePlan={props.updatePlan ?? vi.fn()}
      onNext={props.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper },
  );
}

const claudeConnectButton = () => screen.queryByRole("button", { name: /connect claude subscription/i });
const apiKeyInput = () => screen.queryByPlaceholderText("sk-ant-...");
const reliantCta = () => screen.queryByRole("button", { name: /(start with reliant|continue with \$)/i });
const tileNames = () =>
  screen
    .getAllByRole("button")
    .map((b) => b.getAttribute("aria-label"))
    .filter((n): n is string => !!n && /Reliant credit|Claude subscription|Codex|Antigravity|Copilot|Anthropic API|OpenAI|OpenRouter/.test(n));

beforeEach(() => {
  vi.clearAllMocks();
  mockCapabilities.managedCredits = true;
  mockDaemonStatus.mockReturnValue(machines());
  mockUseWalletOverview.mockReturnValue({
    data: { wallet: { balanceUsdNanos: 0n } },
    isLoading: false,
  });
});

afterEach(() => {
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("defaultProviderFor", () => {
  it("is the subscription sign-in only where a local helper can complete it, else the Anthropic key", () => {
    expect(defaultProviderFor({ localMachineConnected: true })).toBe("claude");
    expect(defaultProviderFor({ localMachineConnected: false })).toBe("anthropic");
  });

  it("is never Reliant credit", () => {
    for (const localMachineConnected of [true, false]) {
      expect(defaultProviderFor({ localMachineConnected })).not.toBe("reliant");
    }
  });
});

describe("orderedProviderIds", () => {
  it("puts Reliant credit last in both contexts", () => {
    expect(orderedProviderIds(true).at(-1)).toBe("reliant");
    expect(orderedProviderIds(false).at(-1)).toBe("reliant");
  });

  it("leads with the API keys (Anthropic first) when no local helper exists", () => {
    expect(orderedProviderIds(false).slice(0, 3)).toEqual(["anthropic", "openai", "openrouter"]);
  });

  it("leads with the subscriptions when a local helper exists", () => {
    expect(orderedProviderIds(true)[0]).toBe("claude");
  });

  it("lists every provider exactly once", () => {
    for (const local of [true, false]) {
      const ids = orderedProviderIds(local);
      expect(new Set(ids).size).toBe(ids.length);
      expect(ids).toHaveLength(8);
    }
  });
});

describe("ModelStep — default selection", () => {
  it("a browser user with no machine opens on the Anthropic API-key form", () => {
    renderStep();

    expect(apiKeyInput()).toBeInTheDocument();
    expect(claudeConnectButton()).toBeNull();
    expect(reliantCta()).toBeNull();
  });

  it("a cloud user whose only machine is a cloud one also opens on the API-key form", () => {
    mockDaemonStatus.mockReturnValue(machines("managed"));
    renderStep();

    expect(apiKeyInput()).toBeInTheDocument();
    expect(reliantCta()).toBeNull();
  });

  it("opens on the Claude subscription when a self-hosted machine is connected", () => {
    mockDaemonStatus.mockReturnValue(machines("self_hosted"));
    renderStep();

    expect(claudeConnectButton()).toBeInTheDocument();
    expect(reliantCta()).toBeNull();
  });

  it("opens on the Claude subscription in the desktop app, which bundles the helper", () => {
    (window as unknown as { electronAPI?: unknown }).electronAPI = {};
    renderStep();

    expect(claudeConnectButton()).toBeInTheDocument();
  });

  it("opens on the API key even where the deployment sells credit; credit is still one click away", () => {
    mockCapabilities.managedCredits = true;
    renderStep();
    expect(reliantCta()).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Reliant credit" }));
    expect(reliantCta()).toBeInTheDocument();
  });

  it("writes nothing on mount — a default selection is not a choice", () => {
    for (const m of [machines(), machines("self_hosted")]) {
      mockDaemonStatus.mockReturnValue(m);
      const updatePlan = vi.fn();
      const onNext = vi.fn();
      const { unmount } = renderStep({ updatePlan, onNext });

      expect(updatePlan).not.toHaveBeenCalled();
      expect(onNext).not.toHaveBeenCalled();
      unmount();
    }
  });
});

describe("ModelStep — order and weight of the tiles", () => {
  it("with no machine: API keys first, subscriptions next, Reliant credit last", () => {
    renderStep();

    expect(tileNames()).toEqual([
      "Anthropic API",
      "OpenAI",
      "OpenRouter",
      "Claude subscription",
      "Codex (ChatGPT)",
      "Antigravity",
      "GitHub Copilot",
      "Reliant credit",
    ]);
  });

  it("with a machine: subscriptions first, Reliant credit still last", () => {
    mockDaemonStatus.mockReturnValue(machines("self_hosted"));
    renderStep();

    const names = tileNames();
    expect(names[0]).toBe("Claude subscription");
    expect(names.at(-1)).toBe("Reliant credit");
  });

  it("Reliant credit is visually quieter while not selected", () => {
    renderStep();
    const credit = screen.getByRole("button", { name: "Reliant credit" });
    const other = screen.getByRole("button", { name: "OpenAI" });

    expect(credit.className).toMatch(/border-dashed/);
    expect(other.className).not.toMatch(/border-dashed/);
  });
});

describe("ModelStep — subscription tiles without a local machine", () => {
  it("labels each subscription tile with what it needs; keys and credit carry no label", () => {
    renderStep();

    for (const name of ["Claude subscription", "Codex (ChatGPT)", "Antigravity"]) {
      expect(screen.getByRole("button", { name })).toHaveAccessibleDescription(SUBSCRIPTION_NEEDS_LOCAL_HINT);
    }
    for (const name of ["Reliant credit", "Anthropic API", "OpenAI", "OpenRouter", "GitHub Copilot"]) {
      expect(screen.getByRole("button", { name })).not.toHaveAccessibleDescription(SUBSCRIPTION_NEEDS_LOCAL_HINT);
    }
    expect(SUBSCRIPTION_NEEDS_LOCAL_HINT).toMatch(/desktop app or CLI on your computer/i);
  });

  it("drops the labels once a local machine is connected", () => {
    mockDaemonStatus.mockReturnValue(machines("self_hosted"));
    renderStep();

    expect(screen.queryByText(SUBSCRIPTION_NEEDS_LOCAL_HINT)).toBeNull();
  });

  it("the subscription tile is still selectable", () => {
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));

    expect(screen.getByText(/authenticate via claude subscription/i)).toBeInTheDocument();
  });
});
