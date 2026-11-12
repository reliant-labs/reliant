/**
 * The step opens on a provider the user can actually FINISH where they are,
 * and opens on it PASSIVELY.
 *
 * Why context decides the default: a subscription sign-in (Claude, Codex,
 * Antigravity) runs through a helper on the user's own computer. Pre-selecting
 * it for everyone put browser and cloud users on a panel that cannot succeed —
 * every one of the 12 external signups in the first week who reached this step
 * either left or lost minutes to it (one sat 30 minutes). So:
 *
 *  1. With a self-hosted machine connected (or in the desktop app), the
 *     subscription sign-in is the default — it is free and it works.
 *  2. Otherwise the default is Reliant credit (or an API key where the
 *     deployment sells none), and the subscription tiles say what they need.
 *  3. Pre-selecting must write NOTHING. `deriveStep` routes on
 *     `plan.modelProvider`, so writing it on mount would derive the user
 *     straight past the question.
 */

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { ModelStep, defaultProviderFor, SUBSCRIPTION_NEEDS_LOCAL_HINT } from "../steps/ModelStep";
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

const noMachines = () => ({
  daemons: [],
  activeDaemon: undefined,
  loading: false,
  unreachable: false,
  refresh: vi.fn(),
});

const cloudOnly = () => ({
  daemons: [{ status: 1, daemonType: "managed" }],
  activeDaemon: { status: 1, daemonType: "managed" },
  loading: false,
  unreachable: false,
  refresh: vi.fn(),
});

const selfHosted = () => ({
  daemons: [{ status: 1, daemonType: "self_hosted" }],
  activeDaemon: { status: 1, daemonType: "self_hosted" },
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
const reliantCta = () => screen.queryByRole("button", { name: /(start with reliant|continue with \$)/i });

beforeEach(() => {
  vi.clearAllMocks();
  mockCapabilities.managedCredits = true;
  mockDaemonStatus.mockReturnValue(noMachines());
  mockUseWalletOverview.mockReturnValue({
    data: { wallet: { balanceUsdNanos: 0n } },
    isLoading: false,
  });
});

afterEach(() => {
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("defaultProviderFor", () => {
  it("is the subscription sign-in only when a local machine can complete it", () => {
    expect(defaultProviderFor({ localMachineConnected: true, managedCredits: true })).toBe("claude");
    expect(defaultProviderFor({ localMachineConnected: false, managedCredits: true })).toBe("reliant");
  });

  it("falls back to an API key where the deployment sells no credit", () => {
    expect(defaultProviderFor({ localMachineConnected: false, managedCredits: false })).toBe("anthropic");
  });
});

describe("ModelStep — default selection", () => {
  it("a browser user with no machine opens on Reliant credit, not a sign-in that cannot finish", () => {
    renderStep();

    expect(reliantCta()).toBeInTheDocument();
    expect(claudeConnectButton()).toBeNull();
  });

  it("a browser user whose only machine is a cloud one opens on Reliant credit", () => {
    mockDaemonStatus.mockReturnValue(cloudOnly());
    renderStep();

    expect(reliantCta()).toBeInTheDocument();
    expect(claudeConnectButton()).toBeNull();
  });

  it("opens on the Claude subscription when a self-hosted machine is connected", () => {
    mockDaemonStatus.mockReturnValue(selfHosted());
    renderStep();

    expect(claudeConnectButton()).toBeInTheDocument();
  });

  it("opens on the Claude subscription in the desktop app, which bundles the helper", () => {
    (window as unknown as { electronAPI?: unknown }).electronAPI = {};
    renderStep();

    expect(claudeConnectButton()).toBeInTheDocument();
  });

  it("opens on the API-key form where the deployment sells no credit", () => {
    mockCapabilities.managedCredits = false;
    renderStep();

    expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    expect(claudeConnectButton()).toBeNull();
  });

  it("writes nothing on mount — a default selection is not a choice", () => {
    for (const machines of [noMachines(), selfHosted()]) {
      mockDaemonStatus.mockReturnValue(machines);
      const updatePlan = vi.fn();
      const onNext = vi.fn();
      const { unmount } = renderStep({ updatePlan, onNext });

      expect(updatePlan).not.toHaveBeenCalled();
      // The decisive one: derivation routes on modelProvider, so a write here
      // would skip the step entirely rather than default it.
      expect(onNext).not.toHaveBeenCalled();
      unmount();
    }
  });
});

describe("ModelStep — subscription tiles without a local machine", () => {
  it("labels each subscription tile with what it needs, and no API-key or Reliant tile", () => {
    renderStep();

    for (const name of ["Claude subscription", "Codex (ChatGPT)", "Antigravity"]) {
      const tile = screen.getByRole("button", { name });
      expect(tile).toHaveAccessibleDescription(SUBSCRIPTION_NEEDS_LOCAL_HINT);
    }
    for (const name of ["Reliant", "Anthropic API", "OpenAI", "OpenRouter", "GitHub Copilot"]) {
      expect(screen.getByRole("button", { name })).not.toHaveAccessibleDescription(SUBSCRIPTION_NEEDS_LOCAL_HINT);
    }
    expect(SUBSCRIPTION_NEEDS_LOCAL_HINT).toMatch(/desktop app or CLI on your computer/i);
  });

  it("drops the labels once a local machine is connected", () => {
    mockDaemonStatus.mockReturnValue(selfHosted());
    renderStep();

    expect(screen.queryByText(SUBSCRIPTION_NEEDS_LOCAL_HINT)).toBeNull();
  });

  it("the subscription tile is still selectable, so someone who has the CLI is not locked out", () => {
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: "Claude subscription" }));

    expect(claudeConnectButton()).toBeInTheDocument();
  });
});
