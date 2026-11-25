// Copyright (c) 2026 Reliant Labs

/**
 * The Trace view over a mock ChatTraceService: loading, the owner's whole
 * answer, a viewer's (no dollars, even if the server sent some), PARTIAL
 * with its reasons, empty, NotFound for an outsider, and an outage with a
 * retry.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { ChatTracePartialReason, type GetChatTraceRequest, type GetChatTraceResponse } from "@/gen/reliant/v1/chat_trace_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { ChatTracePanel, chatTraceRetry } from "../ChatTracePanel";
import { emptyTrace, ownerTrace, partialTrace, viewerTrace } from "./fixtures";

// jsdom has no layout, so the waterfall's virtualizer would draw no rows: draw them all.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count, getItemKey }: { count: number; getItemKey: (index: number) => unknown }) => ({
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, key: getItemKey(index), start: index * 28 })),
    getTotalSize: () => count * 28,
    scrollToIndex: () => {},
  }),
}));

let queryClient: QueryClient;

beforeEach(() => {
  chatTraceRetry.delayMs = () => 0;
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0, gcTime: 0 } } });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function serve(answer: (req: GetChatTraceRequest) => GetChatTraceResponse | Promise<GetChatTraceResponse>) {
  const handler = vi.fn(answer);
  setTransport(mockTransport({ "ChatTraceService.GetChatTrace": handler as never }));
  return handler;
}

function fail(code: Code) {
  const handler = vi.fn(() => {
    throw new ConnectError("nope", code);
  });
  setTransport(mockTransport({ "ChatTraceService.GetChatTrace": handler as never }));
  return handler;
}

function renderPanel(viewerChat = false) {
  return render(<ChatTracePanel chatId="chat-1" viewerChat={viewerChat} />, { wrapper });
}

describe("ChatTracePanel", () => {
  it("says it is loading until the trace arrives", async () => {
    let release: (value: GetChatTraceResponse) => void = () => {};
    const handler = serve(() => new Promise<GetChatTraceResponse>((resolve) => (release = resolve)));
    renderPanel();
    expect(screen.getByTestId("chat-trace-loading")).toHaveTextContent("Loading this chat's trace");
    await waitFor(() => expect(handler).toHaveBeenCalled());
    release(ownerTrace());
    expect(await screen.findByTestId("chat-trace-report")).toBeInTheDocument();
  });

  it("asks for this chat's trace", async () => {
    const handler = serve(() => ownerTrace());
    renderPanel();
    await screen.findByTestId("chat-trace-report");
    expect(handler.mock.calls[0][0].chatId).toBe("chat-1");
  });

  it("gives the owner the waterfall, the cost summary and every model call with its cost", async () => {
    serve(() => ownerTrace());
    renderPanel();
    const report = await screen.findByTestId("chat-trace-report");
    expect(report).toHaveAttribute("data-viewer", "false");
    expect(screen.getByTestId("trace-waterfall-summary")).toHaveTextContent("5 spans · 1 error");
    expect(screen.getByTestId("chat-trace-cost-reported")).toHaveTextContent("$0.0123");
    expect(screen.getByTestId("chat-trace-cost-estimated")).toHaveTextContent("~$0.0020");
    expect(screen.getAllByTestId("chat-trace-call-cost")).toHaveLength(3);
    expect(screen.queryByTestId("chat-trace-partial")).not.toBeInTheDocument();
  });

  it("gives a viewer structure, timing, models and tokens, and no dollars", async () => {
    serve(() => viewerTrace());
    renderPanel(true);
    const report = await screen.findByTestId("chat-trace-report");
    expect(report).toHaveAttribute("data-viewer", "true");
    expect(screen.getByTestId("trace-waterfall")).toBeInTheDocument();
    expect(screen.getByText("gpt-5-mini")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-trace-cost")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-trace-viewer-note")).toBeInTheDocument();
    expect(report.textContent).not.toMatch(/\$/);
  });

  it("keeps dollars from a viewer of the chat even if the answer was in the owner's shape", async () => {
    serve(() => ownerTrace());
    renderPanel(true);
    const report = await screen.findByTestId("chat-trace-report");
    expect(report).toHaveAttribute("data-viewer", "true");
    expect(report.textContent).not.toMatch(/\$/);
  });

  it("takes the viewer shape from the answer too", async () => {
    serve(() => viewerTrace());
    renderPanel(false);
    expect(await screen.findByTestId("chat-trace-report")).toHaveAttribute("data-viewer", "true");
  });

  it("names a partial trace's reasons in plain words and still lists the model calls", async () => {
    serve(() =>
      partialTrace(
        [ChatTracePartialReason.TRACE_EXPIRED, ChatTracePartialReason.CALLS_TRUNCATED],
        ownerTrace({ spans: [] }),
      ),
    );
    renderPanel();
    const banner = await screen.findByTestId("chat-trace-partial");
    expect(banner).toHaveTextContent("Partial trace");
    expect(within(banner).getByText(/older than 7 days has expired/)).toBeInTheDocument();
    expect(within(banner).getByText(/only the earliest are listed/)).toBeInTheDocument();
    expect(screen.getByTestId("chat-trace-no-spans")).toHaveTextContent("No timing to draw for this window.");
    expect(screen.getAllByTestId("chat-trace-call-cost")).toHaveLength(3);
  });

  it("says when the span store could not be read, never an empty chart", async () => {
    serve(() => partialTrace([ChatTracePartialReason.SPANS_UNAVAILABLE], ownerTrace({ spans: [] })));
    renderPanel();
    expect(await screen.findByText(/Timing couldn't be loaded right now/)).toBeInTheDocument();
    expect(screen.queryByTestId("trace-waterfall")).not.toBeInTheDocument();
  });

  it("marks a truncated waterfall as longer than shown", async () => {
    serve(() => partialTrace([ChatTracePartialReason.SPANS_TRUNCATED]));
    renderPanel();
    expect(await screen.findByTestId("trace-waterfall-summary")).toHaveTextContent("the trace is longer than shown");
  });

  it("says a chat with no model calls has nothing yet", async () => {
    serve(() => emptyTrace());
    renderPanel();
    expect(await screen.findByTestId("chat-trace-empty")).toHaveTextContent("No model calls yet");
    expect(screen.queryByTestId("trace-waterfall")).not.toBeInTheDocument();
  });

  it("tells an outsider the trace is not available, without a retry", async () => {
    const handler = fail(Code.NotFound);
    renderPanel();
    expect(await screen.findByTestId("chat-trace-unavailable")).toHaveTextContent("isn't available to you");
    expect(screen.queryByRole("button", { name: /try again|refresh/i })).not.toBeInTheDocument();
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("offers a retry when the read fails", async () => {
    let calls = 0;
    setTransport(
      mockTransport({
        "ChatTraceService.GetChatTrace": (() => {
          calls += 1;
          if (calls <= 3) throw new ConnectError("down", Code.Unavailable);
          return ownerTrace();
        }) as never,
      }),
    );
    renderPanel();
    const alert = await screen.findByTestId("chat-trace-error");
    expect(calls).toBe(3);
    expect(alert).toHaveTextContent("Couldn't load this chat's trace");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByTestId("chat-trace-report")).toBeInTheDocument();
  });

  it("shows the selected step's facts, with cost for the owner only", async () => {
    serve(() => ownerTrace());
    renderPanel();
    await screen.findByTestId("chat-trace-report");
    const rows = screen.getAllByTestId("trace-span-row");
    expect(rows.map((row) => row.getAttribute("data-span-id"))).toEqual(["s-run", "s-node", "s-turn", "s-chat", "s-tool"]);
    fireEvent.click(rows.find((row) => row.getAttribute("data-span-id") === "s-chat")!);
    const facts = screen.getByTestId("chat-trace-span-facts");
    expect(facts).toHaveTextContent("claude-sonnet-5");
    expect(facts).toHaveTextContent("1,200");
    expect(facts).toHaveTextContent("$0.0123");
  });

  it("shows a viewer the selected step's model, tool and tokens, and no cost", async () => {
    serve(() => ownerTrace());
    renderPanel(true);
    await screen.findByTestId("chat-trace-report");
    const rows = screen.getAllByTestId("trace-span-row");
    fireEvent.click(rows.find((row) => row.getAttribute("data-span-id") === "s-chat")!);
    const facts = screen.getByTestId("chat-trace-span-facts");
    expect(facts).toHaveTextContent("claude-sonnet-5");
    expect(facts.textContent).not.toMatch(/\$/);
    fireEvent.click(rows.find((row) => row.getAttribute("data-span-id") === "s-tool")!);
    expect(screen.getByTestId("chat-trace-span-facts")).toHaveTextContent("shell");
    expect(screen.getByTestId("chat-trace-span-facts")).toHaveTextContent("Failed");
  });
});
