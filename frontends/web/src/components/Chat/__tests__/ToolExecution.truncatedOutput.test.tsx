import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ToolCallStatus } from "../../../gen/reliant/v1/chat_pb";
import { queryClient } from "../../../lib/query-client";

// A large tool result reaches the client as a preview (truncated, with its
// full size). The card says so, fetches the whole output on request — once,
// cached per tool call — and renders it in the preview's place; a copy puts
// the full output on the clipboard, never the preview.

const getToolResult = vi.fn();

// The RPC is the only thing faked: the generated query entry keeps its real
// key, so caching and sharing behave as in the app.
vi.mock("../../../hooks/chat-service-hooks_gen", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../hooks/chat-service-hooks_gen")>();
  return {
    ...actual,
    chatServiceQueries: {
      ...actual.chatServiceQueries,
      getToolResult: (req: { chatId: string; toolCallId: string }, opts?: object) => ({
        ...actual.chatServiceQueries.getToolResult(req, opts),
        queryFn: () => getToolResult(req.chatId, req.toolCallId),
      }),
    },
  };
});
vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
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
  // Every card starts expanded, so its output is on screen.
  shouldToolBeCollapsed: () => false,
  TOOL_COLLAPSE_SETTINGS_EVENT: "tool-collapse-settings",
}));

const PREVIEW = "head of the log\n… 120 KB omitted …\ntail of the log";
const FULL = "head of the log\n" + "middle line\n".repeat(1000) + "tail of the log";
// What the server reports as the full size (the label reads it, not FULL).
const ORIGINAL_SIZE = 120_000;

function renderCard(truncated: boolean, toolCallId = "call-big") {
  return render(
    <ToolExecution
      toolCall={{
        id: toolCallId,
        name: "lookup_records",
        input: { url: "https://example.com/big" },
        finished: true,
        durableStatus: ToolCallStatus.COMPLETED,
      }}
      toolResult={{
        name: "lookup_records",
        content: truncated ? PREVIEW : "small output",
        is_error: false,
        truncated: truncated || undefined,
        original_size: truncated ? ORIGINAL_SIZE : undefined,
      }}
      chatId="chat-1"
    />,
  );
}

describe("a truncated tool output", () => {
  beforeEach(() => {
    getToolResult.mockReset();
    getToolResult.mockResolvedValue({ toolCallId: "call-big", content: FULL, isError: false, size: FULL.length });
    queryClient.clear();
  });
  afterEach(() => {
    queryClient.clear();
  });

  it("shows the preview with a Show full output affordance naming the size", () => {
    renderCard(true);
    expect(screen.getByText(/head of the log/)).toBeInTheDocument();
    expect(screen.getByTestId("tool-output-truncated")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show full output (120 KB)" })).toBeInTheDocument();
    expect(getToolResult).not.toHaveBeenCalled();
  });

  it("fetches the full output on request and renders it in place of the preview", async () => {
    renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: /show full output/i }));

    await waitFor(() => expect(screen.queryByTestId("tool-output-truncated")).toBeNull());
    expect(getToolResult).toHaveBeenCalledTimes(1);
    expect(getToolResult).toHaveBeenCalledWith("chat-1", "call-big");
    expect(screen.queryByText(/omitted/)).toBeNull();
  });

  it("reuses the fetched output: a second card for the same call does not refetch", async () => {
    const first = renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: /show full output/i }));
    await waitFor(() => expect(screen.queryByTestId("tool-output-truncated")).toBeNull());
    first.unmount();

    renderCard(true);
    // Already cached: rendered full straight away, no affordance, no fetch.
    expect(screen.queryByTestId("tool-output-truncated")).toBeNull();
    expect(getToolResult).toHaveBeenCalledTimes(1);
  });

  it("copies the full output, not the preview", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: "Copy to clipboard" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(FULL));
    expect(getToolResult).toHaveBeenCalledTimes(1);
  });

  it("offers a retry when the fetch fails", async () => {
    getToolResult.mockReset();
    getToolResult.mockRejectedValue(new Error("unavailable"));
    renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: /show full output/i }));

    expect(await screen.findByRole("button", { name: "Retry" }, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByText("Couldn't load the full output.")).toBeInTheDocument();
  });

  it("shows no affordance for an output sent whole", () => {
    renderCard(false, "call-small");
    expect(screen.getByText("small output")).toBeInTheDocument();
    expect(screen.queryByTestId("tool-output-truncated")).toBeNull();
  });
});
