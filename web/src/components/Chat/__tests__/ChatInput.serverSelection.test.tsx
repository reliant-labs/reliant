/**
 * A chat's model, mode and other params live on the server (its ChatSelection),
 * not in this browser. The composer therefore:
 *
 *   - opens on the server's selection, on any device with a fresh local store;
 *   - pushes nothing just for being opened (the baseline is the server's);
 *   - pushes an edit whether or not a run is live, with the version it read;
 *   - adopts a change made elsewhere, or a model the server moved, but keeps an
 *     edit not yet pushed;
 *   - seeds a chat the server has no selection for from what an older build kept
 *     in localStorage, once, and then forgets it.
 */

import { act, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { useProjectStore } from "../../../store/projectStore";

const CHAT_ID = "chat-selection";
const WORKFLOW = "builtin://agent";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

type Selection = { workflowName: string; params: Record<string, unknown>; version: bigint } | null;
const server = vi.hoisted(() => ({
  selection: null as Selection,
  updates: [] as Array<{ params: Record<string, unknown>; expectedVersion: bigint | undefined }>,
  failNextWith: null as unknown,
  refetches: 0,
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries");
  return {
    ...actual,
    useChat: () => ({ data: undefined, isLoading: false }),
    useChatSelection: () => ({ data: server.selection, isLoading: false }),
    getChatFromCache: () => undefined,
    getChatSelection: () => server.selection ?? undefined,
    setChatSelection: vi.fn(),
    refetchChatSelection: vi.fn(async () => {
      server.refetches += 1;
    }),
  };
});
vi.mock("../../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../../api/client")>("../../../api/client");
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: {
        ...actual.api.chatsV2,
        updateWorkflowParams: vi.fn(
          async (_chatId: string, params: Record<string, unknown>, _thread?: string, expectedVersion?: bigint) => {
            if (server.failNextWith) {
              const error = server.failNextWith;
              server.failNextWith = null;
              throw error;
            }
            server.updates.push({ params, expectedVersion });
            return { success: true, chat_id: CHAT_ID, message: "ok", selection: undefined };
          },
        ),
      },
    },
  };
});
vi.mock("../../../hooks/approval-queries", () => ({
  usePendingQuestions: () => ({ data: [], isLoading: false }),
}));

const DEFINITION = {
  data: {
    workflow: {
      inputs: { model: { type: "model" }, mode: { type: "string" } },
      presets: { tag: "agent" },
    },
  },
  error: null,
};
const PRESETS = { presets: [] as unknown[], loading: false };
const MODELS = {
  models: [
    { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", tags: ["flagship"], supportedThinkingLevels: ["low", "high"] },
    { id: "claude-5.5-opus@claude", name: "Claude Opus 5.5", tags: ["flagship"], supportedThinkingLevels: ["low", "high"] },
  ],
  tiers: {},
};
vi.mock("../../../store/globalDataStore", () => ({
  usePresetsForWorkflow: () => PRESETS,
  useModels: () => MODELS,
  useGlobalDataStore: Object.assign(() => undefined, { getState: () => ({ models: [] }) }),
}));
vi.mock("../../../hooks/workflow-config-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/workflow-config-queries")>(
    "../../../hooks/workflow-config-queries",
  );
  return { ...actual, useWorkflowDefinitionQuery: () => DEFINITION };
});
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => [] }));
vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: { getDefaultPresets: vi.fn().mockResolvedValue({}), listPresets: vi.fn().mockResolvedValue([]) },
}));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { ChatInput } from "../ChatInput";

const LEGACY_KEY = "reliant-chat-params";

function storeParams() {
  return useChatParamsStore.getState().getChatParams(CHAT_ID);
}

function renderComposer(busy = false) {
  return renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} isChatBusy={busy} />);
}

const PUSH_DEBOUNCE_WAIT = { timeout: 3000 };

beforeEach(() => {
  localStorage.clear();
  useProjectStore.setState({ currentProject: { id: "p-selection" } as never });
  useChatParamsStore.getState().removeChatParams(CHAT_ID);
  server.selection = null;
  server.updates.length = 0;
  server.failNextWith = null;
  server.refetches = 0;
});

describe("composer opens on the server's selection", () => {
  it("shows the chat's model on a device whose local store is empty", async () => {
    server.selection = {
      workflowName: WORKFLOW,
      params: { model: { id: "gpt-5.6-sol@codex" }, mode: "plan" },
      version: 3n,
    };
    expect(storeParams()).toEqual({}); // a "second device": nothing local

    renderComposer();

    await waitFor(() => expect(storeParams()).toEqual({ model: { id: "gpt-5.6-sol@codex" }, mode: "plan" }));
    await waitFor(() => expect(screen.getAllByText(/GPT-5\.6 Sol/).length).toBeGreaterThan(0));
  });

  it("pushes nothing just for being opened, even while the chat is busy", async () => {
    server.selection = { workflowName: WORKFLOW, params: { model: { id: "gpt-5.6-sol@codex" } }, version: 1n };

    renderComposer(true);
    await waitFor(() => expect(storeParams()).toHaveProperty("model"));
    await new Promise((resolve) => setTimeout(resolve, 900)); // past the push debounce

    expect(server.updates).toEqual([]);
  });

  it("does not show a selection made for another workflow", async () => {
    server.selection = { workflowName: "builtin://other", params: { model: { id: "gpt-5.6-sol@codex" } }, version: 1n };

    renderComposer();
    await new Promise((resolve) => setTimeout(resolve, 100));

    expect(storeParams()).toEqual({});
  });
});

describe("composer adopts what the server says runs", () => {
  it("takes a model the server moved, replacing the one that was shown", async () => {
    server.selection = { workflowName: WORKFLOW, params: { model: { id: "gpt-5.6-sol@codex" } }, version: 1n };
    const view = renderComposer();
    await waitFor(() => expect(storeParams().model).toEqual({ id: "gpt-5.6-sol@codex" }));

    // The send moved the pin off a provider that cannot serve it; the server's
    // selection now holds what runs.
    server.selection = { workflowName: WORKFLOW, params: { model: { id: "claude-5.5-opus@claude" } }, version: 2n };
    view.rerender(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} isChatBusy={false} />);

    await waitFor(() => expect(storeParams().model).toEqual({ id: "claude-5.5-opus@claude" }));
    expect(server.updates).toEqual([]); // adopting is not an edit
  });
});

describe("composer pushes edits to the selection", () => {
  it("pushes a Cmd+K H edit with the version it read, with no run live", async () => {
    server.selection = { workflowName: WORKFLOW, params: { model: { id: "claude-5.5-opus@claude" } }, version: 4n };
    renderComposer(false);
    await waitFor(() => expect(storeParams()).toHaveProperty("model"));

    act(() => {
      window.dispatchEvent(new CustomEvent("cycle-thinking-level"));
    });

    await waitFor(() => expect(server.updates.length).toBe(1), PUSH_DEBOUNCE_WAIT);
    expect(server.updates[0].expectedVersion).toBe(4n);
    expect(server.updates[0].params).toEqual({ model: { id: "claude-5.5-opus@claude", thinking_level: "high" } });
  });

  it("refetches the selection when the edit was made against a stale version, and does not push again until it has", async () => {
    server.selection = { workflowName: WORKFLOW, params: { model: { id: "claude-5.5-opus@claude" } }, version: 4n };
    renderComposer(false);
    await waitFor(() => expect(storeParams()).toHaveProperty("model"));
    server.failNextWith = new ConnectError("selection changed", Code.Aborted);

    act(() => {
      window.dispatchEvent(new CustomEvent("cycle-thinking-level"));
    });

    await waitFor(() => expect(server.refetches).toBeGreaterThan(0), PUSH_DEBOUNCE_WAIT);
    // The edit is still the user's: it stays in the composer for the next push.
    expect(storeParams().model).toEqual({ id: "claude-5.5-opus@claude", thinking_level: "high" });
  });
});

describe("a chat the server has no selection for", () => {
  it("is seeded once from what an older build kept in this browser, then forgets it", async () => {
    localStorage.setItem(
      LEGACY_KEY,
      JSON.stringify({
        version: 7,
        state: {
          chatParams: { [CHAT_ID]: { model: { id: "gpt-5.6-sol@codex" } }, "other-chat": { mode: "plan" } },
          chatPresets: {},
        },
      }),
    );
    server.selection = null;

    renderComposer();

    await waitFor(() => expect(server.updates.length).toBe(1), PUSH_DEBOUNCE_WAIT);
    expect(server.updates[0].params).toEqual({ model: { id: "gpt-5.6-sol@codex" } });
    const left = JSON.parse(localStorage.getItem(LEGACY_KEY) ?? "{}");
    expect(left.state.chatParams).toEqual({ "other-chat": { mode: "plan" } });
  });

  it("repairs a poisoned key first, so it never seeds a selection the server would refuse", async () => {
    localStorage.setItem(
      LEGACY_KEY,
      JSON.stringify({
        version: 7,
        state: {
          chatParams: { [CHAT_ID]: { model: { id: "claude-5.5-opus@claude" }, thinking_level: "high", "default.mode": "plan" } },
          chatPresets: {},
        },
      }),
    );

    renderComposer();

    await waitFor(() => expect(server.updates.length).toBe(1), PUSH_DEBOUNCE_WAIT);
    expect(server.updates[0].params).toEqual({
      model: { id: "claude-5.5-opus@claude", thinking_level: "high" },
      mode: "plan",
    });
    expect(server.updates[0].params).not.toHaveProperty("thinking_level");
  });

  it("is not seeded from local values once the server has a selection", async () => {
    localStorage.setItem(
      LEGACY_KEY,
      JSON.stringify({ version: 7, state: { chatParams: { [CHAT_ID]: { mode: "plan" } }, chatPresets: {} } }),
    );
    server.selection = { workflowName: WORKFLOW, params: { mode: "auto" }, version: 2n };

    renderComposer();
    await waitFor(() => expect(storeParams()).toEqual({ mode: "auto" }));
    await new Promise((resolve) => setTimeout(resolve, 800));

    expect(server.updates).toEqual([]);
  });
});
