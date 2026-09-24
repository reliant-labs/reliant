// Copyright (c) 2025 Reliant Labs

/**
 * The workflow draft lifecycle over the wire (specs/workflow-draft-lifecycle.md).
 *
 * Mocked at the client boundary (grpcClient.workflow) so the request actually
 * built is what gets asserted:
 *   - a save with no intent sends status UNSPECIFIED (keep the stored status —
 *     so re-saving a complete workflow stays gated server-side)
 *   - "Save as draft" sends DRAFT; mark-complete sends SetWorkflowStatus(COMPLETE)
 *   - list items / get / save responses surface status + findings, and no
 *     stale is_valid flag survives on stored-workflow data
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";

const saveWorkflow = vi.fn();
const setWorkflowStatus = vi.fn();
const listWorkflows = vi.fn();

vi.mock("../grpc-client", () => ({
  grpcClient: { workflow: () => ({ saveWorkflow, setWorkflowStatus, listWorkflows }) },
}));

const nodeOrdering = { type: "node_ordering", message: "nodes.scrape may not have run", suggestion: "guard it" };

describe("workflowGrpc draft lifecycle", () => {
  beforeEach(() => {
    saveWorkflow.mockReset();
    setWorkflowStatus.mockReset();
    listWorkflows.mockReset();
  });

  it("a plain save keeps the stored status (UNSPECIFIED); save-as-draft sends DRAFT", async () => {
    saveWorkflow.mockResolvedValue({
      success: true, message: "Saved as draft", isValid: false, validationErrors: [nodeOrdering],
      id: "d1", slug: "wf", version: 2n, yamlDefinition: "", status: WorkflowDraftStatus.DRAFT,
    });
    const { workflowGrpc } = await import("../workflow-grpc");

    const plain = await workflowGrpc.saveWorkflow("p1", { name: "wf", nodes: [], edges: [] });
    expect(saveWorkflow.mock.calls[0][0].status).toBe(WorkflowDraftStatus.UNSPECIFIED);
    expect(plain.status).toBe("draft");
    expect(plain.validationErrors).toEqual([nodeOrdering]);

    await workflowGrpc.saveWorkflow("p1", { name: "wf", nodes: [], edges: [] }, undefined, 2, undefined, "d1", "draft");
    expect(saveWorkflow.mock.calls[1][0].status).toBe(WorkflowDraftStatus.DRAFT);
  });

  it("mark complete sends SetWorkflowStatus(COMPLETE) and surfaces a rejection with its errors", async () => {
    setWorkflowStatus.mockResolvedValue({
      success: false, message: "Not marked complete", status: WorkflowDraftStatus.DRAFT,
      validationErrors: [nodeOrdering], version: 3n,
    });
    const { workflowGrpc } = await import("../workflow-grpc");

    const result = await workflowGrpc.setWorkflowStatus("p1", "d1", "complete", 3);
    const request = setWorkflowStatus.mock.calls[0][0];
    expect(request.status).toBe(WorkflowDraftStatus.COMPLETE);
    expect(request.draftId).toBe("d1");
    expect(request.expectedVersion).toBe(3n);
    expect(result).toMatchObject({ success: false, status: "draft", version: 3 });
    expect(result.validationErrors).toEqual([nodeOrdering]);
  });

  it("list items carry status and computed findings, not a stored is_valid", async () => {
    listWorkflows.mockResolvedValue({
      workflows: [
        { name: "wip", filename: "wip", stepCount: 1, source: "user", nodes: [], edges: [],
          status: WorkflowDraftStatus.DRAFT, validationErrors: [nodeOrdering] },
        { name: "builtin://agent", filename: "agent", stepCount: 1, source: "builtin", nodes: [], edges: [],
          status: WorkflowDraftStatus.COMPLETE, validationErrors: [] },
      ],
      invalidWorkflows: [],
    });
    const { workflowGrpc } = await import("../workflow-grpc");

    const [wip, agent] = await workflowGrpc.listWorkflows("p-list", true);
    expect(wip.status).toBe("draft");
    expect(wip.validationErrors).toEqual([nodeOrdering]);
    expect(agent.status).toBe("complete");
    expect(wip).not.toHaveProperty("isValid");
  });
});
