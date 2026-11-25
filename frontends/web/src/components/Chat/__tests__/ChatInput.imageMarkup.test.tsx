/**
 * Image markup through the real composer: chip -> preview -> comment pin ->
 * Save replaces the attachment and writes the comments block into the draft;
 * saving again swaps the block; Revert re-uploads the original and removes it.
 * The real attachment store, ChatInput, AttachmentPreview and ImagePreviewModal
 * run; only the network (upload/delete/fetch) and the canvas flatten are mocked.
 */

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useProjectStore } from "../../../store/projectStore";
import { useAttachmentStore } from "../../../store/attachmentStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useChatParamsStore } from "../../../store/chatParamsStore";
import { fetchAttachmentBlob, uploadAttachment } from "../../../hooks/attachment-queries";

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
  const NO_CHAT = { data: undefined, isLoading: false };
  return { ...actual, useChat: () => NO_CHAT, useChatSelection: () => NO_CHAT, getChatFromCache: () => undefined };
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
const NO_COMMANDS: never[] = [];
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => NO_COMMANDS }));
const INPUTS = {
  model: { type: "model", config: { case: "modelInput", value: { base: { ui: "config" }, default: { tags: ["flagship"] } } } },
};
// Stable identities: a fresh object per render would loop the composer's effects.
const WORKFLOWS = { workflows: [], loading: false, error: null };
const DEFINITION = { data: { workflow: { inputs: INPUTS } }, isLoading: false, error: null, refetch: vi.fn() };
vi.mock("../../../hooks/workflow-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
  useWorkflows: () => WORKFLOWS,
  useWorkflowDefinitionQuery: () => DEFINITION,
}));
const PRESETS = { presets: [], loading: false };
const DEFAULT_PRESETS = { defaults: {}, loading: false };
vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  usePresetsForWorkflow: () => PRESETS,
  fetchDefaultPresets: vi.fn().mockResolvedValue({}),
  useDefaultPresets: () => DEFAULT_PRESETS,
}));
vi.mock("../../../hooks/attachment-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/attachment-queries")>()),
  uploadAttachment: vi.fn(),
  fetchAttachmentBlob: vi.fn(),
  deleteAttachment: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => <span data-testid="inline-workflow" /> }));
vi.mock("../../../store/chatStoreHooks", () => ({ useActiveChatId: () => undefined }));
vi.mock("../../../hooks/useFocusManager", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/useFocusManager")>()),
  focusChatInput: vi.fn(),
}));
const flatten = vi.hoisted(() => ({ fn: vi.fn() }));
vi.mock("../imageMarkup/renderCanvas", async (importActual) => ({
  ...(await importActual<typeof import("../imageMarkup/renderCanvas")>()),
  flattenImage: flatten.fn,
}));

import { ChatInput } from "../ChatInput";

const SESSION = "img-session";
const ORIGINAL = new File(["original-bytes"], "photo.png", { type: "image/png" });
const COMMENT_HEADER = "Comments on photo-marked.png (numbers match the pins on the image):";

const composer = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;
const type = (text: string) => fireEvent.change(composer(), { target: { value: text } });
const pinInput = () => screen.getByPlaceholderText("Add a comment…") as HTMLInputElement;
const draftIds = () => (useAttachmentStore.getState().attachments.get(SESSION) ?? []).map((a) => a.id);

let nextId = 0;
const uploads: File[] = [];

beforeEach(() => {
  nextId = 0;
  uploads.length = 0;
  let urls = 0;
  URL.createObjectURL = vi.fn(() => `blob:url-${++urls}`);
  URL.revokeObjectURL = vi.fn();
  flatten.fn.mockReset();
  flatten.fn.mockImplementation(async () => new File(["flattened"], "photo-marked.png", { type: "image/png" }));
  vi.mocked(uploadAttachment).mockReset();
  vi.mocked(uploadAttachment).mockImplementation(async (file: File) => {
    uploads.push(file);
    return { id: `up-${++nextId}`, filename: file.name, mime_type: "image/png", size: file.size, url: "", attachment_type: "image" };
  });
  vi.mocked(fetchAttachmentBlob).mockReset();

  useProjectStore.setState({ currentProject: { id: "p-img" } as never });
  useWorkspaceStateStore.getState().clearNewChatDraft("p-img");
  useChatParamsStore.getState().clearTempNewChatParams();
  useAttachmentStore.setState({
    attachments: new Map([
      [SESSION, [{ id: "orig-1", filename: "photo.png", mime_type: "image/png", size: 14, url: "", attachment_type: "image" as const }]],
    ]),
    blobUrls: new Map([["orig-1", "blob:thumb-orig"]]),
    localFiles: new Map(),
    localImages: new Map([["orig-1", ORIGINAL]]),
    markups: new Map(),
    uploading: false,
    inFlight: 0,
  });
});
afterEach(() => vi.unstubAllGlobals());

/** Open the chip named `name`, and fire the editor image's load so its toolbar appears. */
function openChip(name: string, alt: string) {
  fireEvent.click(screen.getByRole("button", { name: `Open ${name}` }));
  const img = screen.getByAltText(alt) as HTMLImageElement;
  Object.defineProperty(img, "naturalWidth", { configurable: true, value: 800 });
  Object.defineProperty(img, "naturalHeight", { configurable: true, value: 600 });
  fireEvent.load(img);
}

async function saveAndWaitClosed() {
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
}

function addComment(text: string) {
  fireEvent.click(screen.getByRole("button", { name: "Comment" }));
  fireEvent.click(screen.getByTestId("comment-layer"), { clientX: 100, clientY: 120 });
  fireEvent.change(pinInput(), { target: { value: text } });
  fireEvent.keyDown(pinInput(), { key: "Enter" });
}

describe("image markup in the composer", () => {
  it("saving a comment replaces the chip and writes the comments block into the draft", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("make this bigger");
    await saveAndWaitClosed();

    expect(uploads).toHaveLength(1);
    expect(uploads[0].name).toBe("photo-marked.png");
    expect(draftIds()).toEqual(["up-1"]);
    expect(screen.getByRole("button", { name: "Open photo-marked.png" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open photo.png" })).toBeNull();
    expect(composer().value).toBe(`${COMMENT_HEADER}\n1. make this bigger`);

    // What re-editing needs is kept in memory: the original and the doc.
    const markup = useAttachmentStore.getState().getMarkup("up-1");
    expect(markup?.source).toBe(ORIGINAL);
    expect(markup?.doc.shapes).toMatchObject([{ kind: "pin", n: 1, text: "make this bigger" }]);
  });

  it("saving again replaces the block instead of adding a second one", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("make this bigger");
    await saveAndWaitClosed();

    // The user keeps typing around the block; the swap must leave it alone.
    type(`Please fix.\n\n${composer().value}\n\nThanks`);

    openChip("photo-marked.png", "photo-marked.png");
    // The editor reopens on the original with the earlier pin in place.
    expect(screen.getByRole("button", { name: "Revert to original" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));
    fireEvent.click(screen.getByRole("button", { name: "Comment 1: make this bigger" }));
    fireEvent.change(pinInput(), { target: { value: "make this smaller" } });
    fireEvent.keyDown(pinInput(), { key: "Enter" });
    await saveAndWaitClosed();

    expect(uploads).toHaveLength(2);
    expect(draftIds()).toEqual(["up-2"]);
    expect(composer().value).toBe(`Please fix.\n\n${COMMENT_HEADER}\n1. make this smaller\n\nThanks`);
    expect(composer().value.match(/Comments on/g)).toHaveLength(1);
    // The earlier upload's markup entry was dropped with it; the new one carries the edit.
    expect(useAttachmentStore.getState().getMarkup("up-1")).toBeUndefined();
    expect(useAttachmentStore.getState().getMarkup("up-2")?.doc.shapes).toMatchObject([{ text: "make this smaller" }]);
  });

  it("appends a second save's block when the user edited the first one away", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("one");
    await saveAndWaitClosed();

    type("my own words");
    openChip("photo-marked.png", "photo-marked.png");
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));
    fireEvent.click(screen.getByRole("button", { name: "Comment 1: one" }));
    fireEvent.change(pinInput(), { target: { value: "two" } });
    fireEvent.keyDown(pinInput(), { key: "Enter" });
    await saveAndWaitClosed();

    expect(composer().value).toBe(`my own words\n\n${COMMENT_HEADER}\n1. two`);
  });

  it("Revert to original re-uploads the original file and removes the block", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("make this bigger");
    await saveAndWaitClosed();
    expect(composer().value).toContain("Comments on");

    openChip("photo-marked.png", "photo-marked.png");
    fireEvent.click(screen.getByRole("button", { name: "Revert to original" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    expect(uploads).toHaveLength(2);
    expect(uploads[1]).toBe(ORIGINAL);
    expect(draftIds()).toEqual(["up-2"]);
    expect(screen.getByRole("button", { name: "Open photo.png" })).toBeInTheDocument();
    expect(composer().value).toBe("");
    // Back to a plain image: no markup, so no Revert offered the next time.
    expect(useAttachmentStore.getState().getMarkup("up-2")).toBeUndefined();
    openChip("photo.png", "photo.png");
    expect(screen.queryByRole("button", { name: "Revert to original" })).toBeNull();
  });

  it("Revert takes the block out and leaves the user's surrounding text", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("note");
    await saveAndWaitClosed();
    type(`Look at this\n\n${composer().value}`);

    openChip("photo-marked.png", "photo-marked.png");
    fireEvent.click(screen.getByRole("button", { name: "Revert to original" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(composer().value).toBe("Look at this");
  });

  it("keeps the chip and draft untouched when the upload fails, and the modal open", async () => {
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error("network down"));
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    addComment("lost?");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(vi.mocked(uploadAttachment)).toHaveBeenCalledTimes(1));
    await act(async () => {});
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(draftIds()).toEqual(["orig-1"]);
    expect(composer().value).toBe("");
    expect(screen.getByRole("button", { name: "Comment 1: lost?" })).toBeInTheDocument();
  });

  it("opens a draft restored after a reload read-only until its bytes are fetched, then editable", async () => {
    useAttachmentStore.setState({ localImages: new Map(), blobUrls: new Map([["orig-1", "blob:thumb-orig"]]) });
    let resolve!: (blob: Blob) => void;
    vi.mocked(fetchAttachmentBlob).mockReturnValue(new Promise<Blob>((r) => (resolve = r)));
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    fireEvent.click(screen.getByRole("button", { name: "Open photo.png" }));
    expect(fetchAttachmentBlob).toHaveBeenCalledWith("orig-1");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.queryByTestId("markup-editor")).toBeNull();

    await act(async () => resolve(ORIGINAL));
    const img = screen.getByAltText("photo.png") as HTMLImageElement;
    Object.defineProperty(img, "naturalWidth", { configurable: true, value: 800 });
    Object.defineProperty(img, "naturalHeight", { configurable: true, value: 600 });
    fireEvent.load(img);
    expect(screen.getByRole("toolbar", { name: "Edit image" })).toBeInTheDocument();
  });

  it("opens a local image without fetching its bytes", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} attachmentDraftId={SESSION} />);
    openChip("photo.png", "photo.png");
    expect(screen.getByRole("toolbar", { name: "Edit image" })).toBeInTheDocument();
    expect(fetchAttachmentBlob).not.toHaveBeenCalled();
  });
});
