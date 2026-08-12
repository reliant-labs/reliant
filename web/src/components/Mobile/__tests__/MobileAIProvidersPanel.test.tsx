/**
 * MobileAIProvidersPanel — pins that it writes through the exact same
 * `api.settings.*` calls, OAuth hooks, and `onboardingService` desktop
 * `CombinedGeneralSettings` uses (a divergent write path here would be a
 * real bug, not a UI difference — see the module comment), and that no
 * interactive element falls under the 44px touch-target floor.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  provisionManagedKey: vi.fn(),
  claudeStart: vi.fn(),
  codexStart: vi.fn(),
  copilotStart: vi.fn(),
  copilotReset: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
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
  useCodexOAuth: () => ({ start: mocks.codexStart, cancel: vi.fn() }),
  useClaudeOAuth: () => ({ start: mocks.claudeStart, cancel: vi.fn() }),
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

function renderPanel(providers: Array<{
  provider: string;
  configured: boolean;
  hasApiKey: boolean;
  maskedKey?: string;
  displayName: string;
}> = []) {
  const onProvidersUpdate = vi.fn();
  const utils = render(
    <MobileAIProvidersPanel providers={providers} onProvidersUpdate={onProvidersUpdate} />,
  );
  return { ...utils, onProvidersUpdate };
}

beforeEach(() => {
  mocks.refetchModels.mockReset().mockResolvedValue(undefined);
  mocks.updateProvider.mockReset().mockResolvedValue({});
  mocks.validateProviderAPIKey.mockReset();
  mocks.getPreferences.mockReset().mockResolvedValue({});
  mocks.updatePreferences.mockReset().mockResolvedValue({});
  mocks.provisionManagedKey.mockReset().mockResolvedValue({});
  mocks.claudeStart.mockReset();
  mocks.codexStart.mockReset();
  mocks.copilotStart.mockReset();
  mocks.copilotReset.mockReset();
});

describe("MobileAIProvidersPanel", () => {
  it("always shows the desktop-only Claude/Codex OAuth notice", () => {
    renderPanel();
    const notice = screen.getByText(/claude and codex sign-in needs/i);
    expect(notice.textContent).toMatch(/github copilot/i);
  });

  it("lists configured providers with their masked key", () => {
    renderPanel([
      {
        provider: "anthropic",
        configured: true,
        hasApiKey: true,
        maskedKey: "sk-ant-...abcd",
        displayName: "Anthropic",
      },
    ]);
    expect(screen.getByText("Anthropic")).toBeInTheDocument();
    expect(screen.getByText("sk-ant-...abcd")).toBeInTheDocument();
  });

  it("saves a manual API key through the same api.settings.updateProvider call desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    const { onProvidersUpdate } = renderPanel();

    await user.click(screen.getByText("Anthropic"));
    const input = await screen.findByPlaceholderText(/enter your anthropic api key/i);
    await user.type(input, "sk-ant-test-key");
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(mocks.updateProvider).toHaveBeenCalledWith("anthropic", "sk-ant-test-key"),
    );
    await waitFor(() => expect(mocks.refetchModels).toHaveBeenCalled());
    expect(onProvidersUpdate).toHaveBeenCalled();
  });

  it("validates a key through the same api.settings.validateProviderAPIKey call desktop uses", async () => {
    mocks.validateProviderAPIKey.mockResolvedValue({ valid: true, message: "" });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderPanel();

    await user.click(screen.getByText("Anthropic"));
    const input = await screen.findByPlaceholderText(/enter your anthropic api key/i);
    await user.type(input, "sk-ant-test-key");
    await user.click(screen.getByRole("button", { name: /test connection/i }));

    await waitFor(() =>
      expect(mocks.validateProviderAPIKey).toHaveBeenCalledWith(
        "anthropic",
        "sk-ant-test-key",
      ),
    );
    expect(
      await screen.findByText(/connection successful/i),
    ).toBeInTheDocument();
  });

  it("deletes a configured provider through updateProvider with an empty key", async () => {
    vi.stubGlobal("confirm", vi.fn(() => true));
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderPanel([
      {
        provider: "anthropic",
        configured: true,
        hasApiKey: true,
        maskedKey: "sk-ant-...abcd",
        displayName: "Anthropic",
      },
    ]);

    await user.click(screen.getByRole("button", { name: /delete anthropic/i }));
    await waitFor(() => expect(mocks.updateProvider).toHaveBeenCalledWith("anthropic", ""));
    vi.unstubAllGlobals();
  });

  it("starts the Claude OAuth flow through the shared useClaudeOAuth hook", async () => {
    mocks.claudeStart.mockResolvedValue({ ok: true, message: "Connected!" });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderPanel();

    await user.click(screen.getByText("Claude Code"));
    await user.click(await screen.findByRole("button", { name: /login with claude code/i }));

    await waitFor(() => expect(mocks.claudeStart).toHaveBeenCalled());
  });

  it("provisions Reliant through the same onboardingService.provisionManagedKey call desktop uses", async () => {
    // Re-mock cloud eligibility as eligible for this one test.
    vi.doMock("../../../hooks/useOnboardingQueries", () => ({
      useCloudEligibility: () => ({ eligible: true, reason: null, isLoading: false }),
    }));
    vi.resetModules();
    const { MobileAIProvidersPanel: PanelWithReliant } = await import(
      "../MobileAIProvidersPanel"
    );
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    render(<PanelWithReliant providers={[]} onProvidersUpdate={vi.fn()} />);

    await user.click(screen.getByText("Reliant"));
    await waitFor(() => expect(mocks.provisionManagedKey).toHaveBeenCalled());
    vi.doUnmock("../../../hooks/useOnboardingQueries");
  });

  it("writes the streaming toggle through the same api.settings.updatePreferences call desktop uses", async () => {
    mocks.getPreferences.mockResolvedValue({ streaming_enabled: false });
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderPanel();

    const toggle = await screen.findByRole("switch", { name: /response streaming/i });
    await user.click(toggle);

    await waitFor(() =>
      expect(mocks.updatePreferences).toHaveBeenCalledWith({ streaming_enabled: true }),
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

  it("uses only semantic Tailwind color tokens, no hardcoded hex values", () => {
    const source = readFileSync(join(MOBILE_DIR, "MobileAIProvidersPanel.tsx"), "utf8");
    expect(source).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  });
});
