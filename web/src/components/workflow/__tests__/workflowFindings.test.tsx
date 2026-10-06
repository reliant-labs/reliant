/**
 * Validation that tells the truth (research/WORKFLOW_EDITOR_UX_REVIEW.md
 * issue 4): the status chip never says "Valid" about unsaved edits, every
 * finding is placed on its step and field and printed with its remedy once,
 * and picking a finding about a step selects it — not only edge findings.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ValidationError } from "../../../api/workflow-grpc";
import { WorkflowStatusChip } from "../WorkflowStatusChip";
import {
  findingFieldKey,
  findingsByNode,
  locateFinding,
  locateFindings,
  publishBlockers,
  summarizeWorkflowStatus,
} from "../workflowFindings";

const finding = (overrides: Partial<ValidationError>) =>
  ({ type: "structure", message: "", suggestion: "", nodeId: "", field: "", detail: "", path: "", ...overrides }) as ValidationError;

const missingModel = finding({
  message: "wf.nodes.[1](summarize).model: call_llm node requires a model",
  nodeId: "summarize",
  field: "model",
  detail: "call_llm node requires a model",
  path: "wf.nodes.[1](summarize).model",
});
const unreachable = finding({
  message: "wf.nodes.summarize: node 'summarize' is unreachable (add an edge to this node)",
  suggestion: "add an edge to this node",
  nodeId: "summarize",
  detail: "node 'summarize' is unreachable",
  path: "wf.nodes.summarize",
});

const base = {
  source: "user" as const,
  draftStatus: "draft" as const,
  hasUnsavedChanges: false,
  validationStatus: "valid" as const,
  errorCount: 0,
  warningCount: 0,
};

describe("summarizeWorkflowStatus", () => {
  it("says so while there are unsaved changes, never 'Valid'", () => {
    const summary = summarizeWorkflowStatus({ ...base, hasUnsavedChanges: true });
    expect(summary.detail).toBe("Unsaved changes");
    expect(`${summary.lifecycle} ${summary.detail}`).not.toMatch(/valid/i);
    expect(summarizeWorkflowStatus({ ...base, hasUnsavedChanges: true, validationStatus: "invalid", errorCount: 2 }).detail).toBe(
      "Unsaved changes · 2 problems",
    );
  });

  it("names the lifecycle in the words the UI uses everywhere", () => {
    expect(summarizeWorkflowStatus(base)).toMatchObject({ lifecycle: "Draft", detail: "Ready to publish" });
    expect(summarizeWorkflowStatus({ ...base, draftStatus: "complete" })).toMatchObject({ lifecycle: "Published", detail: "No problems" });
    expect(summarizeWorkflowStatus({ ...base, source: "builtin", draftStatus: "complete" }).lifecycle).toBe("Built-in");
    expect(summarizeWorkflowStatus({ ...base, validationStatus: "validating" }).detail).toBe("Checking…");
  });
});

describe("publishBlockers", () => {
  it("does not block on unsaved edits: Publish saves them in the same step", () => {
    expect(publishBlockers({ errorCount: 0, isBusy: false, validating: false })).toEqual([]);
    expect(publishBlockers({ errorCount: 2, isBusy: false, validating: false })).toEqual(["Fix 2 problems first."]);
  });
});

describe("locating findings", () => {
  it("prints the finding without its path and without repeating its remedy", () => {
    expect(locateFinding(unreachable)).toMatchObject({
      nodeId: "summarize",
      text: "node 'summarize' is unreachable",
      suggestion: "add an edge to this node",
    });
    // Older servers send no detail: strip the path and the "(suggestion)" suffix.
    const legacy = locateFinding(finding({ ...unreachable, detail: "" }));
    expect(legacy.text).toBe("node 'summarize' is unreachable");
  });

  it("maps a field location to the config panel's field", () => {
    expect(findingFieldKey("model")).toBe("model");
    expect(findingFieldKey("with.channel")).toBe("channel");
    expect(findingFieldKey("thread.inject.content")).toBeUndefined();
    expect(findingFieldKey("inline.nodes.[0](x).model")).toBeUndefined();
  });

  it("groups findings by step and places trigger findings on the trigger", () => {
    const located = locateFindings([missingModel, unreachable, finding({ path: "wf.triggers[2](nightly).schedule" })]);
    expect(findingsByNode(located).get("summarize")).toHaveLength(2);
    expect(located[2]).toMatchObject({ nodeId: undefined, triggerIndex: 2 });
  });
});

describe("WorkflowStatusChip", () => {
  it("lists every problem in words, and picking one about a step selects it", async () => {
    const onSelect = vi.fn();
    render(
      <WorkflowStatusChip
        summary={summarizeWorkflowStatus({ ...base, validationStatus: "invalid", errorCount: 2 })}
        findings={locateFindings([missingModel, unreachable])}
        describeNode={(id) => `Call LLM · ${id}`}
        canSelect={() => true}
        onSelect={onSelect}
      />,
    );
    await userEvent.click(screen.getByTestId("workflow-status-chip"));
    const list = screen.getByRole("dialog", { name: "Problems" });
    const items = within(list).getAllByRole("button");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("Call LLM · summarize · Model");
    expect(items[0]).toHaveTextContent("call_llm node requires a model");
    // The remedy appears exactly once.
    expect(within(list).getAllByText("add an edge to this node")).toHaveLength(1);

    await userEvent.click(items[0]!);
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ nodeId: "summarize", fieldKey: "model" }));
    expect(screen.queryByRole("dialog", { name: "Problems" })).toBeNull();
  });

  it("closes its list on Escape", async () => {
    render(
      <WorkflowStatusChip
        summary={summarizeWorkflowStatus({ ...base, validationStatus: "invalid", errorCount: 1 })}
        findings={locateFindings([missingModel])}
        describeNode={(id) => id}
        canSelect={() => true}
        onSelect={() => {}}
      />,
    );
    await userEvent.click(screen.getByTestId("workflow-status-chip"));
    expect(screen.getByRole("dialog", { name: "Problems" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Problems" })).toBeNull();
  });
});
