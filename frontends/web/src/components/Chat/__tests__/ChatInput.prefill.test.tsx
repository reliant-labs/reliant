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

vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries");
  return { ...actual, useChat: () => ({ data: undefined, isLoading: false }), useChatSelection: () => ({ data: undefined, isLoading: false }), getChatFromCache: () => undefined };
});
vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: [] }, isLoading: false }),
}));
const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];
vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => NO_MODELS,
  useGlobalDataStore: Object.assign(() => undefined, { getState: () => ({ models: [] }) }),
}));
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => NO_SLASH_COMMANDS }));
const definitionState = vi.hoisted(() => ({ result: undefined as unknown }));
// The selected workflow has no inputs. One stable result, as React Query
// returns one: a fresh object per render re-derives the params every render.
vi.mock("../../../hooks/workflow-queries", async (importActual) => {
  const NO_DEFINITION = { data: { workflow: undefined, version: 0, status: "complete", validationErrors: [] } };
  return {
    ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
    useWorkflows: () => ({ workflows: [], loading: false, error: null }),
    fetchWorkflow: vi.fn().mockResolvedValue(NO_DEFINITION.data),
    useWorkflowDefinitionQuery: () => definitionState.result ?? NO_DEFINITION,
  };
});
vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  usePresetsForWorkflow: () => NO_PRESETS,
  fetchDefaultPresets: vi.fn().mockResolvedValue({}),
}));
vi.mock("../settings", () => ({ ChatSettingsPopover: (props: { mobile: boolean; initialPage: string; disabled: boolean }) => <div data-testid="shared-settings" data-mobile={props.mobile} data-page={props.initialPage} data-disabled={props.disabled} /> }));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { ChatInput } from "../ChatInput";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { useAttachmentStore } from "../../../store/attachmentStore";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { NewChatDraftBoundary, newChatAttachmentDraftId } from "../NewChatDraftBoundary";
vi.mock("../../../store/chatStoreHooks", () => ({ useActiveChatId: () => "existing-a" }));

const PROJECT_ID = "p-prefill";
const composer = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

beforeEach(() => {
  definitionState.result = undefined;
  useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never });
  useWorkspaceStateStore.getState().clearNewChatDraft(PROJECT_ID);
});

describe("ChatInput prefill", () => {
  it("opens authoritative all-settings from mobile options, including while streaming", async () => {
    definitionState.result = { data: { workflow: { inputs: { goal: { type: "string", config: { case: "stringInput", value: { base: { required: true } } } } } } }, isLoading: false };
    renderWithQuery(<SurfaceProvider surface="mobile"><ChatInput isStreaming onSend={vi.fn()} /></SurfaceProvider>);
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.click(screen.getByRole("button", { name: /All settings/ }));
    expect(screen.getByTestId("shared-settings")).toHaveAttribute("data-mobile", "true");
    expect(screen.getByTestId("shared-settings")).toHaveAttribute("data-page", "main");
    expect(screen.getByTestId("shared-settings")).toHaveAttribute("data-disabled", "false");
  });

  it("explains mobile settings loading and failure and offers a retry", async () => {
    const refetch = vi.fn();
    definitionState.result = { isLoading: true, refetch };
    const ui = <SurfaceProvider surface="mobile"><ChatInput onSend={vi.fn()} /></SurfaceProvider>;
    const { rerender } = renderWithQuery(ui);
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByText("Loading workflow settings…")).toBeInTheDocument();
    definitionState.result = { isLoading: false, error: new Error("Offline"), refetch };
    rerender(<SurfaceProvider surface="mobile"><ChatInput onSend={vi.fn()} /></SurfaceProvider>);
    expect(screen.getByText("Could not load workflow settings.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry settings" }));
    expect(refetch).toHaveBeenCalled();
  });
  it("keeps existing-chat attachments separate from project new-chat drafts and clears only the submitted bucket", async () => {
    const a = { id: "existing-file", filename: "existing.txt", mime_type: "text/plain", size: 1 };
    const b = { ...a, id: "new-file", filename: "new.txt" };
    const c = { ...a, id: "other-project", filename: "other.txt" };
    const draft = newChatAttachmentDraftId(PROJECT_ID);
    const otherDraft = newChatAttachmentDraftId("other-project");
    useAttachmentStore.setState({ attachments: new Map([["existing-a", [a]], [draft, [b]], [otherDraft, [c]]]) as never });
    const send = vi.fn().mockResolvedValue(undefined);
    const renderComposer = (project: string) => <SurfaceProvider surface="mobile"><NewChatDraftBoundary key={project} projectId={project}><ChatInput attachmentDraftId={newChatAttachmentDraftId(project)} onSend={send} prefill={{ text: "send", id: 1 }} /></NewChatDraftBoundary></SurfaceProvider>;
    const { rerender } = renderWithQuery(<ChatInput chatId="existing-a" onSend={send} />);
    rerender(renderComposer(PROJECT_ID));
    await waitFor(() => expect(composer().value).toBe("send"));
    act(() => { useChatParamsStore.getState().setTempNewChatParams({ secret: "project-a" }); useChatParamsStore.getState().setTempNewChatWorkflow("project-a-workflow"); });
    act(() => useProjectStore.setState({ currentProject: { id: "other-project" } as never }));
    rerender(renderComposer("other-project"));
    await waitFor(() => expect(composer().value).toBe("send"));
    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({});
    expect(useChatParamsStore.getState().tempNewChatWorkflow).toBeNull();
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(send).toHaveBeenCalled());
    expect(send.mock.calls[0][1]).toEqual(["other-project"]);
    expect(useAttachmentStore.getState().getAttachments("existing-a")).toEqual([a]);
    expect(useAttachmentStore.getState().getAttachments(draft)).toEqual([b]);
    expect(useAttachmentStore.getState().getAttachments(otherDraft)).toEqual([]);
    act(() => useAttachmentStore.getState().reset());
  });
  it("never imports a desktop composer's configuration from another project into a remounted mobile composer", async () => {
    const OTHER = "p-prefill-desktop";
    const mobile = () => <SurfaceProvider surface="mobile"><NewChatDraftBoundary key={PROJECT_ID} projectId={PROJECT_ID}><ChatInput attachmentDraftId={newChatAttachmentDraftId(PROJECT_ID)} onSend={vi.fn()} /></NewChatDraftBoundary></SurfaceProvider>;
    const { rerender } = renderWithQuery(mobile());
    await waitFor(() => expect(composer()).toBeInTheDocument());
    act(() => { useChatParamsStore.getState().setTempNewChatParams({ secret: "project-a" }); useChatParamsStore.getState().setTempNewChatWorkflow("project-a-workflow"); });

    // Desktop, project B: the same temp slot, written with no mobile boundary.
    act(() => useProjectStore.setState({ currentProject: { id: OTHER } as never }));
    rerender(<ChatInput onSend={vi.fn()} />);
    act(() => { useChatParamsStore.getState().setTempNewChatParams({ secret: "project-b" }); useChatParamsStore.getState().setTempNewChatWorkflow("project-b-workflow"); useChatParamsStore.getState().setTempNewChatPresets({ group: "project-b-preset" }); });

    // Back to mobile, project A: B's choices must not ride along.
    act(() => useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never }));
    rerender(mobile());
    await waitFor(() => expect(composer()).toBeInTheDocument());
    const temp = useChatParamsStore.getState();
    expect(temp.tempNewChatParams).not.toHaveProperty("secret");
    expect(temp.tempNewChatWorkflow).not.toBe("project-b-workflow");
    expect(temp.tempNewChatPresets).toEqual({});
  });
  it("retains a mobile draft on failure and blocks duplicate sends and edits in flight", async () => {
    let reject!: (reason: Error) => void;
    const onSend = vi.fn(() => new Promise<void>((_, r) => { reject = r; }));
    renderWithQuery(<SurfaceProvider surface="mobile"><ChatInput onSend={onSend} prefill={{ text: "keep me", id: 10 }} /></SurfaceProvider>);
    await waitFor(() => expect(composer().value).toBe("keep me"));
    fireEvent.keyDown(composer(), { key: "Enter" });
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(composer()).toBeDisabled();
    await act(async () => reject(new Error("offline")));
    expect(composer().value).toBe("keep me");
    expect(composer()).not.toBeDisabled();
  });
  it("lets a mobile send omit an optional input and refuses one that omits a required input", async () => {
    const stringInput = (base: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
      ({ type: "string", config: { case: "stringInput", value: { base, ...extra } } });
    // An optional input is one with a default (model.IsInputRequired).
    definitionState.result = { data: { workflow: { inputs: { tone: stringInput({}, { default: "plain" }) } } }, isLoading: false };
    const optionalSend = vi.fn().mockResolvedValue(undefined);
    const { unmount } = renderWithQuery(<SurfaceProvider surface="mobile"><ChatInput onSend={optionalSend} prefill={{ text: "go", id: 20 }} /></SurfaceProvider>);
    await waitFor(() => expect(composer().value).toBe("go"));
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(optionalSend).toHaveBeenCalledTimes(1));
    unmount();

    definitionState.result = { data: { workflow: { inputs: { goal: stringInput({ required: true }) } } }, isLoading: false };
    const requiredSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<SurfaceProvider surface="mobile"><ChatInput onSend={requiredSend} prefill={{ text: "go", id: 21 }} /></SurfaceProvider>);
    await waitFor(() => expect(composer().value).toBe("go"));
    fireEvent.keyDown(composer(), { key: "Enter" });
    await act(async () => {});
    expect(requiredSend).not.toHaveBeenCalled();
    expect(composer().value).toBe("go");
  });

  it("clears only the submitted mobile draft when a deferred send settles after a project switch", async () => {
    const OTHER = "p-prefill-other";
    useWorkspaceStateStore.getState().setNewChatDraft(OTHER, "other project's draft");
    let resolve!: () => void;
    const onSend = vi.fn(() => new Promise<void>((r) => { resolve = r; }));
    renderWithQuery(<SurfaceProvider surface="mobile"><ChatInput onSend={onSend} prefill={{ text: "submitted", id: 30 }} /></SurfaceProvider>);
    await waitFor(() => expect(composer().value).toBe("submitted"));
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    act(() => useProjectStore.setState({ currentProject: { id: OTHER } as never }));
    await waitFor(() => expect(composer().value).toBe("other project's draft"));
    await act(async () => resolve());

    expect(composer().value).toBe("other project's draft");
    expect(useWorkspaceStateStore.getState().getNewChatDraft(OTHER)).toBe("other project's draft");
    expect(useWorkspaceStateStore.getState().getNewChatDraft(PROJECT_ID)).toBe("");
    act(() => useWorkspaceStateStore.getState().clearNewChatDraft(OTHER));
  });

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
