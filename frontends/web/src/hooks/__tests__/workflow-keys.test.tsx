/**
 * WorkflowService reads and writes share the generated key scope, so no
 * workflow surface (the composer's picker, the Library, the builder, the
 * automation form) is left stale:
 *
 *  - every write refetches every mounted workflow list before it resolves,
 *    so a caller that navigates to what it just saved finds it;
 *  - the chat-safe list follows the current project;
 *  - the stream's WORKFLOW_DRAFT_UPDATED (an agent or another tab wrote a
 *    draft) invalidates workflowServiceKeys.all, and the preset lists the
 *    server matches against a workflow's inputs;
 *  - an editor load bypasses the cache (its version gates the next save),
 *    a lookup does not;
 *  - validating a canvas is a read: it invalidates nothing.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { EntityType, UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "@/types/streaming";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { queryClient } from "@/lib/query-client";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { useProjectStore } from "@/store/projectStore";
import { subscribeToDraftUpdates } from "@/store/workflowDraftUpdates";
import { presetServiceKeys } from "../preset-service-hooks_gen";
import { triggerServiceKeys } from "../trigger-service-hooks_gen";
import { useWorkflowLibrary } from "../workflow-library-queries";
import {
  deleteWorkflow,
  fetchWorkflow,
  saveWorkflow,
  useWorkflowDefinitionQuery,
  useWorkflows,
  validateWorkflow,
} from "../workflow-queries";
import { workflowServiceKeys } from "../workflow-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type Row = { name: string; filename: string; source: string; isHidden?: boolean };
let server: Record<string, Row[]>;
let version: number;
const calls: Record<string, number> = {};
const count = (rpc: string) => {
  calls[rpc] = (calls[rpc] ?? 0) + 1;
};

function selectProject(id: string) {
  useProjectStore.setState({ currentProject: { id, name: id } as never });
}

beforeEach(() => {
  server = {
    p1: [
      { name: "builtin://agent", filename: "agent", source: "builtin" },
      { name: "helper", filename: "helper", source: "user", isHidden: true },
    ],
    p2: [{ name: "builtin://plan", filename: "plan", source: "builtin" }],
  };
  version = 1;
  for (const k of Object.keys(calls)) delete calls[k];
  setTransport(
    mockTransport({
      "WorkflowService.ListWorkflows": (req) => {
        count("ListWorkflows");
        const r = req as { projectId: string; includeHidden?: boolean };
        const rows = (server[r.projectId] ?? []).filter((w) => r.includeHidden || !w.isHidden);
        return { workflows: rows, invalidWorkflows: [] };
      },
      "WorkflowService.GetWorkflow": (req) => {
        count("GetWorkflow");
        const r = req as { name?: string };
        return { workflow: { name: r.name ?? "" }, version: BigInt(version), source: "user" };
      },
      "WorkflowService.SaveWorkflow": (req) => {
        const name = (req as { workflow?: { name?: string } }).workflow?.name ?? "";
        server.p1 = [...server.p1, { name, filename: name, source: "user" }];
        version += 1;
        return { success: true, slug: name, id: "d1", version: BigInt(version) };
      },
      "WorkflowService.DeleteWorkflow": (req) => {
        const name = (req as { name: string }).name;
        server.p1 = server.p1.filter((w) => w.filename !== name);
        return {};
      },
      "WorkflowService.ValidateWorkflow": () => ({ valid: true, errors: [] }),
    }),
  );
  selectProject("p1");
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  resetStreamInvalidation();
  setTransport(null);
  useProjectStore.setState({ currentProject: null });
});

const names = (rows: { name: string }[] | undefined) => rows?.map((w) => w.name);

function draftUpdated(seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    project_id: "p1",
    update_type: UserUpdateType.WORKFLOW_DRAFT_UPDATED,
    entity_type: EntityType.UNSPECIFIED,
    entity_id: "d1",
    data: { draft_id: "d1", slug: "mine", version: 2 },
    created_at: new Date().toISOString(),
  };
}

describe("WorkflowService keys", () => {
  it("saving a workflow refetches the picker's list and the Library before it resolves", async () => {
    const picker = renderHook(() => useWorkflows(), { wrapper });
    const library = renderHook(() => useWorkflowLibrary("p1"), { wrapper });
    await waitFor(() => expect(names(picker.result.current.workflows)).toEqual(["builtin://agent"]));
    await waitFor(() => expect(names(library.result.current.data?.workflows)).toEqual(["builtin://agent", "helper"]));

    await act(() => saveWorkflow("p1", { name: "mine" } as never));

    // In the cache when saveWorkflow resolves (the refetch was awaited)...
    const cached = queryClient.getQueryData<{ workflows: Row[] }>(
      workflowServiceKeys.listWorkflows({ projectId: "p1", includeHidden: false }),
    );
    expect(names(cached?.workflows)).toEqual(["builtin://agent", "mine"]);
    // ...and on screen, in both lists.
    await waitFor(() => expect(names(picker.result.current.workflows)).toEqual(["builtin://agent", "mine"]));
    await waitFor(() =>
      expect(names(library.result.current.data?.workflows)).toEqual(["builtin://agent", "helper", "mine"]),
    );
    expect(picker.result.current.workflows[1].source).toBe("user");
  });

  it("deleting a workflow refetches the mounted list and invalidates the automations", async () => {
    const triggersKey = triggerServiceKeys.listTriggers({ projectId: "p1" });
    queryClient.setQueryData(triggersKey, { triggers: [] });
    const library = renderHook(() => useWorkflowLibrary("p1"), { wrapper });
    await waitFor(() => expect(names(library.result.current.data?.workflows)).toEqual(["builtin://agent", "helper"]));

    await act(() => deleteWorkflow("p1", "helper"));

    await waitFor(() => expect(names(library.result.current.data?.workflows)).toEqual(["builtin://agent"]));
    expect(queryClient.getQueryState(triggersKey)?.isInvalidated).toBe(true);
  });

  it("the chat-safe list follows the current project", async () => {
    const picker = renderHook(() => useWorkflows(), { wrapper });
    await waitFor(() => expect(names(picker.result.current.workflows)).toEqual(["builtin://agent"]));

    act(() => selectProject("p2"));
    await waitFor(() => expect(names(picker.result.current.workflows)).toEqual(["builtin://plan"]));
  });

  it("the stream's WORKFLOW_DRAFT_UPDATED invalidates workflowServiceKeys.all and the preset lists", () => {
    const listKey = workflowServiceKeys.listWorkflows({ projectId: "p1", includeHidden: true });
    const getKey = workflowServiceKeys.getWorkflow({ projectId: "p1", name: "mine" });
    const presetKey = presetServiceKeys.listPresets({ projectId: "p1", includeHidden: false });
    queryClient.setQueryData(listKey, { workflows: [], invalidWorkflows: [] });
    queryClient.setQueryData(getKey, {});
    queryClient.setQueryData(presetKey, { presets: [], invalidPresets: [] });
    const onDraft = vi.fn();
    const unsubscribe = subscribeToDraftUpdates("d1", onDraft);

    useGlobalUpdatesStore.getState().handleUpdate([draftUpdated(1)]);

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(getKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(presetKey)?.isInvalidated).toBe(true);
    // The open editor still hears about its draft.
    expect(onDraft).toHaveBeenCalledWith({ draftId: "d1", slug: "mine", version: 2 });
    unsubscribe();
  });

  it("workflowServiceKeys.all reaches the lists and the definitions", async () => {
    renderHook(() => useWorkflows(), { wrapper });
    renderHook(() => useWorkflowDefinitionQuery("p1", "builtin://agent"), { wrapper });
    await waitFor(() => expect([calls.ListWorkflows, calls.GetWorkflow]).toEqual([1, 1]));

    await queryClient.invalidateQueries({ queryKey: workflowServiceKeys.all });
    expect([calls.ListWorkflows, calls.GetWorkflow]).toEqual([2, 2]);
  });

  it("an editor load bypasses the cache; a lookup reads it", async () => {
    const first = await fetchWorkflow("p1", { name: "mine" });
    expect(first.version).toBe(1);
    version = 5;

    expect((await fetchWorkflow("p1", { name: "mine" })).version).toBe(1);
    expect(calls.GetWorkflow).toBe(1);
    expect((await fetchWorkflow("p1", { name: "mine" }, { fresh: true })).version).toBe(5);
    expect(calls.GetWorkflow).toBe(2);
  });

  it("validating a canvas invalidates nothing", async () => {
    const listKey = workflowServiceKeys.listWorkflows({ projectId: "p1", includeHidden: false });
    queryClient.setQueryData(listKey, { workflows: [], invalidWorkflows: [] });

    await expect(validateWorkflow("p1", { name: "mine" } as never)).resolves.toEqual({ valid: true, errors: [] });
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
  });
});
