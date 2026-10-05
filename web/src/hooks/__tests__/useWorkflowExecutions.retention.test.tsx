import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// A chat's execution tree must outlive an unmount for as long as its messages
// do.
//
// The messages cache is gcTime: Infinity (message-queries.ts) — switching back
// to a chat renders its transcript instantly from cache. The execution tree is
// the ONLY source that tells InterleavedTimeline which thread is a spawned
// sub-agent once the chat's live thread records have been cleared (they are,
// on every unsubscribe). It used to inherit the QueryClient's 5-minute gcTime.
// So returning to a chat more than five minutes later rendered cached
// messages against NO tree: every spawn thread was unclassifiable, the
// timeline skipped it, and its messages vanished until the refetch landed.
// Measured in the dev log: every one of the error bursts
//   "[InterleavedTimeline] Thread has messages but no workflow row and no live
//    origin; cannot classify it"
// that was not a brand-new spawn began exactly on such a re-entry.
//
// These tests pin the retention, using the app's real QueryClient defaults.
// ---------------------------------------------------------------------------

const getWorkflowExecutionsMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/chat-grpc", () => ({
  chatGrpc: {
    getWorkflowExecutions: getWorkflowExecutionsMock,
  },
}));

import { useWorkflowExecutions } from "../useWorkflowExecutions";
import { chatDetailKeys } from "../chat-detail-keys";
import { WorkflowExecutionView } from "../../gen/reliant/v1/chat_pb";

const CHAT = "chat-retention";

function Reader({ chatId }: { chatId: string }) {
  const { data } = useWorkflowExecutions(chatId);
  return React.createElement("div", { "data-testid": "tree" }, data ? data.id : "none");
}

// The production defaults (lib/query-client.ts), not a test-friendly
// gcTime: 0 — the defect is precisely what those defaults do to this query.
function makeProductionLikeClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { staleTime: 30_000, gcTime: 5 * 60_000, retry: false },
    },
  });
}

beforeEach(() => {
  getWorkflowExecutionsMock.mockReset();
  getWorkflowExecutionsMock.mockResolvedValue({
    latest: { id: CHAT, thread: CHAT, children: [], steps: [] },
    all: [{ id: CHAT, thread: CHAT, children: [], steps: [] }],
  });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("useWorkflowExecutions retention", () => {
  it("keeps a chat's tree cached after the chat is closed for longer than the default gcTime", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const client = makeProductionLikeClient();
    const wrapper = ({ children }: { children: ReactNode }) =>
      React.createElement(QueryClientProvider, { client }, children);

    const view = render(React.createElement(Reader, { chatId: CHAT }), { wrapper });
    await waitFor(() => expect(view.getByTestId("tree").textContent).toBe(CHAT));

    // Navigate away: the only observer unmounts.
    view.unmount();

    // Come back after more than five minutes, as a user switching between
    // long-running chats routinely does.
    await act(async () => {
      vi.advanceTimersByTime(10 * 60_000);
    });

    const key = chatDetailKeys.workflowExecutionsView(CHAT, WorkflowExecutionView.BASIC);
    expect(client.getQueryData(key)).toBeDefined();
  });

  it("renders the cached tree on the first frame of a re-entry", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const client = makeProductionLikeClient();
    const wrapper = ({ children }: { children: ReactNode }) =>
      React.createElement(QueryClientProvider, { client }, children);

    const first = render(React.createElement(Reader, { chatId: CHAT }), { wrapper });
    await waitFor(() => expect(first.getByTestId("tree").textContent).toBe(CHAT));
    first.unmount();

    await act(async () => {
      vi.advanceTimersByTime(10 * 60_000);
    });

    // Hold the refetch open so the assertion below can only be satisfied by
    // the cache: the timeline renders cached messages synchronously, and the
    // tree has to be there in the same frame or spawn threads are dropped.
    getWorkflowExecutionsMock.mockReturnValue(new Promise(() => {}));
    const second = render(React.createElement(Reader, { chatId: CHAT }), { wrapper });
    expect(second.getByTestId("tree").textContent).toBe(CHAT);
  });
});
