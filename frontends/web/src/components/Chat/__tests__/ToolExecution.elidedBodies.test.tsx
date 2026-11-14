import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus } from "../../../gen/reliant/v1/chat_pb";
import { queryClient } from "../../../lib/query-client";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { installRecordingTracer } from "../../../test/recordingTracer";

// A chat opened with stub tool bodies (lib/toolBodies.ts) holds a finished
// call's input as a skeleton and its result as a stub. The row labels itself
// from them and fetches nothing while collapsed. Open, it fetches the bodies
// by block id and renders them; until they arrive it shows a placeholder,
// never the skeleton as if it were the input.

const getContentBlockBodies = vi.fn();
const collapsed = vi.hoisted(() => ({ value: true }));
const openChat = vi.hoisted(() => ({ value: undefined as { viewerAccess?: boolean } | undefined }));

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => openChat.value,
  useChatMessages: () => [],
  useStreamingMessages: () => [],
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({
  useTasksForChat: () => ({ data: undefined }),
}));
vi.mock("../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));
vi.mock("../../Settings/ToolCallSettings", () => ({
  shouldToolBeCollapsed: () => collapsed.value,
  TOOL_COLLAPSE_SETTINGS_EVENT: "tool-collapse-settings",
}));

const FULL_INPUT = JSON.stringify({ query: "find the totals", payload: "FULL-BODY ".repeat(200) });
const SKELETON = JSON.stringify({ query: "find the totals", payload: "SKELETON-CUT…" });
const FULL_OUTPUT = "FULL-OUTPUT ".repeat(150);

type BodyRequest = { chatId: string; blockIds: string[]; full?: boolean };

function serveBodies(overrides: Record<string, { content?: string; truncated?: boolean; originalSize?: bigint }> = {}) {
  getContentBlockBodies.mockImplementation(async (request: BodyRequest) => ({
    bodies: request.blockIds.map((id) =>
      id.startsWith("call-")
        ? { blockId: id, input: FULL_INPUT }
        : { blockId: id, content: request.full ? `WHOLE ${id}` : FULL_OUTPUT, ...(request.full ? {} : overrides[id]) },
    ),
    notFound: [],
    deferred: [],
  }));
}

function renderRow(id = "1", { chatId = "chat-1" }: { chatId?: string } = {}) {
  return (
    <ToolExecution
      key={id}
      toolCall={{
        id: `tc-${id}`,
        name: "lookup_records",
        input: JSON.parse(SKELETON),
        finished: true,
        content_block_id: `call-${id}`,
        inputElided: true,
        inputSize: FULL_INPUT.length,
        durableStatus: ToolCallStatus.COMPLETED,
      }}
      toolResult={{
        name: "lookup_records",
        content: "STUB-OUTPUT",
        is_error: false,
        truncated: true,
        original_size: FULL_OUTPUT.length,
        elided: true,
        block_id: `res-${id}`,
      }}
      chatId={chatId}
    />
  );
}

const toggle = () => screen.getByRole("button", { name: /toggle tool details/i });

describe("a tool row whose bodies the transcript left out", () => {
  beforeEach(() => {
    collapsed.value = true;
    openChat.value = undefined;
    queryClient.clear();
    getContentBlockBodies.mockReset();
    serveBodies();
    setTransport(mockTransport({ "ChatService.GetContentBlockBodies": (req: unknown) => getContentBlockBodies(req) }));
  });
  afterEach(() => {
    setTransport(null);
    queryClient.clear();
  });

  it("labels itself from the skeleton and fetches nothing while collapsed", async () => {
    render(renderRow());
    expect(screen.getByText(/find the totals/)).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 80));
    expect(getContentBlockBodies).not.toHaveBeenCalled();
  });

  it("fetches both bodies once when expanded, showing a placeholder until they arrive", async () => {
    let answer: () => void = () => {};
    getContentBlockBodies.mockImplementationOnce(
      (request: BodyRequest) =>
        new Promise((resolve) => {
          answer = () =>
            resolve({
              bodies: [
                { blockId: "call-1", input: FULL_INPUT },
                { blockId: "res-1", content: FULL_OUTPUT },
              ],
              notFound: [],
              deferred: [],
            });
          void request;
        }),
    );
    render(renderRow());
    fireEvent.click(toggle());

    expect(screen.getByTestId("tool-body-placeholder")).toBeInTheDocument();
    // Never the skeleton or the stub as content.
    expect(screen.queryByText(/SKELETON-CUT/)).toBeNull();
    expect(screen.queryByText(/STUB-OUTPUT/)).toBeNull();

    await waitFor(() => expect(getContentBlockBodies).toHaveBeenCalledTimes(1));
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ chatId: "chat-1", blockIds: ["call-1", "res-1"] });
    await act(async () => answer());

    await waitFor(() => expect(screen.queryByTestId("tool-body-placeholder")).toBeNull());
    expect(screen.getAllByText(/FULL-BODY/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/FULL-OUTPUT/).length).toBeGreaterThan(0);
    expect(screen.queryByText(/SKELETON-CUT/)).toBeNull();

    // Collapse and reopen: the bodies are cached.
    fireEvent.click(toggle());
    fireEvent.click(toggle());
    expect(screen.queryByTestId("tool-body-placeholder")).toBeNull();
    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
  });

  it("fetches the bodies of rows expanded by default on mount, in one batch", async () => {
    collapsed.value = false;
    render(
      <>
        {renderRow("1")}
        {renderRow("2")}
      </>,
    );
    await waitFor(() => expect(screen.queryAllByTestId("tool-body-placeholder")).toHaveLength(0));
    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
    expect(getContentBlockBodies.mock.calls[0][0].blockIds.sort()).toEqual(["call-1", "call-2", "res-1", "res-2"]);
  });

  it("says it could not load the details, and retries on request", async () => {
    getContentBlockBodies.mockRejectedValueOnce(new Error("unavailable")).mockRejectedValueOnce(new Error("unavailable"));
    render(renderRow());
    fireEvent.click(toggle());

    expect(await screen.findByTestId("tool-body-error", undefined, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByText("Couldn't load this call's details.")).toBeInTheDocument();
    expect(screen.queryByText(/SKELETON-CUT/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByTestId("tool-body-error")).toBeNull());
    expect(await screen.findAllByText(/FULL-BODY/)).not.toHaveLength(0);
  });

  it("shows a viewer of a shared chat the input and says the result stays with the owner", async () => {
    // The server elides every result for a viewer and answers its body as
    // not found: the row must not ask for it, nor call that a failure.
    openChat.value = { viewerAccess: true };
    render(renderRow());
    fireEvent.click(toggle());

    expect(await screen.findByTestId("tool-result-withheld")).toHaveTextContent("The result stays on the owner's machine.");
    expect(screen.getAllByText(/FULL-BODY/).length).toBeGreaterThan(0);
    expect(screen.queryByTestId("tool-body-error")).toBeNull();
    expect(screen.queryByText(/STUB-OUTPUT/)).toBeNull();
    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ blockIds: ["call-1"] });
  });

  it("never shows a viewer a result that arrived whole in the transcript", async () => {
    // Not relying on the server's elision: an inline body is withheld too,
    // and nothing is fetched for it.
    openChat.value = { viewerAccess: true };
    collapsed.value = false;
    render(
      <ToolExecution
        toolCall={{ id: "tc-inline", name: "shell", input: { command: "env" }, finished: true, durableStatus: ToolCallStatus.COMPLETED }}
        toolResult={{ name: "shell", content: "SECRET=hunter2", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(await screen.findByTestId("tool-result-withheld")).toBeInTheDocument();
    expect(screen.queryByText(/hunter2/)).toBeNull();
    expect(screen.queryByTestId("tool-body-error")).toBeNull();
    expect(getContentBlockBodies).not.toHaveBeenCalled();
  });

  it("shows a large output's display body first, then the whole output on request", async () => {
    serveBodies({ "res-1": { content: "HEAD … TAIL", truncated: true, originalSize: 120_000n } });
    render(renderRow());
    fireEvent.click(toggle());

    expect(await screen.findByRole("button", { name: "Show full output (120 KB)" })).toBeInTheDocument();
    expect(screen.getByText(/HEAD … TAIL/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /show full output/i }));
    await waitFor(() => expect(screen.queryByTestId("tool-output-truncated")).toBeNull());
    expect(screen.getByText(/WHOLE res-1/)).toBeInTheDocument();
    const fullRequest = getContentBlockBodies.mock.calls.map((call) => call[0]).find((req: BodyRequest) => req.full);
    expect(fullRequest).toMatchObject({ blockIds: ["res-1"], full: true });
  });

  it("records the expand as a tool_body.expand interaction, from the click to the body painted", async () => {
    const tracer = installRecordingTracer();
    try {
      render(renderRow());
      fireEvent.click(toggle());
      await waitFor(() => expect(screen.queryByTestId("tool-body-placeholder")).toBeNull());

      await waitFor(() => {
        const span = tracer.spans.find((s) => s.name === "tool_body.expand");
        expect(span?.ended).toBe(true);
      });
      const span = tracer.spans.find((s) => s.name === "tool_body.expand")!;
      expect(span.attributes).toMatchObject({
        "chat.id": "chat-1",
        "tool_body.blocks": 2,
        "tool_body.bytes": FULL_INPUT.length + FULL_OUTPUT.length,
        "tool_body.cache": "miss",
        "tool_body.deferred": false,
        "interaction.outcome": "ok",
      });
    } finally {
      tracer.uninstall();
    }
  });

  it("renders a row sent whole without fetching", async () => {
    collapsed.value = false;
    render(
      <ToolExecution
        toolCall={{ id: "tc-w", name: "lookup_records", input: JSON.parse(FULL_INPUT), finished: true, content_block_id: "call-w", durableStatus: ToolCallStatus.COMPLETED }}
        toolResult={{ name: "lookup_records", content: "small output", block_id: "res-w" }}
        chatId="chat-1"
      />,
    );
    expect(screen.getByText(/small output/)).toBeInTheDocument();
    expect(screen.queryByTestId("tool-body-placeholder")).toBeNull();
    await new Promise((r) => setTimeout(r, 80));
    expect(getContentBlockBodies).not.toHaveBeenCalled();
  });
});
