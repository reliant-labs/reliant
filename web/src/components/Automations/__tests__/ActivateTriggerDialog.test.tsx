// Copyright (c) 2025 Reliant Labs

/**
 * Activating a workflow's declared trigger builds a CreateTriggerRequest with
 * the `workflow_trigger` arm and only the activation's own fields; inputs the
 * declaration maps are shown read-only and never sent as params (the server
 * refuses them). A webhook activation shows its one-time token. The RPC
 * client is the only mock.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  CreateTriggerResponseSchema,
  RotateWebhookTokenResponseSchema,
  TriggerSchema,
  WebhookCredentialSchema,
  type CreateTriggerRequest,
} from "@/gen/reliant/v1/trigger_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ConnectionSchema, ConnectionStatus, ListConnectionsResponseSchema } from "@/gen/reliant/v1/connection_pb";
import { SearchCatalogResponseSchema } from "@/gen/reliant/v1/catalog_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { triageWorkflowResponse, presetsResponse, worktreesResponse, WORKFLOW_LIST } from "@/components/workflow/run/__tests__/runFormFixtures";
import { renderAtRoute } from "./automationTestUtils";

const createTrigger = vi.fn();
const rotateWebhookToken = vi.fn();
const getWorkflow = vi.fn();
const listWorkflows = vi.fn(async () => WORKFLOW_LIST);
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();
const listConnections = vi.fn();
const searchCatalog = vi.fn();
const getCatalogEntry = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    connection: () => ({ listConnections }),
    catalog: () => ({ searchCatalog, getCatalogEntry }),
    trigger: () => ({ createTrigger, rotateWebhookToken }),
    workflow: () => ({ getWorkflow, listWorkflows }),
    preset: () => ({ listPresetsForWorkflow, getDefaultPresetsBatch }),
    worktree: () => ({ listWorktrees }),
    daemonRegistry: () => ({ listDaemons }),
    project: () => ({ listProjectDaemons }),
  },
  getGRPCBaseURLPublic: () => "https://api.example.com",
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: [{ id: "proj-1", name: "Reliant" }],
    currentProject: { id: "proj-1", name: "Reliant" },
    loadProjects: vi.fn(async () => undefined),
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { ActivateTriggerDialog } from "../ActivateTriggerDialog";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";

const scheduleDeclared = {
  name: "nightly",
  description: "Every weekday morning",
  filter: "",
  inputs: { label: "{{ trigger.scheduled_for }}" },
  source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } },
} as unknown as DeclaredTrigger;

const webhookDeclared = {
  name: "ci-deploy",
  filter: "",
  inputs: {},
  source: { case: "webhook", value: {} },
} as unknown as DeclaredTrigger;

beforeEach(() => {
  createTrigger.mockReset();
  rotateWebhookToken.mockReset();
  getWorkflow.mockReset();
  getWorkflow.mockResolvedValue(triageWorkflowResponse());
  listPresetsForWorkflow.mockResolvedValue(presetsResponse());
  getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  listWorktrees.mockImplementation(async (req: { projectId: string }) => worktreesResponse(req.projectId));
  listDaemons.mockResolvedValue({ daemons: [create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE })] });
  listProjectDaemons.mockResolvedValue({
    projectDaemons: [create(ProjectDaemonSchema, { projectId: "proj-1", daemonId: "daemon-1", installState: ProjectInstallState.INSTALLED })],
  });
});

function render(declared: DeclaredTrigger) {
  const onClose = vi.fn();
  renderAtRoute(<ActivateTriggerDialog open onClose={onClose} workflowRef="triage" workflowTitle="Triage" declared={declared} defaultProjectId="proj-1" />);
  return { onClose };
}

describe("ActivateTriggerDialog", () => {
  it("creates an activation with the workflow_trigger arm and only unmapped inputs", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "t-1", name: "Triage · nightly", workflowTrigger: "nightly" }) }));
    const { onClose } = render(scheduleDeclared);

    // The machine is preselected: it is the only eligible one.
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    // The mapped input is listed read-only; the form offers only the others.
    const mapped = screen.getByText("Set from each event").parentElement!;
    expect(within(mapped).getByText("label")).toBeInTheDocument();
    await screen.findByLabelText("Depth");
    expect(screen.queryByLabelText("Label")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Triage overnight issues" } });
    await user.click(screen.getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const request = createTrigger.mock.calls[0]![0] as CreateTriggerRequest;
    const definition = request.trigger!;
    expect(definition.source).toEqual({ case: "workflowTrigger", value: "nightly" });
    expect(definition.workflow).toBe("triage");
    expect(definition.projectId).toBe("proj-1");
    expect(definition.daemonId).toBe("daemon-1");
    expect(definition.message).toBe("Triage overnight issues");
    expect(definition.filter).toBe("");
    expect(Object.keys(definition.params)).not.toContain("label");
    expect(definition.enabled).toBe(true);
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("requires a prompt and a machine before creating anything", async () => {
    const user = userEvent.setup();
    listDaemons.mockResolvedValue({ daemons: [] });
    render(scheduleDeclared);
    fireEvent.change(await screen.findByLabelText("Prompt"), { target: { value: "" } });
    await user.click(screen.getByRole("button", { name: "Activate" }));
    expect(await screen.findByText("Write the prompt each run starts from.")).toBeInTheDocument();
    expect(screen.getByText("Connect a machine first, or choose No machine.")).toBeInTheDocument();
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("activates with No machine: sends no_machine and no daemon", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "t-2", name: "Triage · nightly", workflowTrigger: "nightly" }) }));
    render(scheduleDeclared);

    const option = await screen.findByRole("option", { name: /No machine/ });
    // The server's no-machine check (it refuses a workflow that needs a
    // machine) is the gate; the option itself is always offered.
    expect(option).toBeEnabled();
    expect(option).toHaveTextContent("No machine (server tools only)");

    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Runs on"), { target: { value: (option as HTMLOptionElement).value } });
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Summarize" } });
    await user.click(screen.getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const definition = (createTrigger.mock.calls[0]![0] as CreateTriggerRequest).trigger!;
    expect(definition.noMachine).toBe(true);
    expect(definition.daemonId).toBe("");
  });

  it("shows a webhook activation's URL and one-time token, resolving a bare path against the API origin, and rotates it", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(
      create(CreateTriggerResponseSchema, {
        trigger: create(TriggerSchema, { id: "t-7", name: "Triage · ci-deploy", workflowTrigger: "ci-deploy", webhookUrl: "/hooks/t-7" }),
        webhook: create(WebhookCredentialSchema, { token: "tok_first", url: "/hooks/t-7/tok_first" }),
      }),
    );
    rotateWebhookToken.mockResolvedValue(
      create(RotateWebhookTokenResponseSchema, {
        trigger: create(TriggerSchema, { id: "t-7", name: "Triage · ci-deploy", webhookUrl: "/hooks/t-7" }),
        webhook: create(WebhookCredentialSchema, { token: "tok_second", url: "/hooks/t-7/tok_second" }),
      }),
    );
    render(webhookDeclared);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Deploy" } });
    // Nothing is mapped here, so the required input is the activation's to set.
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "deploy" } });
    await user.click(screen.getByRole("button", { name: "Activate" }));
    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));

    expect(await screen.findByLabelText("Webhook URL")).toHaveValue("https://api.example.com/hooks/t-7");
    expect(screen.getByLabelText("Token")).toHaveValue("tok_first");
    expect(screen.getByLabelText("URL with token")).toHaveValue("https://api.example.com/hooks/t-7/tok_first");

    await user.click(screen.getByRole("button", { name: "Rotate token" }));
    await user.click(within(screen.getByRole("alertdialog", { name: "Rotate the token?" })).getByRole("button", { name: "Rotate token" }));
    await waitFor(() => expect(screen.getByLabelText("Token")).toHaveValue("tok_second"));
    expect(rotateWebhookToken).toHaveBeenCalledWith(expect.objectContaining({ id: "t-7" }));
  });

  it("shows the server's refusal inline", async () => {
    const user = userEvent.setup();
    createTrigger.mockRejectedValue(new Error('workflow "triage" does not declare a trigger named "nightly"'));
    render(scheduleDeclared);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Go" } });
    await user.click(screen.getByRole("button", { name: "Activate" }));
    expect(await screen.findByText(/does not declare a trigger named "nightly"/)).toHaveAttribute("role", "alert");
  });

  it("needs no prompt when the declared trigger has one: empty follows the workflow's", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "t-2", name: "Triage · nightly", workflowTrigger: "nightly" }) }));
    const { onClose } = render({ ...scheduleDeclared, prompt: "Summarise everything before {{ trigger.scheduled_for }}." } as DeclaredTrigger);

    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    const prompt = screen.getByLabelText("Prompt");
    expect(prompt).toHaveValue("");
    expect(prompt).toHaveAttribute("placeholder", "Summarise everything before {{ trigger.scheduled_for }}.");
    await user.click(screen.getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const definition = (createTrigger.mock.calls[0]![0] as CreateTriggerRequest).trigger!;
    expect(definition.message).toBe("");
    expect(definition.source).toEqual({ case: "workflowTrigger", value: "nightly" });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("in personal mode (a built-in), creates a row with the picked source INLINE, not a declaration", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "t-3", name: "Agent · schedule" }) }));
    const onClose = vi.fn();
    renderAtRoute(
      <ActivateTriggerDialog
        open
        mode="personal"
        onClose={onClose}
        workflowRef="builtin://agent"
        workflowTitle="Agent"
        declared={{ name: "schedule", filter: "", inputs: {}, source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } } } as unknown as DeclaredTrigger}
        defaultProjectId="proj-1"
      />,
    );

    expect(await screen.findByRole("form", { name: "Add a personal trigger" })).toBeInTheDocument();
    // The source is editable here: there is no declaration to hold it.
    expect(screen.getByText("When it runs")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Review yesterday's PRs" } });
    // A personal trigger maps no inputs from an event, so the workflow's
    // required ones are the activator's to fill.
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "daily" } });
    await user.click(screen.getByRole("button", { name: "Add trigger" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const definition = (createTrigger.mock.calls[0]![0] as CreateTriggerRequest).trigger!;
    expect(definition.workflow).toBe("builtin://agent");
    expect(definition.source.case).toBe("schedule");
    expect(definition.source.value).toMatchObject({ cron: ["0 9 * * 1-5"], timezone: "UTC" });
    expect(definition.message).toBe("Review yesterday's PRs");
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("in personal mode, 'Only from' writes the new row's own filter", async () => {
    const user = userEvent.setup();
    listConnections.mockResolvedValue(
      create(ListConnectionsResponseSchema, {
        connections: [create(ConnectionSchema, { id: "conn_slack", integrationId: "slack", name: "Acme", senderId: "U0ME", status: ConnectionStatus.ACTIVE, isDefault: true })],
      }),
    );
    searchCatalog.mockResolvedValue(create(SearchCatalogResponseSchema, { entries: [] }));
    getCatalogEntry.mockRejectedValue(new Error("not in this test's catalog"));
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "t-4", name: "Agent · mention" }) }));
    renderAtRoute(
      <ActivateTriggerDialog
        open
        mode="personal"
        onClose={vi.fn()}
        workflowRef="builtin://agent"
        workflowTitle="Agent"
        declared={{ name: "mention", filter: "", inputs: {}, source: { case: "integration", value: { integration: "slack", events: ["app_mention"], match: {} } } } as unknown as DeclaredTrigger}
        defaultProjectId="proj-1"
      />,
    );

    await user.click(await screen.findByRole("button", { name: "Add me (U0ME)" }));
    await user.type(screen.getByLabelText("Add a sender"), "U0TEAMMATE{Enter}");
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Answer the mention" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "slack" } });
    await user.click(screen.getByRole("button", { name: "Add trigger" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const definition = (createTrigger.mock.calls[0]![0] as CreateTriggerRequest).trigger!;
    expect(definition.source.case).toBe("integration");
    expect(definition.filter).toBe('trigger.sender.verified && trigger.sender.id in ["U0ME", "U0TEAMMATE"]');
  });
});
