/**
 * MobileAISettingsScreen hosts `MobileAIProvidersPanel`, the mobile-native
 * rebuild that replaced this screen's previous wholesale import of desktop
 * `CombinedGeneralSettings`. Pins that the static Claude/Codex OAuth notice
 * always renders (it's not gated on a live probe — see the module comment)
 * and that provider fetching wires through to the panel.
 */

import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getProviders: vi.fn(),
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  provisionManagedKey: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
      getProviders: mocks.getProviders,
      updateProvider: mocks.updateProvider,
      validateProviderAPIKey: mocks.validateProviderAPIKey,
      getPreferences: mocks.getPreferences,
      updatePreferences: mocks.updatePreferences,
    },
  },
}));

vi.mock("../../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: mocks.refetchModels }) },
  useModels: () => ({ models: [], loading: false, error: null }),
}));

vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: { setState: vi.fn() },
  resetApiKeySetupDismissed: vi.fn(),
}));

vi.mock("../../../hooks", () => ({
  useCodexOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
  useClaudeOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
  useCopilotOAuth: () => ({
    phase: "idle",
    isActive: false,
    userCode: null,
    verificationUri: null,
    message: null,
    lastResult: null,
    start: vi.fn(),
    cancel: vi.fn(),
    reset: vi.fn(),
  }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}));

vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({ eligible: false, reason: null, isLoading: false }),
}));

vi.mock("../../../services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: mocks.provisionManagedKey },
}));

const { MobileAISettingsScreen } = await import("../MobileAISettingsScreen");

beforeEach(() => {
  mocks.getProviders.mockReset();
  mocks.getProviders.mockResolvedValue([]);
  mocks.getPreferences.mockReset();
  mocks.getPreferences.mockResolvedValue({});
});

describe("MobileAISettingsScreen", () => {
  it("always shows the desktop-only Claude/Codex OAuth notice", async () => {
    render(<MobileAISettingsScreen onBack={vi.fn()} />);
    const notice = await screen.findByText(/claude and codex sign-in needs/i);
    expect(notice).toBeInTheDocument();
    // Not gated on live availability — Copilot is named as the on-device path.
    expect(notice.textContent).toMatch(/github copilot/i);
  });

  it("fetches provider statuses on mount", async () => {
    render(<MobileAISettingsScreen onBack={vi.fn()} />);
    await screen.findByText(/claude and codex sign-in needs/i);
    expect(mocks.getProviders).toHaveBeenCalled();
  });

  it("offers an Add a provider card once statuses load", async () => {
    render(<MobileAISettingsScreen onBack={vi.fn()} />);
    expect(await screen.findByText("Add a provider")).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobileAISettingsScreen onBack={onBack} />);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
