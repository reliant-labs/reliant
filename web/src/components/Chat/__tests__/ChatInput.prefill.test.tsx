/**
 * A host can put text in a new chat's composer (`prefill`): the workflow
 * editor puts a reference to the workflow there, and its suggested prompts
 * after it.
 *
 *   - It wins over the draft the composer seeds itself from when it mounts.
 *   - Each prefill is applied once, by id: re-rendering with the same one
 *     leaves what the user typed alone, and a new id replaces it.
 *   - It is saved as the draft at once, so nothing re-reads a stale one.
 */

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useProjectStore } from "../../../store/projectStore";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

vi.mock("../../../api/chat-grpc", () => ({ chatGrpc: { sendAgentMessage: vi.fn() } }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries");
  return { ...actual, useChat: () => ({ data: undefined, isLoading: false }), getChatFromCache: () => undefined };
});
vi.mock("../../../hooks/approval-queries", () => ({
  usePendingQuestion: () => ({ data: null, isLoading: false }),
}));
const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];
vi.mock("../../../store/globalDataStore", () => ({
  usePresetsForWorkflow: () => NO_PRESETS,
  useModels: () => NO_MODELS,
  useGlobalDataStore: Object.assign(() => undefined, { getState: () => ({ models: [] }) }),
}));
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => NO_SLASH_COMMANDS }));
vi.mock("../../../api/workflow-grpc", () => ({
  workflowGrpc: { getWorkflow: vi.fn().mockResolvedValue({ workflow: null }) },
}));
vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: { getDefaultPresets: vi.fn().mockResolvedValue({}), listPresets: vi.fn().mockResolvedValue([]) },
}));
vi.mock("../settings", () => ({ ChatSettingsPopover: () => null }));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { ChatInput } from "../ChatInput";

const PROJECT_ID = "p-prefill";
const composer = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

beforeEach(() => {
  useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never });
  useWorkspaceStateStore.getState().clearNewChatDraft(PROJECT_ID);
});

describe("ChatInput prefill", () => {
  it("shows the prefill over the draft it seeds from, and saves it as the draft", async () => {
    useWorkspaceStateStore.getState().setNewChatDraft(PROJECT_ID, "Workflow `old-one`: ");
    renderWithQuery(<ChatInput onSend={vi.fn()} prefill={{ text: "Workflow `new-one`: ", id: 0 }} />);

    await waitFor(() => expect(composer().value).toBe("Workflow `new-one`: "));
    expect(useWorkspaceStateStore.getState().getNewChatDraft(PROJECT_ID)).toBe("Workflow `new-one`: ");
  });

  it("applies each prefill once: a re-render keeps the user's edit, a new id replaces it", async () => {
    const onSend = vi.fn();
    const { rerender } = renderWithQuery(<ChatInput onSend={onSend} prefill={{ text: "Add a step that ", id: 1 }} />);
    await waitFor(() => expect(composer().value).toBe("Add a step that "));

    fireEvent.change(composer(), { target: { value: "Add a step that posts to Slack" } });
    rerender(<ChatInput onSend={onSend} prefill={{ text: "Add a step that ", id: 1 }} />);
    expect(composer().value).toBe("Add a step that posts to Slack");

    rerender(<ChatInput onSend={onSend} prefill={{ text: "Add a step that ", id: 2 }} />);
    await waitFor(() => expect(composer().value).toBe("Add a step that "));
  });

  it("puts the caret after the prefilled text", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} prefill={{ text: "Workflow `x`: ", id: 0 }} />);
    await waitFor(() => expect(composer().value).toBe("Workflow `x`: "));
    await act(() => new Promise((resolve) => requestAnimationFrame(() => resolve(undefined))));
    expect(document.activeElement).toBe(composer());
    expect(composer().selectionStart).toBe("Workflow `x`: ".length);
  });
});
