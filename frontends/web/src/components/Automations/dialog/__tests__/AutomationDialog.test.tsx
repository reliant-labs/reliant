// Copyright (c) 2025 Reliant Labs

/**
 * One dialog, two server models (report 03 §6.8 option 1): which form the
 * automation dialog shows for what it was opened with. The forms themselves
 * are stand-ins here; their own tests pin what they do.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

import type { Trigger } from "@/hooks/trigger-model";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";

const definitions: Record<string, { triggers?: DeclaredTrigger[] } | null> = {};

vi.mock("../../../workflow/useWorkflowDefinition", () => ({
  useWorkflowDefinition: (_projectId: string | undefined, ref: string) => ({
    workflowDef: definitions[ref] ?? null,
    error: definitions[ref] === undefined ? "not found" : null,
    loading: false,
    inputGroups: [],
  }),
}));
vi.mock("@/hooks/workflow-library-queries", () => ({ useWorkflowLibrary: () => ({ data: { workflows: [] } }) }));
vi.mock("@/hooks/trigger-queries", () => ({ useTrigger: () => ({ data: undefined, isError: false }) }));
vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ currentProject: { id: "proj-1", name: "Reliant" } });
  return { useProjectStore: (selector: (s: ReturnType<typeof snapshot>) => unknown) => selector(snapshot()) };
});
vi.mock("../DeclaredAutomationForm", () => ({
  DeclaredAutomationForm: (props: { workflowRef: string; defaultProjectId?: string; mode?: string; declaredTrigger?: string; onUseCustom?: () => void }) => (
    <div data-testid="declared">
      {props.workflowRef} in {props.defaultProjectId} mode={props.mode ?? "auto"} from={props.declaredTrigger ?? "-"}
      {props.onUseCustom && <button onClick={props.onUseCustom}>Need a different trigger?</button>}
    </div>
  ),
}));
vi.mock("../InlineAutomationForm", () => ({
  InlineAutomationForm: (props: { trigger?: Trigger; workflowRef?: string; projectId?: string; onBackToDeclared?: () => void; prefill?: { message?: string } }) => (
    <div data-testid="inline">
      {props.trigger ? `edit ${props.trigger.id}` : `new ${props.workflowRef ?? "agent"} in ${props.projectId}`} {props.prefill?.message ?? ""}
      {props.onBackToDeclared && <button onClick={props.onBackToDeclared}>Use the workflow&apos;s own triggers instead</button>}
    </div>
  ),
}));

import { AutomationDialogHost } from "../AutomationDialogHost";
import { closeAutomationDialog, openAutomationDialog } from "../openAutomationDialog";

const nightly = { name: "nightly", source: { case: "schedule", value: { cron: ["0 9 * * *"] } } } as unknown as DeclaredTrigger;

beforeEach(() => {
  for (const key of Object.keys(definitions)) delete definitions[key];
  definitions["triage"] = { triggers: [nightly] };
  definitions["plain"] = { triggers: [] };
  act(() => closeAutomationDialog());
});

function open(options: Parameters<typeof openAutomationDialog>[0]) {
  render(<AutomationDialogHost />);
  act(() => openAutomationDialog(options));
}

describe("AutomationDialog", () => {
  it("is closed until something opens it", () => {
    render(<AutomationDialogHost />);
    expect(screen.queryByTestId("inline")).not.toBeInTheDocument();
  });

  it("starts blank on the agent, in the current project", () => {
    open({});
    expect(screen.getByTestId("inline")).toHaveTextContent("new agent in proj-1");
  });

  it("turns on a workflow's declared triggers through the declared form", () => {
    open({ workflowRef: "triage", projectId: "proj-2", declaredTrigger: "nightly" });
    expect(screen.getByTestId("declared")).toHaveTextContent("triage in proj-2 mode=auto from=nightly");
  });

  it("hands over to a WHEN of its own and back", () => {
    open({ workflowRef: "triage" });
    fireEvent.click(screen.getByRole("button", { name: "Need a different trigger?" }));
    expect(screen.getByTestId("inline")).toHaveTextContent("new triage in proj-1");
    fireEvent.click(screen.getByRole("button", { name: "Use the workflow's own triggers instead" }));
    expect(screen.getByTestId("declared")).toBeInTheDocument();
  });

  it("gives a workflow with no triggers, or a custom request, its own WHEN", () => {
    open({ workflowRef: "plain" });
    expect(screen.getByTestId("inline")).toHaveTextContent("new plain in proj-1");
    act(() => openAutomationDialog({ workflowRef: "triage", custom: true, prefill: { message: "from a run" } }));
    expect(screen.getByTestId("inline")).toHaveTextContent("new triage in proj-1 from a run");
  });

  it("edits an inline automation inline, and an activation as its workflow's settings", () => {
    const inline = { id: "t-1", source: { kind: "schedule" }, workflow: "", projectId: "proj-1" } as unknown as Trigger;
    open({ trigger: inline });
    expect(screen.getByTestId("inline")).toHaveTextContent("edit t-1");

    const activation = { id: "t-2", source: { kind: "activation", workflowTrigger: "nightly" }, workflow: "triage", projectId: "proj-3" } as unknown as Trigger;
    act(() => openAutomationDialog({ trigger: activation }));
    expect(screen.getByTestId("declared")).toHaveTextContent("triage in proj-3 mode=settings");
  });
});
