/**
 * MobileAIProvidersPanel — the mobile-only parts around the shared providers
 * surface (ProvidersSection, tested in Settings/providers): the browser
 * sign-in notice, the streaming preference written through the same
 * useUpdatePreferences desktop uses, MobileModelPreferences, and the 44px /
 * semantic-token source guards.
 */

import { screen, waitFor } from "@testing-library/react";
import { renderWithQuery as render } from "../../../test/renderWithQuery";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  deleteProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  provisionManagedKey: vi.fn(),
  claudeStart: vi.fn(),
  codexStart: vi.fn(),
  antigravityStart: vi.fn(),
  copilotStart: vi.fn(),
  copilotReset: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
      updateProvider: mocks.updateProvider,
      deleteProvider: mocks.deleteProvider,
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
  useCodexOAuth: () => ({ start: mocks.codexStart, cancel: vi.fn() }),
  useClaudeOAuth: () => ({ start: mocks.claudeStart, cancel: vi.fn() }),
  useAntigravityOAuth: () => ({ start: mocks.antigravityStart, cancel: vi.fn() }),
  useCopilotOAuth: () => ({
    phase: "idle",
    isActive: false,
    userCode: null,
    verificationUri: null,
    message: null,
    lastResult: null,
    start: mocks.copilotStart,
    cancel: vi.fn(),
    reset: mocks.copilotReset,
  }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}));

vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({ eligible: false, reason: null, isLoading: false }),
}));

vi.mock("../../../services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: mocks.provisionManagedKey },
}));

vi.mock("../../../lib/events", () => ({
  getEventBus: () => ({ emit: vi.fn() }),
}));

const { MobileAIProvidersPanel } = await import("../MobileAIProvidersPanel");
const { setTransport } = await import("../../../lib/connect");
const { mockTransport } = await import("../../../lib/test-utils");
const { useAuthStore } = await import("../../../store/authStore");

function renderPanel() {
  return render(<MobileAIProvidersPanel />);
}

beforeEach(() => {
  mocks.refetchModels.mockReset().mockResolvedValue(undefined);
  mocks.updateProvider.mockReset().mockResolvedValue({});
  mocks.deleteProvider.mockReset().mockResolvedValue({});
  mocks.validateProviderAPIKey.mockReset();
  mocks.getPreferences.mockReset().mockResolvedValue({});
  mocks.updatePreferences.mockReset().mockResolvedValue({});
  mocks.provisionManagedKey.mockReset().mockResolvedValue({});
  mocks.claudeStart.mockReset();
  mocks.codexStart.mockReset();
  mocks.copilotStart.mockReset();
  mocks.copilotReset.mockReset();
  // Preferences are SettingsService reads/writes through the generated hooks.
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(
    mockTransport({
      "SettingsService.GetPreferences": () => mocks.getPreferences(),
      "SettingsService.UpdatePreferences": (req) => mocks.updatePreferences(req),
      "SettingsService.GetProviderStatuses": () => ({ providers: [] }),
      "ProviderAccountService.ListProviderAccounts": () => ({ accounts: [] }),
    }),
  );
});

afterEach(() => {
  setTransport(null);
  useAuthStore.setState({ session: null });
});

describe("MobileAIProvidersPanel", () => {
  it("always shows the desktop-only browser sign-in notice", () => {
    renderPanel();
    const notice = screen.getByText(/claude, chatgpt and google sign-in needs/i);
    expect(notice.textContent).toMatch(/github copilot/i);
  });

  it("offers the one Add provider entry point", () => {
    renderPanel();
    expect(screen.getByRole("button", { name: "Add provider" })).toBeInTheDocument();
  });

  it("writes the streaming toggle through the same useUpdatePreferences write desktop uses", async () => {
    mocks.getPreferences.mockResolvedValue({ streamingEnabled: false });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderPanel();

    const toggle = await screen.findByRole("switch", { name: /response streaming/i });
    await user.click(toggle);

    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith(expect.objectContaining({ streamingEnabled: true })),
    );
  });

  it("renders the model tuning section via MobileModelPreferences, not desktop's advanced panel", async () => {
    renderPanel();
    expect(screen.getByText("Default models")).toBeInTheDocument();
    expect(screen.queryByText(/advanced model tuning/i)).not.toBeInTheDocument();
    // MobileModelPreferences loads its tag configs asynchronously; let that
    // settle so this test doesn't leak a state update into the next one.
    await screen.findByText(/add a provider above/i);
  });

  it("sizes interactive targets at 44px, not rem-based h-9/h-10/h-11", () => {
    const source = readFileSync(join(MOBILE_DIR, "MobileAIProvidersPanel.tsx"), "utf8");
    expect(source).not.toMatch(/\bh-(9|10|11)\b/);
  });

  it("uses only semantic Tailwind color tokens, no hardcoded hex values or palette colors", () => {
    const source = readFileSync(join(MOBILE_DIR, "MobileAIProvidersPanel.tsx"), "utf8");
    expect(source).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(source).not.toMatch(/\b(amber|emerald|red|sky|zinc)-\d{2,3}\b/);
  });
});
