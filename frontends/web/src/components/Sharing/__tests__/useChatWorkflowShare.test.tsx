// Copyright (c) 2025 Reliant Labs

/**
 * Which saved workflow a chat row's "Share workflow…" acts on: GetWorkflow
 * resolves the chat's workflow reference as a run does, and answers a saved
 * workflow only to its owner, with its org and sharing state. A builtin is
 * never read; a project workflow, one with no org, or a viewer's chat has
 * nothing to share.
 */

import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";
import { WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const state = vi.hoisted(() => ({ controlPlane: true, getWorkflow: vi.fn() }));

vi.mock("@/services/controlPlane/config", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/controlPlane/config")>();
  return {
    ...actual,
    get hasControlPlane() {
      return state.controlPlane;
    },
  };
});

const { useChatWorkflowShare } = await import("../useChatWorkflowShare");
const { useWorkflowDefinitionQuery } = await import("@/hooks/workflow-queries");

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const saved = (over: Record<string, unknown> = {}) => ({
  source: "user",
  draftId: "d-triage",
  orgId: "acme",
  visibility: ResourceVisibility.PRIVATE,
  status: WorkflowDraftStatus.COMPLETE,
  workflow: { name: "triage", title: "Issue triage" },
  ...over,
});

beforeEach(() => {
  state.controlPlane = true;
  state.getWorkflow.mockReset();
  setTransport(mockTransport({ ...serviceHandlers("WorkflowService", { getWorkflow: state.getWorkflow }) }));
});
afterEach(() => setTransport(null));

describe("useChatWorkflowShare", () => {
  it("is the chat's saved workflow, by the chat's reference in its project", async () => {
    state.getWorkflow.mockResolvedValue(saved());
    const { result } = renderHook(() => useChatWorkflowShare({ projectId: "p1", workflowName: "triage" }), { wrapper });
    await waitFor(() =>
      expect(result.current).toEqual({ draftId: "d-triage", name: "Issue triage", orgId: "acme", shared: false }),
    );
    expect(state.getWorkflow.mock.calls[0]![0]).toMatchObject({ projectId: "p1", name: "triage" });
  });

  it("says when it is already shared", async () => {
    state.getWorkflow.mockResolvedValue(saved({ visibility: ResourceVisibility.ORG }));
    const { result } = renderHook(() => useChatWorkflowShare({ projectId: "p1", workflowName: "triage" }), { wrapper });
    await waitFor(() => expect(result.current?.shared).toBe(true));
  });

  it.each([
    ["a project workflow", { source: "project", draftId: undefined, orgId: undefined, visibility: ResourceVisibility.UNSPECIFIED }],
    ["a saved workflow with no org", { orgId: undefined }],
    ["an answer from a server that does not say", { visibility: ResourceVisibility.UNSPECIFIED }],
  ])("is nothing for %s", async (_name, over) => {
    state.getWorkflow.mockResolvedValue(saved(over));
    // The same read beside it says when the answer is in.
    const { result } = renderHook(
      () => ({
        share: useChatWorkflowShare({ projectId: "p1", workflowName: "triage" }),
        answered: useWorkflowDefinitionQuery("p1", "triage").isSuccess,
      }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.answered).toBe(true));
    expect(result.current.share).toBeNull();
  });

  it.each([
    ["no chat", undefined],
    ["a builtin", { projectId: "p1", workflowName: "builtin://agent" }],
    ["no workflow", { projectId: "p1" }],
    ["a viewer's chat", { projectId: "p1", workflowName: "triage", viewerAccess: true }],
  ])("reads nothing for %s", async (_name, chat) => {
    const { result } = renderHook(() => useChatWorkflowShare(chat), { wrapper });
    expect(result.current).toBeNull();
    expect(state.getWorkflow).not.toHaveBeenCalled();
  });

  it("reads nothing without a control plane", () => {
    state.controlPlane = false;
    const { result } = renderHook(() => useChatWorkflowShare({ projectId: "p1", workflowName: "triage" }), { wrapper });
    expect(result.current).toBeNull();
    expect(state.getWorkflow).not.toHaveBeenCalled();
  });
});
