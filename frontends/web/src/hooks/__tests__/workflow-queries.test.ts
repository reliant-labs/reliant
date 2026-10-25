// Copyright (c) 2025 Reliant Labs

/**
 * What hooks/workflow-queries builds on the wire and hands the views.
 *
 * The draft lifecycle (specs/workflow-draft-lifecycle.md), answered at the
 * transport so the request actually built is what gets asserted:
 *   - a save with no intent sends status UNSPECIFIED (keep the stored status —
 *     so re-saving a complete workflow stays gated server-side)
 *   - "Save as draft" sends DRAFT; mark-complete sends SetWorkflowStatus(COMPLETE)
 *   - list items / save responses surface status + findings, and no stale
 *     is_valid flag survives on stored-workflow data
 *
 * The chat-safe rows carry the graph, not just the step count. The bug this
 * pins: `step_count` was copied but `nodes`/`edges` were not, so the mobile
 * catalog row said "17 steps" while the detail screen one tap away rendered
 * "This workflow has no steps" for all 23 workflows.
 */

import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ListWorkflowsResponseSchema,
  WorkflowDraftStatus,
  type SaveWorkflowRequest,
  type SetWorkflowStatusRequest,
} from "@/gen/reliant/v1/workflow_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { fetchWorkflowList, saveWorkflow, setWorkflowStatus, workflowDefsFromProto } from "../workflow-queries";

const saveRpc = vi.fn();
const setStatusRpc = vi.fn();
const listRpc = vi.fn();

const nodeOrdering = { type: "node_ordering", message: "nodes.scrape may not have run", suggestion: "guard it" };

beforeEach(() => {
  saveRpc.mockReset();
  setStatusRpc.mockReset();
  listRpc.mockReset();
  setTransport(
    mockTransport({
      "WorkflowService.SaveWorkflow": (req) => saveRpc(req),
      "WorkflowService.SetWorkflowStatus": (req) => setStatusRpc(req),
      "WorkflowService.ListWorkflows": (req) => listRpc(req),
    }),
  );
});

afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

describe("workflow draft lifecycle", () => {
  it("a plain save keeps the stored status (UNSPECIFIED); save-as-draft sends DRAFT", async () => {
    saveRpc.mockResolvedValue({
      success: true, message: "Saved as draft", isValid: false, validationErrors: [nodeOrdering],
      id: "d1", slug: "wf", version: 2n, yamlDefinition: "", status: WorkflowDraftStatus.DRAFT,
    });

    const plain = await saveWorkflow("p1", { name: "wf", nodes: [], edges: [] });
    expect((saveRpc.mock.calls[0][0] as SaveWorkflowRequest).status).toBe(WorkflowDraftStatus.UNSPECIFIED);
    expect(plain.status).toBe("draft");
    expect(plain.validationErrors).toMatchObject([nodeOrdering]);

    await saveWorkflow("p1", { name: "wf", nodes: [], edges: [] }, 2, undefined, "d1", "draft");
    expect((saveRpc.mock.calls[1][0] as SaveWorkflowRequest).status).toBe(WorkflowDraftStatus.DRAFT);
  });

  it("mark complete sends SetWorkflowStatus(COMPLETE) and surfaces a rejection with its errors", async () => {
    setStatusRpc.mockResolvedValue({
      success: false, message: "Not marked complete", status: WorkflowDraftStatus.DRAFT,
      validationErrors: [nodeOrdering], version: 3n,
    });

    const result = await setWorkflowStatus("p1", "d1", "complete", 3);
    const request = setStatusRpc.mock.calls[0][0] as SetWorkflowStatusRequest;
    expect(request.status).toBe(WorkflowDraftStatus.COMPLETE);
    expect(request.draftId).toBe("d1");
    expect(request.expectedVersion).toBe(3n);
    expect(result).toMatchObject({ success: false, status: "draft", version: 3 });
    expect(result.validationErrors).toMatchObject([nodeOrdering]);
  });

  it("list items carry status and computed findings, not a stored is_valid", async () => {
    listRpc.mockResolvedValue({
      workflows: [
        { name: "wip", filename: "wip", stepCount: 1, source: "user",
          status: WorkflowDraftStatus.DRAFT, validationErrors: [nodeOrdering] },
        { name: "builtin://agent", filename: "agent", stepCount: 1, source: "builtin",
          status: WorkflowDraftStatus.COMPLETE, validationErrors: [] },
      ],
      invalidWorkflows: [],
    });

    const { workflows: [wip, agent] } = await fetchWorkflowList("p-list", true);
    expect(wip.status).toBe("draft");
    expect(wip.validationErrors).toMatchObject([nodeOrdering]);
    expect(agent.status).toBe("complete");
    expect(wip).not.toHaveProperty("isValid");
  });
});

/** Shaped like a real ListWorkflows entry, trimmed to what this asserts. */
function wireResponse(overrides: Record<string, unknown> = {}) {
  return create(ListWorkflowsResponseSchema, {
    workflows: [
      {
        name: "gsd",
        filename: "gsd.yaml",
        description: "Get stuff done",
        stepCount: 17,
        source: "builtin",
        isHidden: false,
        status: WorkflowDraftStatus.COMPLETE,
        nodes: Array.from({ length: 17 }, (_, i) => ({ id: `n${i}` })),
        edges: Array.from({ length: 16 }, (_, i) => ({ from: `n${i}`, to: `n${i + 1}` })),
        ...overrides,
      },
    ],
  });
}

describe("the chat-safe workflow rows", () => {
  it("carry the graph through, not just the step count", () => {
    const [wf] = workflowDefsFromProto(wireResponse());
    expect(wf.nodes).toHaveLength(17);
    expect(wf.edges).toHaveLength(16);
    // The whole bug was these two disagreeing: a workflow that reports N
    // steps must carry N nodes, or two screens describe it differently.
    expect(wf.nodes.length).toBe(wf.step_count);
  });

  it("still map the descriptive fields", () => {
    expect(workflowDefsFromProto(wireResponse())[0]).toMatchObject({
      name: "gsd",
      filename: "gsd.yaml",
      description: "Get stuff done",
      step_count: 17,
      source: "builtin",
      is_hidden: false,
      status: "complete",
    });
  });

  it("tolerate a workflow that genuinely has no graph", () => {
    // An invalid or unparsed workflow legitimately has no nodes.
    const [wf] = workflowDefsFromProto(wireResponse({ stepCount: 0, nodes: [], edges: [] }));
    expect(wf.nodes).toEqual([]);
    expect(wf.step_count).toBe(0);
  });
});
