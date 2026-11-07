/**
 * The inline composer and its ⊕ "Chat options" sheet
 * (dev-docs/design/mobile-chat-redesign.md §5.3–§5.5).
 *
 *   - One action slot, four states: disabled Send, Send, Stop, and ghost Stop
 *     plus Queue while the agent runs and there is text.
 *   - A chip row only when this send carries something beyond its text, each
 *     chip removable.
 *   - One sheet whose pages push inside it; a value changed on a page is the
 *     value the next send carries (parity: change it, send, check the request).
 *   - The model stays off the composer on touch (D1); controls that do not fit
 *     the row fold into ⊕ (priority+).
 */

import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useProjectStore } from "../../../store/projectStore";
import { useAttachmentStore } from "../../../store/attachmentStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { foldCount } from "../composerPriority";
import { useAuthStore } from "../../../store/authStore";
import { legacyPasteDraftKey } from "../useLongPaste";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>("../../../hooks/chat-queries");
  // "chat-started" has a first message, so its workflow is fixed.
  const STARTED = { id: "chat-started", lastMessageAt: { seconds: 1n } };
  return { ...actual, useChat: (id?: string) => ({ data: id === "chat-started" ? STARTED : undefined, isLoading: false }), useChatSelection: () => ({ data: undefined, isLoading: false }), getChatFromCache: () => undefined };
});
vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: [] }, isLoading: false }),
}));
const MODELS = {
  models: [{ id: "claude-sonnet@anthropic", name: "Claude Sonnet", driverId: "anthropic", provider: "Anthropic", tags: ["flagship", "fast"], supportedThinkingLevels: [] }],
  tiers: {},
  loading: false,
  error: null,
};
vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => MODELS,
  useGlobalDataStore: Object.assign(() => undefined, { getState: () => ({ models: [] }) }),
}));
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
const slash = vi.hoisted(() => ({ action: vi.fn() }));
vi.mock("../../../hooks/useSlashCommands", () => {
  const commands = [{ id: "branch", title: "Branch chat", description: "Branch from the last message", action: slash.action }];
  return { useSlashCommands: () => commands };
});

// InputDef is the decoded protobuf oneof.
const INPUTS = {
  model: { type: "model", config: { case: "modelInput", value: { base: { ui: "config" }, default: { tags: ["flagship"] } } } },
  mode: { type: "enum", config: { case: "enumInput", value: { base: { ui: "toolbar" }, enumValues: ["manual", "auto", "plan"], default: "auto" } } },
};
const definition = vi.hoisted(() => ({ result: undefined as unknown }));
vi.mock("../../../hooks/workflow-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
  useWorkflows: () => ({ workflows: [], loading: false, error: null }),
  useWorkflowDefinitionQuery: () => definition.result,
}));
const PRESETS = vi.hoisted(() => ({
  value: {
    presets: [{ name: "reviewer", description: "Review changes", source: "builtin", tag: "agent", params: { mode: "plan" } }],
    loading: false,
  },
}));
vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  usePresetsForWorkflow: () => PRESETS.value,
  fetchDefaultPresets: vi.fn().mockResolvedValue({}),
  useDefaultPresets: () => ({ defaults: {}, loading: false }),
}));
// A draft attachment without local bytes (restored after a reload) reads its text from the server.
vi.mock("../../../hooks/attachment-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/attachment-queries")>()),
  fetchAttachmentBlob: vi.fn(async () => ({ text: async () => "hello" }) as Blob),
  deleteAttachment: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => <span data-testid="inline-workflow" /> }));
vi.mock("../../../store/chatStoreHooks", () => ({ useActiveChatId: () => undefined }));

import { ChatInput } from "../ChatInput";

const PROJECT_ID = "p-composer";
const composer = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;
const type = (text: string) => fireEvent.change(composer(), { target: { value: text } });
const STABLE_DEFINITION = { data: { workflow: { inputs: INPUTS } }, isLoading: false, error: null, refetch: vi.fn() };

function primaryPointer(kind: "coarse" | "fine") {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: kind === "coarse" && query === "(pointer: coarse)",
    addEventListener: () => {},
    removeEventListener: () => {},
  }) as unknown as MediaQueryList);
}

beforeEach(() => {
  definition.result = STABLE_DEFINITION;
  useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never });
  useWorkspaceStateStore.getState().clearNewChatDraft(PROJECT_ID);
  useAttachmentStore.setState({ attachments: new Map() as never });
  slash.action.mockReset();
  useChatParamsStore.getState().clearTempNewChatParams();
});
afterEach(() => vi.unstubAllGlobals());

describe("long paste", () => {
  const LONG = "long text\n".repeat(600);
  function pasteLong(text = LONG) {
    const event = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", { value: { files: [], items: [], getData: () => text } });
    act(() => { composer().dispatchEvent(event); });
    return event;
  }
  function fileText(file: File) {
    return new Promise<string>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as string);
      reader.readAsText(file);
    });
  }
  const textAttachment = (id: string, filename: string) => ({ id, filename, mime_type: "text/plain", size: 5, url: "", attachment_type: "file_reference" as const });

  it("attaches each long paste as a numbered pasted-text chip, sent like any attachment", async () => {
    const original = useAttachmentStore.getState().attachFile;
    let next = 0;
    const attachFile = vi.fn(async (session: string, file: File) => {
      const attachment = textAttachment(`paste-${++next}`, file.name);
      const map = new Map(useAttachmentStore.getState().attachments);
      map.set(session, [...(map.get(session) ?? []), attachment]);
      useAttachmentStore.setState({ attachments: map });
      return attachment;
    });
    useAttachmentStore.setState({ attachFile });
    try {
      const onSend = vi.fn().mockResolvedValue(undefined);
      renderWithQuery(<ChatInput onSend={onSend} attachmentDraftId="paste-test" />);
      const event = pasteLong("exact 😀\r\n".repeat(600));
      expect(event.defaultPrevented).toBe(true);
      expect(composer().value).toBe("");
      await waitFor(() => expect(screen.getByRole("button", { name: "Open pasted-text.txt" })).toBeInTheDocument());
      expect(await fileText(attachFile.mock.calls[0][1])).toBe("exact 😀\r\n".repeat(600));
      expect(attachFile.mock.calls[0][1].type).toBe("text/plain");
      pasteLong();
      await waitFor(() => expect(screen.getByRole("button", { name: "Open pasted-text(2).txt" })).toBeInTheDocument());
      // No banner, no inline panel: the chips are the whole UI.
      expect(screen.queryByText(/Long paste attached/)).toBeNull();
      // Send stays disabled until the upload queue settles; clicking earlier is a no-op.
      await waitFor(() => expect(screen.getByTestId("send-button")).not.toBeDisabled());
      fireEvent.click(screen.getByTestId("send-button"));
      await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
      expect(onSend.mock.calls[0][1]).toEqual(["paste-1", "paste-2"]);
    } finally {
      act(() => useAttachmentStore.setState({ attachFile: original }));
    }
  });

  it("migrates a v1.10 new-chat paste only into the project it was pasted in", async () => {
    // v1.10 stored the desktop new chat's paste under [user, project, "temp"]
    // (its ChatInput keyed the store by JSON [userId, project id, session]).
    const p1Key = legacyPasteDraftKey("u1", "p1", "temp");
    localStorage.setItem(p1Key, JSON.stringify([{ id: 1, text: "p1's private paste" }]));
    const original = useAttachmentStore.getState().attachFile;
    const attachFile = vi.fn(async (_session: string, file: File) => textAttachment("migrated", file.name));
    useAttachmentStore.setState({ attachFile });
    act(() => useAuthStore.setState({ user: { id: "u1" } as never }));
    try {
      // A new chat in p2: p1's paste is neither attached nor removed.
      act(() => useProjectStore.setState({ currentProject: { id: "p2" } as never }));
      const view = renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId="new-chat:project:p2" />);
      await act(async () => {});
      expect(attachFile).not.toHaveBeenCalled();
      expect(JSON.parse(localStorage.getItem(p1Key)!)).toEqual([{ id: 1, text: "p1's private paste" }]);
      view.unmount();

      // Back in p1, its own new chat takes it.
      act(() => useProjectStore.setState({ currentProject: { id: "p1" } as never }));
      renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId="new-chat:project:p1" />);
      await waitFor(() => expect(attachFile).toHaveBeenCalledTimes(1));
      expect(attachFile.mock.calls[0][0]).toBe("new-chat:project:p1");
    } finally {
      localStorage.removeItem(p1Key);
      act(() => {
        useAttachmentStore.setState({ attachFile: original });
        useAuthStore.setState({ user: null } as never);
      });
    }
  });

  it("opens a text chip to edit: Save replaces the attachment, Insert puts the text in the message", async () => {
    const original = useAttachmentStore.getState().replaceAttachment;
    const originalRemove = useAttachmentStore.getState().removeAttachment;
    const replaceAttachment = vi.fn().mockResolvedValue(undefined);
    const removeAttachment = vi.fn();
    useAttachmentStore.setState({
      replaceAttachment,
      removeAttachment,
      attachments: new Map([["paste-edit", [textAttachment("t1", "pasted-text.txt")]]]),
    });
    try {
      renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId="paste-edit" />);
      fireEvent.click(screen.getByRole("button", { name: "Open pasted-text.txt" }));
      const editor = await screen.findByLabelText("Edit pasted-text.txt");
      expect((editor as HTMLTextAreaElement).value).toBe("hello");
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
      fireEvent.change(editor, { target: { value: "hello, edited" } });
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
      await waitFor(() => expect(replaceAttachment).toHaveBeenCalledTimes(1));
      const [session, id, file] = replaceAttachment.mock.calls[0];
      expect([session, id, file.name]).toEqual(["paste-edit", "t1", "pasted-text.txt"]);
      expect(await fileText(file)).toBe("hello, edited");
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

      fireEvent.click(screen.getByRole("button", { name: "Open pasted-text.txt" }));
      await screen.findByLabelText("Edit pasted-text.txt");
      fireEvent.click(screen.getByRole("button", { name: "Insert as text" }));
      expect(composer().value).toBe("hello");
      expect(removeAttachment).toHaveBeenCalledWith("paste-edit", "t1");
    } finally {
      act(() => useAttachmentStore.setState({ replaceAttachment: original, removeAttachment: originalRemove }));
    }
  });
});

describe("action slot", () => {
  it("idle and empty: Send, disabled", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    expect(screen.getByTestId("send-button")).toBeDisabled();
    expect(screen.queryByTestId("stop-button")).toBeNull();
  });

  it("idle with text: Send, which sends", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    type("ship it");
    fireEvent.click(screen.getByTestId("send-button"));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][0]).toBe("ship it");
  });

  it("running and empty: Stop alone", () => {
    const onStop = vi.fn();
    renderWithQuery(<ChatInput onSend={vi.fn()} onStop={onStop} isStreaming />);
    expect(screen.queryByTestId("send-button")).toBeNull();
    expect(screen.queryByTestId("queue-send-button")).toBeNull();
    fireEvent.click(screen.getByTestId("stop-button"));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("running with text: ghost Stop to the left of Queue", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} onStop={vi.fn()} isStreaming />);
    type("also do this");
    const stop = screen.getByTestId("stop-button");
    const queue = screen.getByTestId("queue-send-button");
    expect(stop.compareDocumentPosition(queue) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(queue).toBeEnabled();
  });
});

describe("Enter by pointer (D2)", () => {
  it("coarse: Return does not send, Cmd+Enter does", async () => {
    primaryPointer("coarse");
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    type("ship it");
    fireEvent.keyDown(composer(), { key: "Enter" });
    await act(async () => {});
    expect(onSend).not.toHaveBeenCalled();
    fireEvent.keyDown(composer(), { key: "Enter", metaKey: true });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
  });

  it("fine: Enter sends", async () => {
    primaryPointer("fine");
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    type("ship it");
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
  });
});

describe("chip row", () => {
  it("is absent when the send carries nothing but text", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    type("just text");
    expect(screen.queryByTestId("composer-chips")).toBeNull();
  });

  it("shows an attachment chip whose × removes it", async () => {
    const file = { id: "f1", filename: "notes.txt", mime_type: "text/plain", size: 1, url: "" };
    useAttachmentStore.setState({ attachments: new Map([["temp", [file]]]) as never });
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} attachmentDraftId="temp" />);
    const chips = screen.getByTestId("composer-chips");
    expect(within(chips).getByText("notes.txt")).toBeInTheDocument();
    // An attachment alone is something to send.
    expect(screen.getByTestId("send-button")).toBeEnabled();
    fireEvent.click(within(chips).getByRole("button", { name: "Remove notes.txt" }));
    expect(screen.queryByTestId("composer-chips")).toBeNull();
    expect(screen.getByTestId("send-button")).toBeDisabled();
  });

  it("shows a picked preset as a chip that clears it", async () => {
    definition.result = { ...STABLE_DEFINITION, data: { workflow: { inputs: INPUTS, presets: { tag: "agent" } } } };
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    const sheet = screen.getByRole("dialog");
    fireEvent.click(within(sheet).getByRole("button", { name: /^Preset/ }));
    fireEvent.click(within(sheet).getByRole("button", { name: /reviewer Review changes/ }));
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
    const chips = screen.getByTestId("composer-chips");
    expect(within(chips).getByText("Preset: reviewer")).toBeInTheDocument();
    fireEvent.click(within(chips).getByRole("button", { name: "Clear the preset" }));
    expect(screen.queryByTestId("composer-chips")).toBeNull();
  });
});

describe("⊕ sheet", () => {
  it("pages to All settings inside one sheet; the changed value is what the send carries", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    fireEvent.click(screen.getByTestId("options-settings-row"));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.getByTestId("composer-options")).toHaveAttribute("data-page", "settings");
    fireEvent.click(screen.getByRole("button", { name: "plan" }));
    // Back returns to the sheet's root, still open.
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByTestId("composer-options")).toHaveAttribute("data-page", "root");
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));

    type("go");
    fireEvent.click(screen.getByTestId("send-button"));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][3]).toMatchObject({ mode: "plan" });
  });

  it("picking the active mode again drops the override from the send", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    fireEvent.click(screen.getByTestId("options-settings-row"));
    fireEvent.click(screen.getByRole("button", { name: "plan" }));
    fireEvent.click(screen.getByRole("button", { name: "plan" }));
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));

    type("go");
    fireEvent.click(screen.getByTestId("send-button"));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    // No literal "auto" and no stale "plan": the workflow's default applies.
    expect(onSend.mock.calls[0][3] ?? {}).not.toHaveProperty("mode");
  });

  it("pages to the model, whose pick the send carries", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    fireEvent.click(screen.getByTestId("options-model-row"));
    expect(screen.getByTestId("composer-options")).toHaveAttribute("data-page", "model");
    fireEvent.click(screen.getByRole("button", { name: /^fast/i }));
    // Back from the page the Model row opened leaves for the root, not Chat settings.
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByTestId("composer-options")).toHaveAttribute("data-page", "root");
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));

    type("go");
    fireEvent.click(screen.getByTestId("send-button"));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][3]).toMatchObject({ model: { tags: ["fast"] } });
  });

  it("runs a command from the Commands page", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    fireEvent.click(screen.getByRole("button", { name: "Commands" }));
    fireEvent.click(screen.getByRole("button", { name: /Branch chat/ }));
    expect(slash.action).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("marks ⊕ when a required input is unset, and a send opens All settings instead", async () => {
    definition.result = {
      ...STABLE_DEFINITION,
      data: { workflow: { inputs: { ...INPUTS, goal: { type: "string", config: { case: "stringInput", value: { base: { required: true } } } } } } },
    };
    const onSend = vi.fn();
    renderWithQuery(<ChatInput onSend={onSend} />);
    expect(screen.getByTestId("options-required-dot")).toBeInTheDocument();
    type("go");
    fireEvent.click(screen.getByTestId("send-button"));
    await act(async () => {});
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.getByTestId("composer-options")).toHaveAttribute("data-page", "settings");
  });

  it("locks the workflow once the chat has started", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} chatId="chat-started" />);
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
    const row = screen.getByTestId("options-workflow-row");
    expect(row.tagName).not.toBe("BUTTON");
    expect(row).toHaveTextContent("Set when the chat began");
  });
});

describe("priority+ row", () => {
  it("keeps the model pill off the composer on a coarse pointer (D1)", () => {
    primaryPointer("coarse");
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    expect(within(screen.getByTestId("composer-tools")).queryByRole("button", { name: "Model settings" })).toBeNull();
  });

  it("keeps the model pill on a fine pointer", () => {
    primaryPointer("fine");
    renderWithQuery(<ChatInput onSend={vi.fn()} />);
    expect(within(screen.getByTestId("composer-tools")).getByRole("button", { name: "Model settings" })).toBeInTheDocument();
  });

  it("folds what does not fit, in priority order", () => {
    expect(foldCount(300, [100, 100, 100], 4)).toBe(2);
    expect(foldCount(308, [100, 100, 100], 4)).toBe(3);
    // A control rendering nothing takes no room and no gap.
    expect(foldCount(204, [100, 0, 100], 4)).toBe(3);
    // A prefix: once one does not fit, the rest fold even if they would.
    expect(foldCount(150, [100, 80, 10], 4)).toBe(1);
  });

  it("marks folded controls out of the row, and their ⊕ rows remain", () => {
    // Every control is 120px wide and the row has room for two.
    const width = vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
      return this.hasAttribute("data-fold-item") ? 120 : 0;
    });
    const room = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (this: HTMLElement) {
      return this.dataset.testid === "composer-tools" ? 250 : 0;
    });
    try {
      primaryPointer("fine");
      renderWithQuery(<ChatInput onSend={vi.fn()} />);
      const items = Array.from(screen.getByTestId("composer-tools").querySelectorAll("[data-fold-item]"));
      const keys = items.map((el) => [el.getAttribute("data-fold-item"), el.getAttribute("data-folded")]);
      // workflow and model fit; the toolbar param, account and settings fold.
      expect(keys).toEqual([
        ["workflow", null],
        ["model", null],
        ["param:mode", "true"],
        ["account", "true"],
        ["settings", "true"],
      ]);
      fireEvent.click(screen.getByRole("button", { name: "Chat options" }));
      expect(screen.getByTestId("options-settings-row")).toBeInTheDocument();
    } finally {
      width.mockRestore();
      room.mockRestore();
    }
  });
});
