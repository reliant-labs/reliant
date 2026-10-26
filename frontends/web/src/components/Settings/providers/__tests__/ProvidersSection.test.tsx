/**
 * Settings → AI, end to end over the account RPCs: the three tabs, the one
 * "Add provider" action in the page header, the picker that lists every
 * provider (connected ones marked, never removed), the scoped "Add another
 * Claude account" path both entry points share, the empty state, the error
 * state, and the mobile panel that renders the same surface.
 *
 * ProviderAccountService answers at the transport, so the generated hooks run
 * for real.
 */

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../../test/renderWithQuery";
import type { ProviderAccount } from "../../../../types/providerAccount";
import { setTransport } from "../../../../lib/connect";
import { mockTransport } from "../../../../lib/test-utils";
import { providerAccountHandlers } from "../../../../test/providerAccountTransport";

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  addApiKey: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  provisionManagedKey: vi.fn(),
  navigate: vi.fn(),
  reliantAvailable: false,
  wallet: undefined as undefined | { wallet: { balanceUsdNanos: bigint } },
}));

vi.mock("../../../../api/client", () => ({
  api: { settings: { validateProviderAPIKey: mocks.validateProviderAPIKey } },
}));
vi.mock("../../../../lib/settingsPersistence", () => ({
  readSetting: vi.fn().mockResolvedValue({ status: "missing" }),
  upsertStringSetting: vi.fn(),
}));
vi.mock("../../../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn() }) },
  useModels: () => ({ models: [], loading: false, error: null }),
}));
vi.mock("../../../../lib/events", () => ({ getEventBus: () => ({ emit: vi.fn() }) }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));
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
vi.mock("../../../../hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => ({ data: mocks.wallet }),
}));
vi.mock("../../../../services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: mocks.provisionManagedKey },
}));
vi.mock("../../../../services/controlPlane/reliantAI", () => ({
  get reliantAIAvailable() {
    return mocks.reliantAvailable;
  },
}));
vi.mock("../../ModelPreferences", () => ({ ModelPreferences: () => <div data-testid="model-preferences" /> }));
vi.mock("../../ToolPreferences", () => ({ ToolPreferences: () => <div data-testid="tool-preferences" /> }));
vi.mock("../../LocalModelsSection", () => ({ LocalModelsSection: () => <div data-testid="local-models" /> }));
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

const reliantAccount: ProviderAccount = {
  ...claudeAccount,
  id: "r1",
  provider: "reliant",
  providerDisplayName: "Reliant",
  authKind: "managed",
  label: "Reliant",
  email: undefined,
};

beforeEach(() => {
  for (const m of [mocks.listAccounts, mocks.addApiKey, mocks.validateProviderAPIKey, mocks.provisionManagedKey, mocks.navigate]) {
    m.mockReset();
  }
  mocks.reliantAvailable = false;
  mocks.wallet = undefined;
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(
    mockTransport({
      ...providerAccountHandlers({ listAccounts: mocks.listAccounts, addApiKey: mocks.addApiKey }),
      "SettingsService.GetPreferences": () => ({}),
      "SettingsService.GetProviderStatuses": () => ({ providers: [] }),
    }),
  );
});

afterEach(() => {
  setTransport(null);
  useAuthStore.setState({ session: null });
});

const picker = () => screen.getByRole("region", { name: "Add a provider" });

describe("Settings → AI", () => {
  it("has three tabs, and Models holds the tier defaults and the tool models", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);

    expect(screen.getByRole("heading", { level: 1, name: "AI" })).toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Providers", "Models", "Local & custom"]);

    await userEvent.click(screen.getByRole("tab", { name: "Models" }));
    expect(screen.getByTestId("model-preferences")).toBeInTheDocument();
    expect(screen.getByTestId("tool-preferences")).toBeInTheDocument();
    // Streaming is a chat preference now; it is not on this page.
    expect(screen.queryByText(/streaming/i)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Local & custom" }));
    expect(screen.getByTestId("local-models")).toBeInTheDocument();
  });

  it("lists connected providers as cards with a scoped 'Add another' in each header", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);

    const card = await screen.findByRole("region", { name: "Claude" });
    expect(within(card).getByRole("heading", { name: "Claude" })).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Add another Claude account" })).toBeInTheDocument();
    // The old form is gone: no select of providers, no "Add New Provider".
    expect(screen.queryByText(/Add New Provider/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: /provider/i })).not.toBeInTheDocument();
  });

  it("the header's Add provider lists every provider by connect method, connected ones marked", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);
    await screen.findByRole("region", { name: "Claude" });

    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    const p = within(picker());
    expect(p.getByRole("heading", { name: "Sign in with a subscription" })).toBeInTheDocument();
    expect(p.getByRole("heading", { name: "API key" })).toBeInTheDocument();
    // No control plane in this deployment, so no Reliant credits group.
    expect(p.queryByRole("heading", { name: "Reliant credits" })).not.toBeInTheDocument();

    const claude = p.getByRole("button", { name: "Claude, connected" });
    expect(claude).toHaveTextContent("Connected · 1");
    for (const name of ["ChatGPT", "Google", "GitHub Copilot", "Anthropic API", "OpenAI API", "Gemini API", "OpenRouter"]) {
      expect(p.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it("picking a connected provider opens the same panel as its card's button", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);
    await screen.findByRole("region", { name: "Claude" });

    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    await userEvent.click(within(picker()).getByRole("button", { name: "Claude, connected" }));

    expect(screen.queryByRole("region", { name: "Add a provider" })).not.toBeInTheDocument();
    const card = screen.getByRole("region", { name: "Claude" });
    const panel = within(card).getByTestId("connect-panel");
    expect(panel).toHaveTextContent("Add another Claude account");
    // The browser re-auth trap is named BEFORE the click, not only after it.
    expect(panel).toHaveTextContent(/Still signed in to claude\.ai as your other account\?.*private window/);
    expect(within(card).getByRole("button", { name: "Add another Claude account" })).toHaveAttribute("aria-expanded", "true");
  });

  it("picking a new key provider connects it in the picker, with Test connection", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    mocks.validateProviderAPIKey.mockResolvedValue({ valid: true, message: "" });
    mocks.addApiKey.mockResolvedValue({ account: null, created: true });
    renderWithQuery(<AISettings />);
    await screen.findByRole("region", { name: "Claude" });

    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    await userEvent.click(within(picker()).getByRole("button", { name: "OpenAI API" }));

    const panel = within(screen.getByTestId("connect-panel"));
    expect(panel.getByText("Add your OpenAI API key")).toBeInTheDocument();
    await userEvent.type(panel.getByLabelText("API key"), "sk-test");
    await userEvent.click(panel.getByRole("button", { name: "Test connection" }));
    expect(mocks.validateProviderAPIKey).toHaveBeenCalledWith("openai", "sk-test");
    expect(await panel.findByText("The key works.")).toBeInTheDocument();

    await userEvent.click(panel.getByRole("button", { name: "Add key" }));
    await waitFor(() => expect(mocks.addApiKey).toHaveBeenCalledWith("openai", "sk-test", undefined));
  });

  it("shows a real empty state with a place to start", async () => {
    mocks.listAccounts.mockResolvedValue([]);
    renderWithQuery(<AISettings />);

    const empty = await screen.findByTestId("providers-empty");
    expect(empty).toHaveTextContent("Connect the AI your agents run on");
    await userEvent.click(within(empty).getByRole("button", { name: "Sign in with Claude" }));
    expect(within(empty).getByTestId("connect-panel")).toHaveTextContent("Sign in with Claude");

    await userEvent.click(within(empty).getByRole("button", { name: "Cancel" }));
    await userEvent.click(within(empty).getByRole("button", { name: "All providers" }));
    expect(picker()).toBeInTheDocument();
  });

  it("says when the providers could not be loaded, with a retry", async () => {
    mocks.listAccounts.mockRejectedValue(new ConnectError("down", Code.Unimplemented));
    renderWithQuery(<AISettings />);
    expect(await screen.findByText("Your providers could not be loaded.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("removes an account only through the in-app confirm", async () => {
    const deleteAccount = vi.fn().mockResolvedValue(undefined);
    setTransport(
      mockTransport({
        ...providerAccountHandlers({ listAccounts: mocks.listAccounts, deleteAccount }),
        "SettingsService.GetProviderStatuses": () => ({ providers: [] }),
      }),
    );
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);
    await screen.findByRole("region", { name: "Claude" });

    await userEvent.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Remove alice@example.com?");
    expect(dialog).toHaveTextContent("Claude stops working until you connect an account again.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(deleteAccount).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(deleteAccount).toHaveBeenCalledWith("a1"));
  });
});

describe("Reliant, once", () => {
  beforeEach(() => {
    mocks.reliantAvailable = true;
  });

  it("offers credits in the picker and enables them in place", async () => {
    mocks.wallet = { wallet: { balanceUsdNanos: 18_400_000_000n } };
    mocks.provisionManagedKey.mockResolvedValue({ synced: true });
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<AISettings />);
    await screen.findByRole("region", { name: "Claude" });

    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    const p = within(picker());
    expect(p.getByRole("heading", { name: "Reliant credits" })).toBeInTheDocument();
    const tile = p.getByRole("button", { name: "Reliant" });
    expect(tile).toHaveTextContent(/\$18\.40 available · Enable/);
    await userEvent.click(tile);
    await waitFor(() => expect(mocks.provisionManagedKey).toHaveBeenCalledTimes(1));
  });

  it("is a provider card whose keys and spend open in Billing", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount, reliantAccount]);
    renderWithQuery(<AISettings />);

    const card = await screen.findByRole("region", { name: "Reliant" });
    expect(within(card).queryByRole("button", { name: /Add another/ })).not.toBeInTheDocument();
    await userEvent.click(within(card).getByRole("button", { name: "Keys & spend" }));
    expect(mocks.navigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
      search: { tab: "ai" },
    });
    // No "Reliant AI" tab on this page any more.
    expect(screen.queryByRole("tab", { name: /Reliant/ })).not.toBeInTheDocument();
  });
});

describe("mobile providers panel", () => {
  it("renders the same cards in the mobile variant, with no drag handle", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<MobileAIProvidersPanel />);
    expect(await screen.findByTestId("account-list")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Actions for alice@example.com" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Reorder / })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add another Claude account" })).toBeInTheDocument();
  });

  it("opens the same picker from its Add provider button", async () => {
    mocks.listAccounts.mockResolvedValue([claudeAccount]);
    renderWithQuery(<MobileAIProvidersPanel />);
    await screen.findByTestId("account-list");
    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    expect(within(picker()).getByRole("button", { name: "Claude, connected" })).toBeInTheDocument();
  });

  it("keeps the browser sign-in notice", () => {
    mocks.listAccounts.mockResolvedValue([]);
    renderWithQuery(<MobileAIProvidersPanel />);
    expect(screen.getByText(/reliant auth serve/)).toBeInTheDocument();
  });
});
