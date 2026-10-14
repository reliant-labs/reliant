// Copyright (c) 2025 Reliant Labs

/**
 * Enabling a workflow turns on every trigger it declares, in one step:
 * one set of choices, one request, all or nothing. The RPC client is the only
 * mock, answering with the real generated messages.
 *
 * Pinned here: the workflow's own inputs ("activation params") are asked for
 * and sent; the connected account is asked for once per integration; a missing
 * connection and a server refusal are shown against the trigger that caused
 * them, with the others reported as held back; a failed undo is shown as the
 * one thing to act on; a webhook's URL and token are shown once.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  EnableWorkflowResponseSchema,
  TriggerSchema,
  WebhookCredentialSchema,
  WorkflowTriggerOutcome,
  WorkflowTriggerResultSchema,
  type EnableWorkflowRequest,
} from "@/gen/reliant/v1/trigger_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { ConnectionAuthKind, IntegrationAuthMethodSchema, ListConnectionsResponseSchema } from "@/gen/reliant/v1/connection_pb";
import { CatalogConnectionRequirementSchema, CatalogEntryKind, CatalogEntrySchema, CatalogEntrySummarySchema, CatalogIntegrationSchema, GetCatalogEntryResponseSchema, SearchCatalogResponseSchema } from "@/gen/reliant/v1/catalog_pb";
import { triageWorkflowResponse, presetsResponse, worktreesResponse, WORKFLOW_LIST } from "@/components/workflow/run/__tests__/runFormFixtures";
import { renderAtRoute } from "./automationTestUtils";

const enableWorkflow = vi.fn();
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
    trigger: () => ({ enableWorkflow }),
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

import { EnableWorkflowDialog } from "../EnableWorkflowDialog";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";

const nightly = {
  name: "nightly",
  filter: "",
  inputs: {},
  prompt: "Sweep overnight.",
  source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } },
} as unknown as DeclaredTrigger;

const hook = {
  name: "ci-deploy",
  filter: "",
  inputs: {},
  source: { case: "webhook", value: {} },
} as unknown as DeclaredTrigger;

const newIssue = {
  name: "new-issue",
  filter: "",
  inputs: { label: "{{ trigger.payload.data.issue.title }}" },
  prompt: "Triage it.",
  source: { case: "integration", value: { integration: "github", events: ["issues.opened"], match: {} } },
} as unknown as DeclaredTrigger;

function result(trigger: string, outcome: WorkflowTriggerOutcome, extra: { message?: string; id?: string; token?: string } = {}) {
  return create(WorkflowTriggerResultSchema, {
    trigger,
    outcome,
    message: extra.message ?? "",
    activation: outcome === WorkflowTriggerOutcome.ENABLED
      ? create(TriggerSchema, { id: extra.id ?? `t-${trigger}`, name: `Triage · ${trigger}`, workflowTrigger: trigger, webhookUrl: `/hooks/t-${trigger}` })
      : undefined,
    webhook: extra.token ? create(WebhookCredentialSchema, { token: extra.token, url: `/hooks/t-${trigger}/${extra.token}` }) : undefined,
  });
}

beforeEach(() => {
  for (const mock of [enableWorkflow, getWorkflow, listPresetsForWorkflow, getDefaultPresetsBatch, listWorktrees, listDaemons, listProjectDaemons, listConnections, searchCatalog, getCatalogEntry]) {
    mock.mockReset();
  }
  getWorkflow.mockResolvedValue(triageWorkflowResponse());
  listPresetsForWorkflow.mockResolvedValue(presetsResponse());
  getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  listWorktrees.mockImplementation(async (req: { projectId: string }) => worktreesResponse(req.projectId));
  listDaemons.mockResolvedValue({ daemons: [create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE })] });
  listProjectDaemons.mockResolvedValue({
    projectDaemons: [create(ProjectDaemonSchema, { projectId: "proj-1", daemonId: "daemon-1", installState: ProjectInstallState.INSTALLED })],
  });
  listConnections.mockResolvedValue(create(ListConnectionsResponseSchema, { connections: [] }));
  searchCatalog.mockResolvedValue({ entries: [], nextPageToken: "", totalSize: 0, categoryFacets: [] });
  getCatalogEntry.mockRejectedValue(new Error("not in this test's catalog"));
});

/** A catalog with one GitHub trigger type, connectable by OAuth, so the account picker has something to show. */
function stubGithubCatalog() {
  searchCatalog.mockResolvedValue(
    create(SearchCatalogResponseSchema, {
      entries: [create(CatalogEntrySummarySchema, { ref: "github/issue.opened@1", kind: CatalogEntryKind.TRIGGER, id: "issue.opened", displayName: "Issue opened", integration: create(CatalogIntegrationSchema, { id: "github", displayName: "GitHub" }) })],
    }),
  );
  getCatalogEntry.mockResolvedValue(
    create(GetCatalogEntryResponseSchema, {
      entry: create(CatalogEntrySchema, {
        summary: create(CatalogEntrySummarySchema, { ref: "github/issue.opened@1", kind: CatalogEntryKind.TRIGGER, id: "issue.opened", displayName: "Issue opened", connectionRequired: true, integration: create(CatalogIntegrationSchema, { id: "github", displayName: "GitHub" }) }),
        connection: create(CatalogConnectionRequirementSchema, { required: true, methods: [create(IntegrationAuthMethodSchema, { kind: ConnectionAuthKind.OAUTH2, available: true })] }),
      }),
    }),
  );
}

function render(declared: DeclaredTrigger[]) {
  const onClose = vi.fn();
  const onEnabled = vi.fn();
  renderAtRoute(
    <EnableWorkflowDialog open onClose={onClose} onEnabled={onEnabled} workflowRef="triage" workflowTitle="Triage" declared={declared} defaultProjectId="proj-1" />,
  );
  return { onClose, onEnabled };
}

const sentRequest = () => enableWorkflow.mock.calls[0]![0] as EnableWorkflowRequest;

describe("EnableWorkflowDialog", () => {
  it("lists every trigger it will turn on and enables them all with one request", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(
      create(EnableWorkflowResponseSchema, {
        enabled: true,
        results: [result("nightly", WorkflowTriggerOutcome.ENABLED), result("ci-deploy", WorkflowTriggerOutcome.ENABLED, { token: "tok_once" })],
      }),
    );
    const { onEnabled } = render([nightly, hook]);

    const list = await screen.findByRole("list", { name: "Triggers this will turn on" });
    expect(within(list).getByText("nightly")).toBeInTheDocument();
    expect(within(list).getByText("ci-deploy")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    // ci-deploy declares no prompt, so one is needed, and the required input too.
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Deploy checks" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "deploy" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    expect(enableWorkflow).toHaveBeenCalledTimes(1);
    const request = sentRequest();
    expect(request.workflow).toBe("triage");
    expect(request.projectId).toBe("proj-1");
    expect(request.daemonId).toBe("daemon-1");
    expect(request.noMachine).toBe(false);
    expect(request.message).toBe("Deploy checks");
    expect(request.overrides).toHaveLength(0);
    await waitFor(() => expect(onEnabled).toHaveBeenCalled());

    // The one-time webhook credentials are shown with the success.
    expect(await screen.findByRole("heading", { name: "Triage is on" })).toBeInTheDocument();
    expect(screen.getByLabelText("Token")).toHaveValue("tok_once");
    expect(screen.getByLabelText("Webhook URL")).toHaveValue("https://api.example.com/hooks/t-ci-deploy");
  });

  it("asks for the workflow's own inputs and sends them as the activations' params", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("nightly", WorkflowTriggerOutcome.ENABLED)] }));
    render([nightly]);

    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    // The inputs form is the run form's: typed, with the workflow's defaults.
    const depth = await screen.findByLabelText("Depth");
    fireEvent.change(depth, { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "bug" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    const params = sentRequest().params;
    expect(params.label?.kind).toEqual({ case: "stringValue", value: "bug" });
    expect(params.depth?.kind).toEqual({ case: "numberValue", value: 4 });
  });

  it("does not ask for an input every trigger maps from its event", async () => {
    render([{ ...newIssue, inputs: { label: "{{ x }}" } } as DeclaredTrigger]);
    await screen.findByLabelText("Depth");
    expect(screen.queryByLabelText("Label")).not.toBeInTheDocument();
  });

  it("requires a prompt when a trigger declares none, and a machine, before sending anything", async () => {
    const user = userEvent.setup();
    listDaemons.mockResolvedValue({ daemons: [] });
    render([hook]);
    await screen.findByLabelText("Prompt");
    await user.click(screen.getByRole("button", { name: "Enable" }));

    expect(await screen.findByText(/Write the prompt each run starts from: “ci-deploy” has none of its own\./)).toBeInTheDocument();
    expect(screen.getByText("Connect a machine first, or choose No machine.")).toBeInTheDocument();
    expect(enableWorkflow).not.toHaveBeenCalled();
  });

  it("needs no prompt when every trigger brings its own, and says the box is unused", async () => {
    render([nightly]);
    expect(await screen.findByText("Every trigger brings its own prompt, and these words are not used.")).toBeInTheDocument();
  });

  it("enables with No machine: sends no_machine and no daemon", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("nightly", WorkflowTriggerOutcome.ENABLED)] }));
    render([nightly]);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    const option = screen.getByRole("option", { name: /No machine/ });
    fireEvent.change(screen.getByLabelText("Runs on"), { target: { value: (option as HTMLOptionElement).value } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    expect(sentRequest().noMachine).toBe(true);
    expect(sentRequest().daemonId).toBe("");
  });

  it("asks for the connected account once per integration", async () => {
    stubGithubCatalog();
    render([newIssue, { ...newIssue, name: "new-pr", prompt: "Review." } as DeclaredTrigger, nightly]);
    expect(await screen.findByText(/Events reach “new-issue”, “new-pr” only through this account\./)).toBeInTheDocument();
    // One picker for the two GitHub triggers, and none for the schedule.
    expect(await screen.findAllByText("GitHub isn't connected yet.")).toHaveLength(1);
    expect(screen.getByRole("button", { name: /Connect GitHub/ })).toBeInTheDocument();
  });

  it("says plainly when the catalog cannot say how an integration connects, instead of loading forever", async () => {
    render([newIssue]);
    expect(await screen.findByText(/Couldn't look up how github connects\./)).toBeInTheDocument();
    expect(screen.queryByText("Loading github…")).not.toBeInTheDocument();
  });

  it("shows a refusal against the trigger that caused it, and says the others were held back", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(
      create(EnableWorkflowResponseSchema, {
        enabled: false,
        results: [
          result("nightly", WorkflowTriggerOutcome.NOT_ENABLED, { message: "Not enabled because another trigger of this workflow could not be." }),
          result("new-issue", WorkflowTriggerOutcome.FAILED, { message: "connect github first: an integration trigger listens through one of your connections" }),
        ],
      }),
    );
    const { onEnabled, onClose } = render([nightly, { ...newIssue, inputs: {} } as DeclaredTrigger]);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(await screen.findByLabelText("Depth"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "bug" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByText("Nothing was turned on, because “new-issue” could not be.")).toBeInTheDocument());
    const rows = within(screen.getByRole("list", { name: "What happened to each trigger" }));
    expect(rows.getByText(/connect github first/)).toBeInTheDocument();
    expect(rows.getByText(/Could not enable\./)).toBeInTheDocument();
    expect(rows.getByText(/Not enabled\./)).toBeInTheDocument();
    expect(screen.getByText(/Enabling is all or nothing, so none of the others were started either\./)).toBeInTheDocument();

    // The dialog stays open on the same choices; nothing reported as enabled.
    expect(onEnabled).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Triage is on" })).not.toBeInTheDocument();
  });

  it("flags a trigger that could not be switched back off as still running", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(
      create(EnableWorkflowResponseSchema, {
        enabled: false,
        results: [
          result("nightly", WorkflowTriggerOutcome.LEFT_ENABLED, { message: "Enabling failed and this trigger could not be switched back off: temporal is down. Disable it from Automations." }),
          result("ci-deploy", WorkflowTriggerOutcome.FAILED, { message: "webhook credentials could not be written" }),
        ],
      }),
    );
    render([nightly, hook]);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "go" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    expect(await screen.findByText("Something is still running that you did not ask for.")).toBeInTheDocument();
    expect(screen.getByText(/Still running\./)).toBeInTheDocument();
    expect(screen.getByText(/Disable it from Automations/)).toBeInTheDocument();
    expect(screen.queryByText(/Enabling is all or nothing/)).not.toBeInTheDocument();
  });

  it("shows a request that fails outright inline, keeping the choices", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockRejectedValue(new Error("the server is unreachable"));
    render([nightly]);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: "Enable" }));
    expect(await screen.findByText("the server is unreachable")).toHaveAttribute("role", "alert");
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1");
  });

  it("sends a per-trigger override only for what was changed", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("nightly", WorkflowTriggerOutcome.ENABLED), result("ci-deploy", WorkflowTriggerOutcome.ENABLED)] }));
    render([nightly, hook]);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "shared prompt" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });

    // Collapsed by default: the common case is one screen.
    expect(screen.getAllByRole("combobox", { name: "Runs on" })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: /nightly/ }));
    const machines = screen.getAllByRole("combobox", { name: "Runs on" });
    expect(machines).toHaveLength(2);
    const override = machines[1]!;
    expect(within(override).getByRole("option", { name: "Same as above" })).toBeInTheDocument();
    const noMachine = within(override).getByRole("option", { name: /No machine/ }) as HTMLOptionElement;
    fireEvent.change(override, { target: { value: noMachine.value } });
    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    const request = sentRequest();
    expect(request.noMachine).toBe(false);
    expect(request.daemonId).toBe("daemon-1");
    expect(request.overrides).toHaveLength(1);
    expect(request.overrides[0]).toMatchObject({ trigger: "nightly", noMachine: true });
  });

  it("offers no per-trigger section for a workflow with one trigger", async () => {
    render([nightly]);
    await screen.findByLabelText("Runs on");
    expect(screen.queryByText("Advanced: per trigger")).not.toBeInTheDocument();
  });
});
