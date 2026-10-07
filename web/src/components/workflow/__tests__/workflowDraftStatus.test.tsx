/**
 * Workflow draft lifecycle in the builder (specs/workflow-draft-lifecycle.md).
 *
 * Users can store INVALID work in progress as a draft and later mark it
 * complete; validation blocks becoming complete/runnable, not every save.
 * These pin the UI-side rules: which workflows count as runnable for
 * pickers, how a rejected save of a published (complete) workflow is
 * recognized, and the Draft badge. When Publish is disabled is
 * workflowFindings' publishBlockers.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { WorkflowDraftStatus } from "../../../gen/reliant/v1/workflow_pb";
import { DraftStatusBadge } from "../DraftStatusBadge";
import {
  draftStatusFromProto,
  draftStatusToProto,
  isCompleteSaveRejection,
  isRunnable,
  splitFindings,
} from "../workflowDraftStatus";

const error = { type: "node_ordering", message: "nodes.scrape may not have run" };
const warning = { type: "warning:conditional_access", message: "exit_code zero-fills" };

describe("draft status mapping", () => {
  it("maps the wire enum both ways; UNSPECIFIED is never runnable", () => {
    expect(draftStatusFromProto(WorkflowDraftStatus.COMPLETE)).toBe("complete");
    expect(draftStatusFromProto(WorkflowDraftStatus.DRAFT)).toBe("draft");
    expect(draftStatusFromProto(WorkflowDraftStatus.UNSPECIFIED)).toBe("draft");
    expect(draftStatusFromProto(undefined)).toBe("draft");
    expect(draftStatusToProto("complete")).toBe(WorkflowDraftStatus.COMPLETE);
    expect(draftStatusToProto("draft")).toBe(WorkflowDraftStatus.DRAFT);
  });
});

describe("splitFindings", () => {
  it("separates blocking errors from warning:* findings", () => {
    expect(splitFindings([error, warning])).toEqual({ errors: [error], warnings: [warning] });
  });
});

describe("isRunnable", () => {
  it("offers complete, visible workflows; never drafts or hidden ones", () => {
    expect(isRunnable({ status: "complete" })).toBe(true);
    expect(isRunnable({})).toBe(true); // builtin/project: always complete
    expect(isRunnable({ status: "draft" })).toBe(false);
    expect(isRunnable({ status: "complete", is_hidden: true })).toBe(false);
  });
});

describe("isCompleteSaveRejection", () => {
  it("recognizes a refused save that carries errors (offer Save as draft)", () => {
    expect(isCompleteSaveRejection({ success: false, validationErrors: [error, warning] })).toBe(true);
  });
  it("does not treat an error-free failure (e.g. a name conflict) as a validation rejection", () => {
    expect(isCompleteSaveRejection({ success: false, validationErrors: [warning] })).toBe(false);
    expect(isCompleteSaveRejection({ success: true, validationErrors: [error] })).toBe(false);
  });
});

describe("DraftStatusBadge", () => {
  it("shows Draft and the error count when the draft has errors", () => {
    render(<DraftStatusBadge errorCount={3} />);
    const badge = screen.getByTestId("workflow-draft-badge");
    expect(badge.textContent).toContain("Draft");
    expect(badge.textContent).toContain("3");
  });

  it("shows just Draft for a valid draft", () => {
    render(<DraftStatusBadge />);
    expect(screen.getByTestId("workflow-draft-badge").textContent).toBe("Draft");
  });
});
