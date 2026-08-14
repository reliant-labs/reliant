import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// One chat's execution tree is read by many components at once: ChatContainer,
// every ToolExecutionGroup, and every ToolExecution row in the transcript. They
// share a query cache entry, so the INITIAL load is already one request.
//
// The refetch pulse was the hole. Each hook instance held its own
// "workflow_executions" subscription, so one pulse ran N invalidateQueries
// calls in the same tick — and invalidateQueries defaults to
// cancelRefetch: true, which aborts the in-flight fetch and starts a fresh one
// instead of joining it. N mounted readers therefore turned one pulse into N
// requests on the wire, all inside a millisecond or two.
//
// These tests pin the fan-out at one request per pulse regardless of how many
// components read the hook.
// ---------------------------------------------------------------------------

const getWorkflowExecutionsMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/chat-grpc", () => ({
  chatGrpc: {
    getWorkflowExecutions: getWorkflowExecutionsMock,
  },
}));

import { useWorkflowExecutions } from "../useWorkflowExecutions";
import { triggerRefetch } from "../../store/refetchStore";

function Reader({ chatId }: { chatId: string }) {
  const { allWorkflows } = useWorkflowExecutions(chatId);
  return React.createElement("div", null, String(allWorkflows.length));
}

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) =>
    React.createElement(QueryClientProvider, { client }, children);
  return { Wrapper, client };
}

let uid = 0;
function nextChatId(): string {
  uid += 1;
  return `chat-dedupe-${uid}-${Math.random().toString(36).slice(2)}`;
}

beforeEach(() => {
  getWorkflowExecutionsMock.mockReset();
  getWorkflowExecutionsMock.mockResolvedValue({ latest: null, all: [] });
});

afterEach(() => {
  cleanup();
});

describe("useWorkflowExecutions request fan-out", () => {
  it("mounting eight readers for one chat issues a single request", async () => {
    const chatId = nextChatId();
    const { Wrapper } = makeWrapper();

    render(
      React.createElement(
        Wrapper,
        null,
        ...Array.from({ length: 8 }, (_, i) =>
          React.createElement(Reader, { key: i, chatId }),
        ),
      ),
    );

    await waitFor(() =>
      expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(1),
    );
  });

  it("one refetch pulse issues one request, not one per mounted reader", async () => {
    const chatId = nextChatId();
    const { Wrapper } = makeWrapper();

    // Hold the pulse-driven fetch open. The duplicate storm only shows up
    // while a request is in flight: that is precisely when cancelRefetch
    // aborts and restarts instead of joining.
    let releaseSecond: (() => void) | undefined;
    getWorkflowExecutionsMock
      .mockResolvedValueOnce({ latest: null, all: [] })
      .mockImplementation(
        () =>
          new Promise((resolve) => {
            releaseSecond = () => resolve({ latest: null, all: [] });
          }),
      );

    render(
      React.createElement(
        Wrapper,
        null,
        ...Array.from({ length: 8 }, (_, i) =>
          React.createElement(Reader, { key: i, chatId }),
        ),
      ),
    );

    await waitFor(() =>
      expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(1),
    );

    triggerRefetch("workflow_executions");

    await waitFor(
      () => expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(2),
      { timeout: 2000 },
    );

    // Give every other subscriber the chance it used to take to pile on.
    await new Promise((r) => setTimeout(r, 50));
    expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(2);

    releaseSecond?.();
  });

  it("keeps refetching on later pulses after readers unmount and remount", async () => {
    const chatId = nextChatId();
    const { Wrapper } = makeWrapper();

    const { unmount } = render(
      React.createElement(Wrapper, null, React.createElement(Reader, { chatId })),
    );
    await waitFor(() =>
      expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(1),
    );
    unmount();

    // A fresh provider stands in for the reader coming back on a new mount.
    const { Wrapper: Wrapper2 } = makeWrapper();
    render(
      React.createElement(Wrapper2, null, React.createElement(Reader, { chatId })),
    );
    await waitFor(() =>
      expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(2),
    );

    triggerRefetch("workflow_executions");

    await waitFor(
      () => expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(3),
      { timeout: 2000 },
    );
  });
});
