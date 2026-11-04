/**
 * MobileAISettingsScreen hosts `MobileAIProvidersPanel`. Pins that the static
 * browser sign-in notice always renders (it's not gated on a live probe — see
 * the panel's module comment) and that the header's back button works.
 */

import { screen } from "@testing-library/react";
import { renderWithQuery as render } from "../../../test/renderWithQuery";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getProviders: vi.fn(),
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  provisionManagedKey: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
      getProviders: mocks.getProviders,
      updateProvider: mocks.updateProvider,
      validateProviderAPIKey: mocks.validateProviderAPIKey,
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
  useAntigravityOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
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
const { setTransport } = await import("../../../lib/connect");
const { mockTransport } = await import("../../../lib/test-utils");

beforeEach(() => {
  setTransport(
    mockTransport({
      "SettingsService.GetPreferences": () => ({}),
      "SettingsService.GetProviderStatuses": () => ({ providers: [] }),
      "ProviderAccountService.ListProviderAccounts": () => ({ accounts: [] }),
    }),
  );
});

describe("MobileAISettingsScreen", () => {
  it("always shows the desktop-only browser sign-in notice", async () => {
    render(<MobileAISettingsScreen onBack={vi.fn()} />);
    const notice = await screen.findByText(/claude, chatgpt and google sign-in needs/i);
    expect(notice).toBeInTheDocument();
    // Not gated on live availability — Copilot is named as the on-device path.
    expect(notice.textContent).toMatch(/github copilot/i);
  });

  it("offers Add provider", async () => {
    render(<MobileAISettingsScreen onBack={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Add provider" })).toBeInTheDocument();
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
