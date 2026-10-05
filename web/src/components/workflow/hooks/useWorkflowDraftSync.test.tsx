import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

const getWorkflowByDraftId = vi.fn();
vi.mock("../../../api/workflow-grpc", () => ({
  getWorkflowByDraftId: (...args: unknown[]) => getWorkflowByDraftId(...args),
}));

import { useWorkflowDraftSync } from "./useWorkflowDraftSync";
import { publishDraftUpdate } from "../../../store/workflowDraftUpdates";

const remote = (version: number) => ({
  workflow: { name: "wf", nodes: [], edges: [] },
  version,
  yamlDefinition: "yaml",
  status: "draft",
});

function setup(overrides: Partial<Parameters<typeof useWorkflowDraftSync>[0]> = {}) {
  const onRemoteUpdate = vi.fn();
  const onModifiedElsewhere = vi.fn();
  const props = {
    projectId: "p1",
    draftId: "d1",
    version: 3,
    hasModifications: false,
    isSaving: false,
    onRemoteUpdate,
    onModifiedElsewhere,
    ...overrides,
  };
  const hook = renderHook((p: typeof props) => useWorkflowDraftSync(p), { initialProps: props });
  return { hook, props, onRemoteUpdate, onModifiedElsewhere };
}

describe("useWorkflowDraftSync", () => {
  beforeEach(() => {
    getWorkflowByDraftId.mockReset();
  });

  it("refetches and applies a newer version", async () => {
    getWorkflowByDraftId.mockResolvedValue(remote(4));
    const { onRemoteUpdate } = setup();

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 4 }));

    await waitFor(() => expect(onRemoteUpdate).toHaveBeenCalledTimes(1));
    expect(getWorkflowByDraftId).toHaveBeenCalledWith("p1", "d1");
    expect(onRemoteUpdate.mock.calls[0][0].version).toBe(4);
  });

  it("ignores the same or an older version (self-echo)", async () => {
    const { onRemoteUpdate } = setup();

    act(() => {
      publishDraftUpdate({ draftId: "d1", slug: "wf", version: 3 });
      publishDraftUpdate({ draftId: "d1", slug: "wf", version: 2 });
    });

    expect(getWorkflowByDraftId).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();
  });

  it("ignores updates for other drafts", () => {
    const { onRemoteUpdate } = setup();
    act(() => publishDraftUpdate({ draftId: "other", slug: "x", version: 9 }));
    expect(getWorkflowByDraftId).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();
  });

  it("shows the notice instead of applying when the canvas has unsaved edits", async () => {
    getWorkflowByDraftId.mockResolvedValue(remote(4));
    const { onRemoteUpdate, onModifiedElsewhere } = setup({ hasModifications: true });

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 4 }));

    expect(onModifiedElsewhere).toHaveBeenCalledTimes(1);
    expect(getWorkflowByDraftId).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();

    await act(async () => {
      await onModifiedElsewhere.mock.calls[0][0]();
    });
    expect(onRemoteUpdate).toHaveBeenCalledTimes(1);
  });

  it("drops a push that arrives mid-save once the save's version lands", async () => {
    const { hook, props, onRemoteUpdate } = setup({ isSaving: true });

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 4 }));
    hook.rerender({ ...props, isSaving: false, version: 4 });

    await Promise.resolve();
    expect(getWorkflowByDraftId).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();
  });

  it("applies a push from elsewhere that arrives mid-save with a version above the saved one", async () => {
    getWorkflowByDraftId.mockResolvedValue(remote(5));
    const { hook, props, onRemoteUpdate } = setup({ isSaving: true });

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 5 }));
    hook.rerender({ ...props, isSaving: false, version: 4 });

    await waitFor(() => expect(onRemoteUpdate).toHaveBeenCalledTimes(1));
    expect(onRemoteUpdate.mock.calls[0][0].version).toBe(5);
  });

  it("holds a status-change echo (isSaving covers it) and drops it once the version lands", async () => {
    const { hook, props, onRemoteUpdate, onModifiedElsewhere } = setup({ isSaving: true, hasModifications: true });

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 4 }));
    expect(onModifiedElsewhere).not.toHaveBeenCalled();
    hook.rerender({ ...props, isSaving: false, version: 4 });

    await Promise.resolve();
    expect(onModifiedElsewhere).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();
  });

  it("never applies an older fetch that resolves after a newer one (out of order)", async () => {
    const resolvers: Array<(v: unknown) => void> = [];
    getWorkflowByDraftId.mockImplementation(() => new Promise((resolve) => resolvers.push(resolve)));
    const { onRemoteUpdate } = setup();

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 4 }));
    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 5 }));
    // Single-flight: the second push did not start a second concurrent fetch.
    expect(getWorkflowByDraftId).toHaveBeenCalledTimes(1);

    await act(async () => resolvers[0](remote(5)));
    await waitFor(() => expect(onRemoteUpdate).toHaveBeenCalledTimes(1));
    // The queued refetch returns a stale version and is discarded.
    await waitFor(() => expect(getWorkflowByDraftId).toHaveBeenCalledTimes(2));
    await act(async () => resolvers[1](remote(4)));
    expect(onRemoteUpdate).toHaveBeenCalledTimes(1);
    expect(onRemoteUpdate.mock.calls[0][0].version).toBe(5);
  });

  it("surfaces a delete for the open draft without fetching or discarding edits, bypassing the version gate", () => {
    const onDeleted = vi.fn();
    const { onRemoteUpdate, onModifiedElsewhere } = setup({ hasModifications: true, onDeleted });

    act(() => publishDraftUpdate({ draftId: "d1", slug: "wf", version: 3, deleted: true }));

    expect(onDeleted).toHaveBeenCalledTimes(1);
    expect(getWorkflowByDraftId).not.toHaveBeenCalled();
    expect(onRemoteUpdate).not.toHaveBeenCalled();
    expect(onModifiedElsewhere).not.toHaveBeenCalled();
  });
});
