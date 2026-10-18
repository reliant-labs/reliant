/** Mobile composer disclosure, sending states, and failure recovery. */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useChatStore } from "../../../store/chatStore";
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

import { SurfaceProvider } from "../../../lib/surfaceContext";
import { useAttachmentStore } from "../../../store/attachmentStore";
import { ChatInput } from "../ChatInput";

const PROJECT_ID = "p-prefill";
const composer = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

beforeEach(() => {
  useChatStore.setState({ activeChatId: null });
  useAttachmentStore.setState({ attachments: new Map(), uploading: false });
  useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never });
  useWorkspaceStateStore.getState().clearNewChatDraft(PROJECT_ID);
});

describe("Mobile chat composer", () => {
  const renderMobile = (props: Parameters<typeof ChatInput>[0]) => renderWithQuery(
    <SurfaceProvider surface="mobile"><ChatInput {...props} /></SurfaceProvider>,
  );
  it("opens Add and Chat options in one sheet, then restores trigger focus", () => {
    renderMobile({ onSend: vi.fn() });
    const trigger = screen.getByRole("button", { name: "Add files or chat options" });
    trigger.focus();
    fireEvent.click(trigger);
    expect(screen.getByRole("dialog", { name: "Add to chat" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    expect(screen.getByRole("dialog", { name: "Chat options" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(trigger);
  });
  it("keeps chat settings reachable when no model field is available", () => {
    renderMobile({ onSend: vi.fn() });
    fireEvent.click(screen.getByRole("button", {name:"Open chat options"}));
    expect(screen.getByRole("dialog", {name:"Chat options"})).toBeInTheDocument();
  });
  it("uses a short mobile placeholder", () => {
    renderMobile({ onSend: vi.fn() });
    expect(composer()).toHaveAttribute("placeholder", "Message Reliant");
  });
  it("keeps a new-chat draft and attachments when creation fails", async () => {
    const onSend = vi.fn().mockRejectedValue(new Error("offline"));
    const attachment = { id: "a1", filename: "notes.txt", size: 10, mime_type: "text/plain", url: "" };
    useAttachmentStore.setState({ attachments: new Map([[`mobile-new:${PROJECT_ID}`, [attachment]]]) });
    renderMobile({ onSend });
    fireEvent.change(composer(), { target: { value: "try again" } });
    fireEvent.click(screen.getByTestId("send-button"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(composer()).toHaveValue("try again");
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
  });
  it("isolates new-chat attachments from the previously active chat", () => {
    useChatStore.setState({ activeChatId: "previous-chat" });
    useAttachmentStore.setState({ attachments: new Map([["previous-chat", [{
      id: "old-file", filename: "previous-chat.txt", size: 10, mime_type: "text/plain", url: "",
    }]]]) });
    renderMobile({ onSend: vi.fn() });
    expect(screen.queryByText("previous-chat.txt")).not.toBeInTheDocument();
    expect(screen.getByTestId("send-button")).toBeDisabled();
  });
  it("disables sending and Add while uploads are active", () => {
    useAttachmentStore.setState({ uploading: true });
    renderMobile({ onSend: vi.fn(), prefill: { text: "hello", id: 1 } });
    expect(screen.getByTestId("send-button")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Add files or chat options" })).toBeDisabled();
    useAttachmentStore.setState({ uploading: false });
  });
  it("exposes queue alongside stop during streaming with a draft", () => {
    renderMobile({ onSend: vi.fn(), chatId: "mobile-running", isStreaming: true });
    fireEvent.change(composer(), { target: { value: "next task" } });
    expect(screen.getByTestId("queue-send-button")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Stop generation/ })).toBeInTheDocument();
  });
});
