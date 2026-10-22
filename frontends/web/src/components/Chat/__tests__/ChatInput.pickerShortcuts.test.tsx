/**
 * Two composer controls edit the params that every later send carries, so what
 * they store has to be what a send accepts:
 *
 *   - Cmd+K H cycles reasoning effort. Effort is `model.thinking_level`; a
 *     top-level `thinking_level` is an unknown workflow input and the server
 *     refuses every send that carries it.
 *   - The settings popover's preset picker targets the workflow, whose key is
 *     "". Targeting "default" wrote `default.model`-style dotted keys, which
 *     the send path rejects.
 */

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { useProjectStore } from "../../../store/projectStore";
import { buildWorkflowParamsPayload } from "../../../api/chat-grpc";

const CHAT_ID = "chat-picker";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
// The chat's server-side selection is what the composer opens on. Each test
// sets it; `null` is a chat the server holds no selection for.
const serverSelection: { current: { workflowName: string; params: Record<string, unknown>; version: bigint } | null } = {
  current: null,
};
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries");
  return {
    ...actual,
    useChat: () => ({ data: undefined, isLoading: false }),
    useChatSelection: () => ({ data: serverSelection.current, isLoading: false }),
    getChatFromCache: () => undefined,
    refetchChatSelection: vi.fn().mockResolvedValue(undefined),
  };
});
// Edits are pushed to the server; capture them instead of reaching the network.
const pushed = vi.hoisted(() => ({ calls: [] as Array<Record<string, unknown>> }));
vi.mock("../../../lib/chatSelection", () => ({
  pushChatParams: vi.fn(async (_chatId: string, params: Record<string, unknown>) => {
    pushed.calls.push(params);
    return null;
  }),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  usePendingQuestions: () => ({ data: [], isLoading: false }),
}));

const DEFINITION = {
  data: { workflow: { inputs: { model: { type: "model" } }, presets: { tag: "agent" } } },
  error: null,
};
const PRESETS = { presets: [] as unknown[], loading: false };
const PRESET = { name: "fast-preset", description: "", source: "builtin", tag: "agent", params: { model: "gpt-5" } };
const MODELS = {
  models: [{ id: "gpt-5@openai", tags: ["flagship"], supportedThinkingLevels: ["low", "high"] }],
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
  return {
    ...actual,
    useWorkflowDefinitionQuery: () => DEFINITION,
  };
});
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => [] }));
vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: { getDefaultPresets: vi.fn().mockResolvedValue({}), listPresets: vi.fn().mockResolvedValue([]) },
}));
vi.mock("../settings", () => ({
  ChatSettingsPopover: (props: { onPresetChange: (p: unknown) => void; selectedPreset: string | null }) => (
    <button data-testid="pick-preset" onClick={() => props.onPresetChange(PRESET)}>
      {props.selectedPreset ?? "None"}
    </button>
  ),
}));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { ChatInput } from "../ChatInput";

PRESETS.presets.push(PRESET);

beforeEach(() => {
  useProjectStore.setState({ currentProject: { id: "p-picker" } as never });
  useChatParamsStore.getState().removeChatParams(CHAT_ID);
  serverSelection.current = null;
  pushed.calls.length = 0;
});

describe("Cmd+K H (cycle thinking level)", () => {
  it("writes the level onto the model selector, so the next send is accepted", async () => {
    serverSelection.current = { workflowName: "builtin://agent", params: { model: { tags: ["flagship"] } }, version: 1n };
    renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} />);

    act(() => {
      window.dispatchEvent(new CustomEvent("cycle-thinking-level"));
    });

    await waitFor(() =>
      expect(useChatParamsStore.getState().getChatParams(CHAT_ID)).toEqual({
        model: { tags: ["flagship"], thinking_level: "high" },
      }),
    );
    expect(() => buildWorkflowParamsPayload(useChatParamsStore.getState().getChatParams(CHAT_ID))).not.toThrow();
    expect(useChatParamsStore.getState().getChatParams(CHAT_ID)).not.toHaveProperty("thinking_level");
  });
});

describe("settings popover preset picker", () => {
  it("stores the preset under the workflow target, with no dotted keys", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} />);

    act(() => {
      window.dispatchEvent(new CustomEvent("open-model-selector"));
    });
    fireEvent.click(await screen.findByTestId("pick-preset"));

    const params = useChatParamsStore.getState().getChatParams(CHAT_ID);
    expect(Object.keys(params).some((k) => k.includes("."))).toBe(false);
    expect(params.model).toEqual({ id: "gpt-5" });
    expect(useChatParamsStore.getState().getChatPresets(CHAT_ID)).toEqual({ "": "fast-preset" });
    expect(() => buildWorkflowParamsPayload(params)).not.toThrow();
    await waitFor(() => expect(screen.getByTestId("pick-preset").textContent).toBe("fast-preset"));
  });
});
