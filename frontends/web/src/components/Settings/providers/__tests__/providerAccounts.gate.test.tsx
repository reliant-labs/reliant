/**
 * The feature gate on the two hosts: Settings → Providers (desktop) and the
 * mobile providers panel. With the account RPCs answering, the account list
 * replaces the single-credential list; while they answer Unimplemented (the
 * backend scaffold, P3.2 not landed) today's connect/disconnect view is
 * untouched. A configured provider the accounts RPC does not list keeps its
 * legacy row, so nothing connected disappears.
 */

import { screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../../test/renderWithQuery";
import type { ProviderAccount } from "../../../../api/provider-account-grpc";

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getPreferences: vi.fn(),
  updateProvider: vi.fn(),
}));

vi.mock("../../../../api/provider-account-grpc", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../../api/provider-account-grpc")>();
  return { ...actual, providerAccountGrpc: { listAccounts: mocks.listAccounts } };
});

vi.mock("../../../../api/client", () => ({
  api: {
    settings: {
      getPreferences: mocks.getPreferences,
      updatePreferences: vi.fn(),
      updateProvider: mocks.updateProvider,
      validateProviderAPIKey: vi.fn(),
    },
  },
}));

vi.mock("../../../../lib/settingsPersistence", () => ({
  readSetting: vi.fn().mockResolvedValue({ status: "missing" }),
  upsertStringSetting: vi.fn(),
}));
vi.mock("../../../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn() }) },
  useModels: () => ({ models: [], loading: false, error: null }),
}));
vi.mock("../../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: { setState: vi.fn() },
  resetApiKeySetupDismissed: vi.fn(),
}));
vi.mock("../../../../lib/events", () => ({ getEventBus: () => ({ emit: vi.fn() }) }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../../hooks", () => {
  const flow = () => ({ start: vi.fn(), cancel: vi.fn(), reset: vi.fn() });
  return {
    useClaudeOAuth: flow,
    useCodexOAuth: flow,
    useAntigravityOAuth: flow,
    useCopilotOAuth: () => ({
      phase: "idle", isActive: false, userCode: null, verificationUri: null,
      message: null, lastResult: null, start: vi.fn(), cancel: vi.fn(), reset: vi.fn(),
    }),
    useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
  };
});
vi.mock("../../../../hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({ eligible: false, reason: null, isLoading: false }),
}));
vi.mock("../../../../hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => ({ data: undefined }),
}));
vi.mock("../../../../services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: vi.fn() },
}));
vi.mock("../../../../services/controlPlane/reliantAI", () => ({ reliantAIAvailable: false }));
vi.mock("../../ModelPreferences", () => ({ ModelPreferences: () => null }));
vi.mock("../../ToolPreferences", () => ({ ToolPreferences: () => null }));
vi.mock("../../LocalModelsSection", () => ({ LocalModelsSection: () => null }));
vi.mock("../../../Mobile/MobileModelPreferences", () => ({ MobileModelPreferences: () => null }));

import { useAuthStore } from "../../../../store/authStore";
import { AISettings } from "../../AISettings";
import { MobileAIProvidersPanel } from "../../../Mobile/MobileAIProvidersPanel";

const claudeAccount: ProviderAccount = {
  id: "a1",
  provider: "claude",
  providerDisplayName: "Claude subscription",
  authKind: "oauth",
  label: "alice@example.com",
  email: "alice@example.com",
  priority: 0,
  status: "available",
  windows: [],
  usageSource: "headers",
  createdAt: "2026-10-01T00:00:00Z",
};

const statuses = [
  { provider: "claude", displayName: "Claude subscription", hasApiKey: true, configured: true },
  { provider: "openai", displayName: "OpenAI", hasApiKey: true, configured: true, maskedKey: "sk-…abcd" },
];

beforeEach(() => {
  Object.values(mocks).forEach((m) => m.mockReset());
  mocks.getPreferences.mockResolvedValue({});
  useAuthStore.setState({ session: { access_token: "t" } as never });
});

describe("desktop Settings → Providers", () => {
  it("keeps today's connect/disconnect view while the account RPCs are Unimplemented", async () => {
    mocks.listAccounts.mockRejectedValue(new ConnectError("scaffold", Code.Unimplemented));
    renderWithQuery(<AISettings providers={statuses} />);

    await waitFor(() => expect(mocks.listAccounts).toHaveBeenCalled());
    expect(await screen.findByText("Configured Providers")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect Claude subscription" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete OpenAI" })).toBeInTheDocument();
    expect(screen.queryByTestId("account-list")).not.toBeInTheDocument();
  });

  it("shows the account list once the RPC answers, and keeps rows for providers it does not list", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings providers={statuses} />);

    expect(await screen.findByTestId("account-list")).toBeInTheDocument();
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add another account" })).toBeInTheDocument();
    // The Claude single-credential row is replaced; OpenAI, not in the account list, stays.
    expect(screen.queryByRole("button", { name: "Disconnect Claude subscription" })).not.toBeInTheDocument();
    expect(screen.getByText("Other providers")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete OpenAI" })).toBeInTheDocument();
  });

  it("keeps today's view when the account list is empty", async () => {
    mocks.listAccounts.mockResolvedValue([]);
    renderWithQuery(<AISettings providers={statuses} />);
    await waitFor(() => expect(mocks.listAccounts).toHaveBeenCalled());
    expect(await screen.findByText("Configured Providers")).toBeInTheDocument();
    expect(screen.queryByTestId("account-list")).not.toBeInTheDocument();
  });
});

describe("mobile providers panel", () => {
  it("keeps the Connected rows on Unimplemented", async () => {
    mocks.listAccounts.mockRejectedValue(new ConnectError("scaffold", Code.Unimplemented));
    renderWithQuery(<MobileAIProvidersPanel providers={statuses} onProvidersUpdate={vi.fn()} />);
    await waitFor(() => expect(mocks.listAccounts).toHaveBeenCalled());
    expect(await screen.findByRole("button", { name: "Disconnect Claude subscription" })).toBeInTheDocument();
    expect(screen.queryByText("Other providers")).not.toBeInTheDocument();
    expect(screen.queryByTestId("account-list")).not.toBeInTheDocument();
  });

  it("renders the account list, reorder via the overflow menu, and legacy rows for the rest", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<MobileAIProvidersPanel providers={statuses} onProvidersUpdate={vi.fn()} />);
    expect(await screen.findByTestId("account-list")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Actions for alice@example.com" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Reorder / })).not.toBeInTheDocument();
    expect(screen.getByText("Other providers")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete OpenAI" })).toBeInTheDocument();
  });
});
