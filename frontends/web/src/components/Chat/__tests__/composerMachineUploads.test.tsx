/**
 * A file chat can't attach (an xlsx) dropped or picked in the composer is
 * sent to the chat's machine, into ~/.reliant/uploads/<chat>/, instead of
 * being refused: a chip shows it, Send waits for it, and the message that is
 * sent names its absolute path for the agent.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useAttachmentStore } from "../../../store/attachmentStore";
import { useMachineUploadStore } from "../../../store/machineUploadStore";
import { UploadDestination } from "../../../gen/reliant/v1/filesystem_pb";

const CHAT_ID = "chat-abc";
const MAIN_THREAD_ID = "workflow-xyz";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const { sendAgentMessageMock, toastErrorMock, beginMock, chunkMock, commitMock } = vi.hoisted(() => ({
  sendAgentMessageMock: vi.fn(),
  toastErrorMock: vi.fn(),
  beginMock: vi.fn(),
  chunkMock: vi.fn(),
  commitMock: vi.fn(),
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    error: toastErrorMock,
    info: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
  },
}));

vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return {
    ...actual,
    useChat: () => ({
      data: { id: CHAT_ID, workflowId: MAIN_THREAD_ID, projectId: "project-1" },
      isLoading: false,
    }),
    getChatFromCache: () => undefined,
  };
});

vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: [] }, isLoading: false }),
}));

const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => NO_MODELS,
  useGlobalDataStore: Object.assign(() => undefined, {
    getState: () => ({ models: [] }),
  }),
}));

vi.mock("../../Settings/ModelPreferences", () => ({
  loadTagModelConfigs: vi.fn().mockResolvedValue({}),
}));

vi.mock("../../../hooks/useSlashCommands", () => ({
  useSlashCommands: () => NO_SLASH_COMMANDS,
}));

// The selected workflow has no inputs. One stable result, as React Query
// returns one: a fresh object per render re-derives the params every render.
vi.mock("../../../hooks/workflow-queries", async (importActual) => {
  const NO_DEFINITION = { data: { workflow: undefined, version: 0, status: "complete", validationErrors: [] } };
  return {
    ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
    fetchWorkflow: vi.fn().mockResolvedValue(NO_DEFINITION.data),
    useWorkflowDefinitionQuery: () => NO_DEFINITION,
  };
});

vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  usePresetsForWorkflow: () => NO_PRESETS,
  fetchDefaultPresets: vi.fn().mockResolvedValue({}),
}));

vi.mock("../settings", () => ({
  ChatSettingsPopover: () => null,
}));
vi.mock("../WorkflowSelector", () => ({
  WorkflowSelector: () => null,
}));

import { ChatInput } from "../ChatInput";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { emptyChatPullReads } from "@/test/chatPullReads";

// ChatService's pull reads are generated-key reads, so they are answered at
// the transport.
beforeEach(() =>
  setTransport(
    mockTransport({
      ...emptyChatPullReads(),
      "ChatService.SendAgentMessage": (req: unknown) => sendAgentMessageMock(req),
      "FileSystemService.BeginFileUpload": (req: unknown) => beginMock(req),
      "FileSystemService.UploadFileChunk": (req: unknown) => chunkMock(req),
      "FileSystemService.CommitFileUpload": (req: unknown) => commitMock(req),
    }),
  ),
);
afterEach(() => setTransport(null));

function typeInComposer(text: string) {
  const editor = screen.getByTestId("chat-input");
  editor.textContent = text;
  fireEvent.input(editor);
  return editor;
}

function pressEnter(editor: HTMLElement): boolean {
  return fireEvent.keyDown(editor, {
    key: "Enter",
    shiftKey: false,
    metaKey: false,
    ctrlKey: false,
  });
}

const PLACED = `/home/me/.reliant/uploads/${CHAT_ID}/report.xlsx`;

function pickFile(file: File) {
  fireEvent.change(screen.getByTestId("attach-files-input"), { target: { files: [file] } });
}

describe("a file chat can't attach goes to the chat's machine", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAttachmentStore.getState().reset();
    for (const upload of useMachineUploadStore.getState().uploads) useMachineUploadStore.getState().remove(upload.id);
    beginMock.mockImplementation(() => ({
      uploadId: "0123abcd",
      chunkSize: 1 << 20,
      path: PLACED,
      maxSize: 2n << 30n,
      destination: UploadDestination.USER_UPLOADS,
    }));
    chunkMock.mockImplementation((req: { offset: bigint; data: Uint8Array }) => ({
      bytesReceived: req.offset + BigInt(req.data.length),
    }));
    commitMock.mockImplementation(() => ({ path: PLACED, absolutePath: PLACED, size: 5n, created: true }));
  });

  it("uploads it, shows a chip, and the sent message names its absolute path", async () => {
    let releaseCommit!: () => void;
    commitMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          releaseCommit = () => resolve({ path: PLACED, absolutePath: PLACED, size: 5n, created: true });
        }),
    );
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(<ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={false} />);

    pickFile(new File(["hello"], "report.xlsx", { type: "application/vnd.ms-excel" }));

    await waitFor(() => expect(commitMock).toHaveBeenCalledTimes(1));
    expect(beginMock).toHaveBeenCalledWith(
      expect.objectContaining({
        projectId: "project-1",
        chatId: CHAT_ID,
        filename: "report.xlsx",
        destination: UploadDestination.USER_UPLOADS,
        uploadsFolder: CHAT_ID,
      }),
    );
    expect(screen.getByTestId("machine-upload-chip").textContent).toContain("report.xlsx · Uploading…");
    expect(toastErrorMock).not.toHaveBeenCalled();

    // Send waits for the upload.
    const editor = typeInComposer("what is in this spreadsheet?");
    pressEnter(editor);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(onSend).not.toHaveBeenCalled();

    releaseCommit();
    await waitFor(() => expect(screen.getByTestId("machine-upload-chip").textContent).toContain("On the machine"));
    expect(screen.getByTestId("machine-upload-chip").getAttribute("title")).toBe(PLACED);

    pressEnter(editor);
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][0]).toBe(
      `what is in this spreadsheet?\n\nUser uploaded file: ${PLACED} (report.xlsx, 5 B, application/vnd.ms-excel)`,
    );
    expect(onSend.mock.calls[0][1]).toBeUndefined(); // no chat attachment
    await waitFor(() => expect(screen.queryByTestId("machine-upload-chip")).toBeNull());
  });

  it("a new chat, with no machine yet, says to start the chat first", async () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} isStreaming={false} />);
    const input = screen.getByTestId("attach-files-input");
    // A new chat's picker still offers only what chat attaches; a file that
    // gets through anyway (a drop, "All files") is not uploaded anywhere.
    expect(input.getAttribute("accept")).toBeTruthy();
    fireEvent.change(input, { target: { files: [new File(["x"], "data.zip", { type: "application/zip" })] } });

    await waitFor(() => expect(toastErrorMock).toHaveBeenCalledTimes(1));
    expect(toastErrorMock.mock.calls[0][0]).toContain("Send a message to start this chat, then add data.zip");
    expect(beginMock).not.toHaveBeenCalled();
    expect(screen.queryByTestId("machine-upload-chip")).toBeNull();
  });

  it("in a chat, the picker takes any file", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} />);
    expect(screen.getByTestId("attach-files-input").hasAttribute("accept")).toBe(false);
  });
});
