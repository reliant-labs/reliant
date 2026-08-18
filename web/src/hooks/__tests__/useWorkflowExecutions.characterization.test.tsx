import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// Characterization tests for useWorkflowExecutions.
//
// These pin the CURRENT observable behavior of the hook so a refactor that
// homes the execution tree into React Query (Phase 1) can be proven
// behavior-preserving. They exercise only the PUBLIC contract — the returned
// shape and the fetch/refetch timing — never the internal module cache. That
// is deliberate: the same file must pass unchanged before and after the hook
// is reimplemented on top of useQuery, so it is wrapped in a QueryClientProvider
// from the start (the pre-refactor hook simply ignores the provider).
//
// DEAD-PATH PROOF (documented here so the finding travels with the tests):
// A grep of web/src confirms that the following stream-driven writes have NO
// component/hook/selector reader and are dead as of this commit —
//   * chatStore.nodeExecutions (written at chatStore.ts ~2546 from
//     node_execution stream updates; only re-read by its own reducer + tests,
//     never surfaced to the UI). NOT deleted — it becomes the Phase-2 source
//     of truth for node status.
//   * workflow_status handling (WorkflowStatusUpdate filtered at
//     chatStore.ts ~1826) is used only for a debug log; no state is derived.
//   * WorkflowExecutionUpdate / ExecutionLogUpdate (streaming-grpc.ts,
//     types/streaming.ts) are parsed but never consumed by any reader.
// The live path that actually drives the workflow UI is THIS hook's fetch of
// chatGrpc.getWorkflowExecutions + the "workflow_executions" refetch pulse.
// ---------------------------------------------------------------------------

// Minimal mock of the grpc module — the hook only touches
// chatGrpc.getWorkflowExecutions. Mocking the whole module also keeps the real
// grpc-client (and its supabase import) out of the test.
const getWorkflowExecutionsMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/chat-grpc", () => ({
  chatGrpc: {
    getWorkflowExecutions: getWorkflowExecutionsMock,
  },
}));

import { useWorkflowExecutions } from "../useWorkflowExecutions";
import { WorkflowState, WorkflowStopReason } from "../../gen/reliant/v1/chat_pb";
import { triggerRefetch } from "../../store/refetchStore";
import type { WorkflowExecutionData } from "../../api/chat-grpc";

// Build a minimal execution node — only `status` matters for the derived
// hasRunningWorkflow flag; the rest is cast away.
function wf(
  overrides: Partial<WorkflowExecutionData> = {},
): WorkflowExecutionData {
  return {
    id: "wf-1",
    state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED,
    children: [],
    steps: [],
    ...overrides,
  } as unknown as WorkflowExecutionData;
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
  return `chat-${uid}-${Math.random().toString(36).slice(2)}`;
}

beforeEach(() => {
  getWorkflowExecutionsMock.mockReset();
  getWorkflowExecutionsMock.mockResolvedValue({ latest: null, all: [] });
});

afterEach(() => {
  cleanup();
});

describe("useWorkflowExecutions (characterization)", () => {
  it("fetches the execution tree for a chat and exposes latest + all", async () => {
    const chatId = nextChatId();
    const latest = wf({ id: "root" });
    const all = [latest];
    getWorkflowExecutionsMock.mockResolvedValue({ latest, all });

    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWorkflowExecutions(chatId), {
      wrapper: Wrapper,
    });

    await waitFor(() => expect(result.current.allWorkflows).toHaveLength(1));

    expect(getWorkflowExecutionsMock).toHaveBeenCalledWith(chatId);
    expect(result.current.data).toEqual(latest);
    expect(result.current.allWorkflows).toEqual(all);
  });

  it("does not fetch when chatId is null", async () => {
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWorkflowExecutions(null), {
      wrapper: Wrapper,
    });

    // Give any async work a chance to run, then assert no fetch happened.
    await new Promise((r) => setTimeout(r, 10));
    expect(getWorkflowExecutionsMock).not.toHaveBeenCalled();
    expect(result.current.allWorkflows).toEqual([]);
    expect(result.current.data).toBeNull();
  });

  it("derives hasRunningWorkflow=true when any workflow is RUNNING", async () => {
    const chatId = nextChatId();
    const running = wf({ id: "r", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED });
    getWorkflowExecutionsMock.mockResolvedValue({
      latest: running,
      all: [wf({ id: "done" }), running],
    });

    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWorkflowExecutions(chatId), {
      wrapper: Wrapper,
    });

    await waitFor(() =>
      expect(result.current.hasRunningWorkflow).toBe(true),
    );
  });

  it("derives hasRunningWorkflow=false when no workflow is RUNNING", async () => {
    const chatId = nextChatId();
    getWorkflowExecutionsMock.mockResolvedValue({
      latest: wf({ id: "done", state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED }),
      all: [wf({ id: "done", state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED })],
    });

    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWorkflowExecutions(chatId), {
      wrapper: Wrapper,
    });

    await waitFor(() => expect(result.current.allWorkflows).toHaveLength(1));
    expect(result.current.hasRunningWorkflow).toBe(false);
  });

  it("re-fetches when a workflow_executions refetch pulse fires", async () => {
    const chatId = nextChatId();
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWorkflowExecutions(chatId), {
      wrapper: Wrapper,
    });

    await waitFor(() =>
      expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(1),
    );

    // A pulse from the chat stream should drive a second fetch. The refetch
    // store debounces ~300ms, so allow generous slack.
    triggerRefetch("workflow_executions");

    await waitFor(
      () => expect(getWorkflowExecutionsMock).toHaveBeenCalledTimes(2),
      { timeout: 2000 },
    );

    // Still the same chat — the pulse refetches in place, it does not change id.
    expect(getWorkflowExecutionsMock).toHaveBeenLastCalledWith(chatId);
    expect(result.current.error).toBeNull();
  });
});
