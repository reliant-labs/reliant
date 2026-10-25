// Copyright (c) 2025 Reliant Labs

/**
 * RunService reads and writes share the generated key scope. A run control
 * (pause, resume, stop) refetches the mounted run reads, and so does a
 * refused one (the run usually moved first); adopting a run, a ChatService
 * write, refetches them too; and every run read the Runs surface mounts — the
 * paged list, the children strip, the live pill, the Library's last-run
 * column — sits under `runServiceKeys.all`, the one key the update stream
 * invalidates.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  useAdoptRun,
  useChildRuns,
  useLastRunPerWorkflow,
  useLiveRuns,
  useRunControl,
  useRunList,
} from "../run-queries";
import { runServiceKeys } from "../run-service-hooks_gen";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

/** A server holding one live scheduled run, "chat-1", that the controls move. */
function runTransport(options: { refuseControls?: boolean } = {}) {
  let displayState = RunDisplayState.RUNNING;
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const run = () => ({
    id: "wf-1",
    sessionId: "chat-1",
    title: "Nightly",
    workflowName: "triage",
    launchKind: "schedule",
    displayState,
  });
  const control = (name: string, next: RunDisplayState) => () => {
    count(name);
    displayState = next;
    if (options.refuseControls) throw new ConnectError("the run already finished", Code.FailedPrecondition);
    return {};
  };
  const transport = mockTransport({
    "RunService.ListRuns": () => {
      count("ListRuns");
      return { runs: [run()], nextPageToken: "" };
    },
    "RunService.LastRunPerWorkflow": () => {
      count("LastRunPerWorkflow");
      return { runs: [run()] };
    },
    "RunService.PauseRun": control("PauseRun", RunDisplayState.PAUSED),
    "RunService.ResumeRun": control("ResumeRun", RunDisplayState.RUNNING),
    "RunService.CancelRun": control("CancelRun", RunDisplayState.CANCELLED),
    "ChatService.AdoptChat": () => {
      count("AdoptChat");
      return { chat: { id: "chat-1", launchKind: "schedule", adoptedAt: "2026-10-24T00:00:00Z" } };
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0 };
}

describe("RunService keys", () => {
  it("pausing a run refetches the mounted run list", async () => {
    const { transport, calls } = runTransport();
    setTransport(transport);

    const list = renderHook(() => useRunList({ kind: ["schedule"] }), { wrapper });
    await waitFor(() => expect(list.result.current.runs[0]?.displayState).toBe(RunDisplayState.RUNNING));

    const control = renderHook(() => useRunControl(), { wrapper });
    act(() => control.result.current.mutate({ chatId: "chat-1", action: "pause" }));

    await waitFor(() => expect(list.result.current.runs[0]?.displayState).toBe(RunDisplayState.PAUSED));
    expect(calls("PauseRun")).toBe(1);
    expect(calls("ListRuns")).toBe(2);
  });

  it("a refused stop still refetches, and reports the error to the caller", async () => {
    const { transport, calls } = runTransport({ refuseControls: true });
    setTransport(transport);

    const live = renderHook(() => useLiveRuns(), { wrapper });
    await waitFor(() => expect(live.result.current.data?.runs).toHaveLength(1));

    const control = renderHook(() => useRunControl(), { wrapper });
    const errors: Error[] = [];
    act(() =>
      control.result.current.mutate(
        { chatId: "chat-1", action: "stop" },
        { onError: (error) => errors.push(error) },
      ),
    );

    await waitFor(() => expect(errors).toHaveLength(1));
    await waitFor(() => expect(live.result.current.data?.runs[0]?.displayState).toBe(RunDisplayState.CANCELLED));
    expect(calls("CancelRun")).toBe(1);
    expect(calls("ListRuns")).toBe(2);
  });

  it("adopting a run (a ChatService write) refetches the run reads", async () => {
    const { transport, calls } = runTransport();
    setTransport(transport);

    const lastRuns = renderHook(() => useLastRunPerWorkflow("proj-1"), { wrapper });
    await waitFor(() => expect(lastRuns.result.current.data?.get("triage")?.chatId).toBe("chat-1"));

    const adopt = renderHook(() => useAdoptRun(), { wrapper });
    await act(async () => {
      await adopt.result.current.mutateAsync({ chatId: "chat-1" });
    });

    await waitFor(() => expect(calls("LastRunPerWorkflow")).toBe(2));
    expect(calls("AdoptChat")).toBe(1);
  });

  it("invalidating runServiceKeys.all — what the update stream does — refetches every run read", async () => {
    const { transport, calls } = runTransport();
    setTransport(transport);

    const list = renderHook(() => useRunList({}), { wrapper });
    const children = renderHook(() => useChildRuns("parent-1"), { wrapper });
    const live = renderHook(() => useLiveRuns(), { wrapper });
    const lastRuns = renderHook(() => useLastRunPerWorkflow("proj-1"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.runs).toHaveLength(1);
      expect(children.result.current.data?.runs).toHaveLength(1);
      expect(live.result.current.data?.runs).toHaveLength(1);
      expect(lastRuns.result.current.data?.size).toBe(1);
    });
    expect(calls("ListRuns")).toBe(3);
    expect(calls("LastRunPerWorkflow")).toBe(1);

    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: runServiceKeys.all });
    });

    expect(calls("ListRuns")).toBe(6);
    expect(calls("LastRunPerWorkflow")).toBe(2);
  });
});
