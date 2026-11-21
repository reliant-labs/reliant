/**
 * The one connect panel both "Add provider" and a card's "Add another …" open:
 * its heading says what the primary button does, the add-another case warns
 * about the browser re-auth trap before the click, and each redirect provider
 * runs its OWN flow.
 */

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../../test/renderWithQuery";

const mocks = vi.hoisted(() => ({
  claudeStart: vi.fn(),
  codexStart: vi.fn(),
  antigravityStart: vi.fn(),
}));

vi.mock("../../../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn() }) },
}));
vi.mock("../../../../lib/events", () => ({ getEventBus: () => ({ emit: vi.fn() }) }));
vi.mock("../../../../hooks", () => ({
  useClaudeOAuth: () => ({ start: mocks.claudeStart, cancel: vi.fn() }),
  useCodexOAuth: () => ({ start: mocks.codexStart, cancel: vi.fn() }),
  useAntigravityOAuth: () => ({ start: mocks.antigravityStart, cancel: vi.fn() }),
  useCopilotOAuth: () => ({
    phase: "idle", isActive: false, userCode: null, verificationUri: null,
    message: null, lastResult: null, start: vi.fn(), cancel: vi.fn(), reset: vi.fn(),
  }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}));

import { ConnectPanel, connectHeading } from "../ConnectPanel";

const noop = () => {};
const addApiKey = vi.fn();

function renderPanel(provider: string, another = false) {
  const onClose = vi.fn();
  renderWithQuery(
    <ConnectPanel
      provider={provider}
      mode="add"
      another={another}
      variant="desktop"
      addApiKey={addApiKey}
      onConnected={noop}
      onClose={onClose}
    />,
  );
  return { onClose };
}

beforeEach(() => {
  for (const m of Object.values(mocks)) m.mockReset();
});

describe("connectHeading", () => {
  it("names the provider by its brand, and says 'another' only when one is connected", () => {
    expect(connectHeading("claude", "add", false)).toBe("Sign in with Claude");
    expect(connectHeading("claude", "add", true)).toBe("Add another Claude account");
    expect(connectHeading("codex", "add", true)).toBe("Add another ChatGPT account");
    expect(connectHeading("openai", "add", false)).toBe("Add your OpenAI API key");
    expect(connectHeading("openai", "add", true)).toBe("Add another OpenAI key");
  });
});

describe("ConnectPanel", () => {
  it("warns about the browser's signed-in account before an add-another sign-in", () => {
    renderPanel("codex", true);
    expect(screen.getByText(/Still signed in to chatgpt\.com as your other account\?/)).toBeInTheDocument();
  });

  it("says nothing about other accounts on a first sign-in", () => {
    renderPanel("claude");
    expect(screen.queryByText(/your other account/)).not.toBeInTheDocument();
  });

  it("starts Antigravity's OWN flow, asking Google to offer the account picker", async () => {
    mocks.antigravityStart.mockResolvedValue({ ok: false, errorCode: "cancelled", message: "" });
    renderPanel("antigravity", true);
    await userEvent.click(screen.getByRole("button", { name: /login with/i }));
    await waitFor(() => expect(mocks.antigravityStart).toHaveBeenCalledWith({ accountId: undefined, prompt: "select_account" }));
    expect(mocks.codexStart).not.toHaveBeenCalled();
    expect(mocks.claudeStart).not.toHaveBeenCalled();
  });

  it("keeps the panel open with the way out when the browser re-authorized the first account", async () => {
    mocks.claudeStart.mockResolvedValue({ ok: true, message: "", outcome: { created: false, accountLabel: "alice@example.com" } });
    const { onClose } = renderPanel("claude", true);
    await userEvent.click(screen.getByRole("button", { name: /login with/i }));
    expect(await screen.findByRole("status")).toHaveTextContent("That's alice@example.com, already connected.");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("takes a Vertex AI service-account key file and checks its shape before adding it", async () => {
    addApiKey.mockReset();
    renderPanel("vertexai");
    expect(screen.getByText("Add your Vertex AI key")).toBeInTheDocument();
    const field = screen.getByLabelText("Service-account key (JSON)");
    expect(field.tagName).toBe("TEXTAREA");

    await userEvent.click(field);
    await userEvent.paste('{"type": "service_account", "project_id": "acme"');
    await userEvent.click(screen.getByRole("button", { name: /add key/i }));
    expect(await screen.findByTestId("service-account-key-error")).toHaveTextContent(/not valid JSON/);
    expect(addApiKey).not.toHaveBeenCalled();

    const key = JSON.stringify({
      type: "service_account",
      project_id: "acme",
      private_key: "fake-private-key-material",
      client_email: "reliant@acme.iam.gserviceaccount.com",
    });
    addApiKey.mockResolvedValue({ created: true });
    await userEvent.clear(field);
    await userEvent.paste(key);
    expect(screen.queryByTestId("service-account-key-error")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /add key/i }));
    await waitFor(() => expect(addApiKey).toHaveBeenCalledWith("vertexai", key));
  });
});
