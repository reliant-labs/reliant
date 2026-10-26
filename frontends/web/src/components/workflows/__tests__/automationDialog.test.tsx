// Copyright (c) 2025 Reliant Labs

/**
 * The one opener for the automation dialog (openAutomationDialog) and the
 * host that renders it: a request names what the user wants, and the host
 * picks the dialog — the declared-trigger setup for a workflow that declares
 * triggers, the custom form for one that does not (or when asked for a
 * custom one), the trigger's own form or its project's settings for an edit.
 * The dialogs themselves are stubs: their insides are not this test's.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  library: { workflows: [] as Array<{ name: string; title?: string; triggers?: unknown[] }> },
  triggers: [] as Array<{ id: string; workflow: string; projectId: string; workflowTrigger?: string; name: string }>,
}));

vi.mock("@/hooks/workflow-library-queries", () => ({
  useWorkflowLibrary: () => ({ data: mocks.library, isLoading: false }),
}));
vi.mock("@/hooks/trigger-queries", () => ({
  useTriggerById: (id?: string) => mocks.triggers.find((t) => t.id === id),
}));
vi.mock("@/store/projectStore", () => {
  const state = { currentProject: { id: "proj-1", name: "Reliant" } };
  return { useProjectStore: (selector: (s: typeof state) => unknown) => selector(state) };
});
vi.mock("../../Automations/AutomationFormDialog", () => ({
  AutomationFormDialog: (props: { trigger?: { id: string }; prefill?: { workflow?: string; name?: string }; defaultProjectId?: string }) => (
    <div data-testid="custom-form" data-trigger={props.trigger?.id ?? ""} data-workflow={props.prefill?.workflow ?? ""} data-project={props.defaultProjectId ?? ""} />
  ),
}));
vi.mock("../../Automations/WorkflowSetupDialog", () => ({
  WorkflowSetupDialog: (props: { workflowRef: string; mode?: string; defaultProjectId?: string; declared: unknown[] }) => (
    <div data-testid="setup-form" data-workflow={props.workflowRef} data-mode={props.mode ?? ""} data-project={props.defaultProjectId ?? ""} data-declared={props.declared.length} />
  ),
}));

import { AutomationDialogHost, closeAutomationDialog, openAutomationDialog } from "../../Automations/automationDialog";

function renderHost() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AutomationDialogHost />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocks.library = {
    workflows: [
      { name: "triage", triggers: [{ name: "nightly" }] },
      { name: "builtin://agent", title: "Agent" },
    ],
  };
  mocks.triggers = [
    { id: "declared-1", workflow: "triage", projectId: "proj-2", workflowTrigger: "nightly", name: "Triage · nightly" },
    { id: "custom-1", workflow: "builtin://agent", projectId: "proj-1", name: "Morning" },
  ];
});
afterEach(() => act(() => closeAutomationDialog()));

describe("openAutomationDialog", () => {
  it("renders nothing until asked", () => {
    renderHost();
    expect(screen.queryByTestId("custom-form")).toBeNull();
    expect(screen.queryByTestId("setup-form")).toBeNull();
  });

  it("a workflow that declares triggers is set up from them, in the current project by default", () => {
    renderHost();
    act(() => openAutomationDialog({ workflowRef: "triage" }));
    const setup = screen.getByTestId("setup-form");
    expect(setup).toHaveAttribute("data-workflow", "triage");
    expect(setup).toHaveAttribute("data-project", "proj-1");
    expect(setup).toHaveAttribute("data-declared", "1");
  });

  it("a workflow that declares none — or a request for a custom one — gets the custom form, prefilled", () => {
    renderHost();
    act(() => openAutomationDialog({ workflowRef: "builtin://agent", projectId: "proj-3" }));
    expect(screen.getByTestId("custom-form")).toHaveAttribute("data-workflow", "builtin://agent");
    expect(screen.getByTestId("custom-form")).toHaveAttribute("data-project", "proj-3");
    act(() => openAutomationDialog({ workflowRef: "triage", custom: true }));
    expect(screen.getByTestId("custom-form")).toHaveAttribute("data-workflow", "triage");
    expect(screen.queryByTestId("setup-form")).toBeNull();
  });

  it("with nothing named it is a new automation, workflow not chosen yet", () => {
    renderHost();
    act(() => openAutomationDialog());
    expect(screen.getByTestId("custom-form")).toHaveAttribute("data-workflow", "");
  });

  it("editing a hand-made trigger opens its form; a declared one, its project's settings", () => {
    renderHost();
    act(() => openAutomationDialog({ triggerId: "custom-1" }));
    expect(screen.getByTestId("custom-form")).toHaveAttribute("data-trigger", "custom-1");
    act(() => openAutomationDialog({ triggerId: "declared-1" }));
    const setup = screen.getByTestId("setup-form");
    expect(setup).toHaveAttribute("data-mode", "settings");
    expect(setup).toHaveAttribute("data-project", "proj-2");
  });

  it("closes", () => {
    renderHost();
    act(() => openAutomationDialog({ workflowRef: "triage" }));
    act(() => closeAutomationDialog());
    expect(screen.queryByTestId("setup-form")).toBeNull();
  });
});
