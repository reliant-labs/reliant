// Copyright (c) 2025 Reliant Labs

/**
 * WorkflowSetupDialog: one workflow turned on in one project, in three modes
 * (set up, finish setup, settings), with one EnableWorkflow request. The RPC
 * client is the only mock, answering with the real generated messages.
 *
 * Pinned here: the sections and what lands in them (scope inputs first, typed
 * inputs as pickers); required gating through an error summary that takes
 * focus and links to fields; a server refusal mapped to the field it names;
 * finish mode's prefill, banner and focus; settings mode round-tripping what
 * is stored (owner decision O2: Enable updates existing automations); the
 * success view; the O7 notify copy; the webhook credentials shown once.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  ActingIdentity,
  EnableWorkflowResponseSchema,
  ListTriggersResponseSchema,
  TriggerHealthSchema,
  TriggerHealthStatus,
  TriggerSchema,
  WebhookCredentialSchema,
  WorkflowTriggerOutcome,
  WorkflowTriggerResultSchema,
  type EnableWorkflowRequest,
} from "@/gen/reliant/v1/trigger_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { ListConnectionsResponseSchema } from "@/gen/reliant/v1/connection_pb";
import { InputOptionSchema, InputOptionsConnectionState, ListInputOptionsResponseSchema, type ListInputOptionsRequest } from "@/gen/reliant/v1/catalog_pb";
import { GetWorkflowResponseSchema } from "@/gen/reliant/v1/workflow_pb";
import { InputBaseSchema, InputSchema, IntegrationInputConfigSchema, StringInputConfigSchema, WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { jsToProtoValue } from "@/api/proto-utils";
import { triageWorkflowResponse, presetsResponse, worktreesResponse, WORKFLOW_LIST } from "@/components/workflow/run/__tests__/runFormFixtures";
import { renderAtRoute } from "./automationTestUtils";

const enableWorkflow = vi.fn();
const listTriggers = vi.fn();
const getWorkflow = vi.fn();
const listWorkflows = vi.fn();
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();
const listConnections = vi.fn();
const searchCatalog = vi.fn();
const getCatalogEntry = vi.fn();
const listInputOptions = vi.fn();
const listRepos = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    connection: () => ({ listConnections }),
    catalog: () => ({ searchCatalog, getCatalogEntry, listInputOptions }),
    trigger: () => ({ enableWorkflow, listTriggers }),
    workflow: () => ({ getWorkflow, listWorkflows }),
    preset: () => ({ listPresetsForWorkflow, getDefaultPresetsBatch }),
    worktree: () => ({ listWorktrees }),
    daemonRegistry: () => ({ listDaemons }),
    project: () => ({ listProjectDaemons }),
    repo: () => ({ listRepos }),
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

import { WorkflowSetupDialog, type SetupMode } from "../WorkflowSetupDialog";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";

const nightly = {
  name: "nightly",
  filter: "",
  inputs: {},
  prompt: "Sweep overnight.",
  source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } },
} as unknown as DeclaredTrigger;

const hook = { name: "ci-deploy", filter: "", inputs: {}, source: { case: "webhook", value: {} } } as unknown as DeclaredTrigger;

const prOpened = {
  name: "pr-opened",
  filter: "",
  inputs: {},
  prompt: "Review it.",
  source: { case: "integration", value: { integration: "github", events: ["pull_request.opened"], match: {}, matchInput: { repository: "repos" } } },
} as unknown as DeclaredTrigger;

function typedInput(type: string, opts: { multi?: boolean; required?: boolean; minItems?: number; scope?: Record<string, string>; fallback?: unknown; description?: string }) {
  return create(InputSchema, {
    type,
    config: {
      case: "integrationInput",
      value: create(IntegrationInputConfigSchema, {
        base: create(InputBaseSchema, { required: opts.required ?? false, description: opts.description ?? "" }),
        multi: opts.multi ?? false,
        minItems: opts.minItems,
        scope: opts.scope ?? {},
        default: opts.fallback === undefined ? undefined : jsToProtoValue(opts.fallback),
      }),
    },
  });
}

/** A PR-review-shaped workflow: a required repository scope and a label picked from it. */
function reviewWorkflowResponse() {
  return create(GetWorkflowResponseSchema, {
    source: "builtin",
    workflow: create(WorkflowSchema, {
      name: "review",
      inputs: {
        repos: typedInput("github.repository", { multi: true, required: true, minItems: 1, description: "Repositories to review." }),
        skip_label: typedInput("github.label", { scope: { repository: "repos" }, fallback: "skip-review" }),
        guidance: create(InputSchema, {
          type: "string",
          config: { case: "stringInput", value: create(StringInputConfigSchema, { base: create(InputBaseSchema, { description: "Extra guidance" }), default: "" }) },
        }),
      },
    }),
  });
}

function result(trigger: string, outcome: WorkflowTriggerOutcome, extra: { message?: string; token?: string } = {}) {
  return create(WorkflowTriggerResultSchema, {
    trigger,
    outcome,
    message: extra.message ?? "",
    activation:
      outcome === WorkflowTriggerOutcome.ENABLED || outcome === WorkflowTriggerOutcome.UPDATED
        ? create(TriggerSchema, { id: `t-${trigger}`, name: `Triage · ${trigger}`, workflowTrigger: trigger, webhookUrl: `/hooks/t-${trigger}` })
        : undefined,
    webhook: extra.token ? create(WebhookCredentialSchema, { token: extra.token, url: `/hooks/t-${trigger}/${extra.token}` }) : undefined,
  });
}

function activation(trigger: string, opts: { status?: TriggerHealthStatus; enabled?: boolean; params?: Record<string, unknown>; detail?: string; workflow?: string; daemonId?: string } = {}) {
  return create(TriggerSchema, {
    id: `t-${trigger}`,
    name: `Triage · ${trigger}`,
    projectId: "proj-1",
    projectName: "Reliant",
    workflow: opts.workflow ?? "triage",
    workflowTrigger: trigger,
    enabled: opts.enabled ?? true,
    daemonId: opts.daemonId ?? "daemon-1",
    notifyOnComplete: true,
    actingIdentity: ActingIdentity.APP,
    params: Object.fromEntries(Object.entries(opts.params ?? {}).map(([k, v]) => [k, jsToProtoValue(v)])),
    updatedAt: "2026-10-12T09:00:00Z",
    health: create(TriggerHealthSchema, { status: opts.status ?? TriggerHealthStatus.HEALTHY, lastFailureDetail: opts.detail ?? "" }),
  });
}

beforeEach(() => {
  for (const mock of [enableWorkflow, listTriggers, getWorkflow, listWorkflows, listPresetsForWorkflow, getDefaultPresetsBatch, listWorktrees, listDaemons, listProjectDaemons, listConnections, searchCatalog, getCatalogEntry, listInputOptions]) {
    mock.mockReset();
  }
  listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
  // Triage edits code (it needs a machine); review works through GitHub alone.
  listWorkflows.mockResolvedValue({
    ...WORKFLOW_LIST,
    workflows: [
      ...WORKFLOW_LIST.workflows.map((w) => (w.name === "triage" ? { ...w, needsMachine: ["bash"] } : w)),
      { name: "review", source: "builtin", stepCount: 1, nodes: [], edges: [], validationErrors: [], needsMachine: [] },
    ],
  });
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
  listInputOptions.mockImplementation(async (req: ListInputOptionsRequest) => {
    if (req.resolve.length > 0) return create(ListInputOptionsResponseSchema, {});
    const values = req.type === "github.label" ? ["skip-review", "needs-work"] : ["acme/api", "acme/web"];
    return create(ListInputOptionsResponseSchema, {
      options: values.map((value) => create(InputOptionSchema, { value, label: value, group: req.type === "github.label" ? "" : "acme" })),
      connectionState: InputOptionsConnectionState.CONNECTED,
    });
  });
});

function render(declared: DeclaredTrigger[], opts: { mode?: SetupMode; workflowRef?: string; title?: string } = {}) {
  const onClose = vi.fn();
  const onEnabled = vi.fn();
  renderAtRoute(
    <WorkflowSetupDialog
      open
      onClose={onClose}
      onEnabled={onEnabled}
      workflowRef={opts.workflowRef ?? "triage"}
      workflowTitle={opts.title ?? "Triage"}
      declared={declared}
      defaultProjectId="proj-1"
      mode={opts.mode}
    />,
  );
  return { onClose, onEnabled };
}

const sentRequest = () => enableWorkflow.mock.calls[0]![0] as EnableWorkflowRequest;
const machineReady = () => waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));

describe("WorkflowSetupDialog: set up", () => {
  it("turns every trigger on with one request, then says it is on and what happens next", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(
      create(EnableWorkflowResponseSchema, {
        enabled: true,
        results: [result("nightly", WorkflowTriggerOutcome.ENABLED), result("ci-deploy", WorkflowTriggerOutcome.ENABLED, { token: "tok_once" })],
      }),
    );
    const { onEnabled } = render([nightly, hook]);
    expect(await screen.findByRole("heading", { name: "Set up Triage" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "2 triggers" }));
    const list = screen.getByRole("list", { name: "What starts it" });
    expect(within(list).getByText("nightly")).toBeInTheDocument();
    expect(within(list).getByText("ci-deploy")).toBeInTheDocument();
    await machineReady();
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Deploy checks" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "deploy" } });
    await user.click(screen.getByRole("button", { name: "Turn on" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    const request = sentRequest();
    expect(request).toMatchObject({ workflow: "triage", projectId: "proj-1", daemonId: "daemon-1", noMachine: false, message: "Deploy checks" });
    expect(request.overrides).toHaveLength(0);
    expect(request.params.label?.kind).toEqual({ case: "stringValue", value: "deploy" });
    await waitFor(() => expect(onEnabled).toHaveBeenCalled());

    expect(await screen.findByText("Triage is on in Reliant")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "What happens next" })).toBeInTheDocument();
    expect(screen.getByLabelText("Token")).toHaveValue("tok_once");
  });

  it("keeps the primary button enabled; pressing it with gaps opens a focused error summary linking to each field", async () => {
    const user = userEvent.setup();
    listDaemons.mockResolvedValue({ daemons: [] });
    render([hook]);
    await screen.findByLabelText("Label");
    expect(screen.getByTestId("setup-footer-summary")).toHaveTextContent(/required fields? left/);
    const turnOn = screen.getByRole("button", { name: "Turn on" });
    expect(turnOn).toBeEnabled();
    await user.click(turnOn);

    const summary = await screen.findByTestId("error-summary");
    await waitFor(() => expect(summary).toHaveFocus());
    expect(summary).toHaveAttribute("role", "alert");
    const links = within(summary).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual([
      "Fill in Label.",
      "Connect a machine first, or choose No machine.",
      "Write the prompt each run starts from: “ci-deploy” has none of its own.",
    ]);
    expect(screen.getByTestId("setup-footer-summary")).toHaveTextContent("3 things to fix");
    await user.click(links[1]);
    expect(screen.getByLabelText("Runs on")).toHaveFocus();
    expect(enableWorkflow).not.toHaveBeenCalled();
  });

  it("asks for the scope input first, as a picker, and the label picker waits for it", async () => {
    const user = userEvent.setup();
    getWorkflow.mockResolvedValue(reviewWorkflowResponse());
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("pr-opened", WorkflowTriggerOutcome.ENABLED)] }));
    render([prOpened], { workflowRef: "review", title: "PR review" });

    const watches = await screen.findByTestId("section-watches");
    expect(within(watches).getByText("What it watches")).toBeInTheDocument();
    const repos = await within(watches).findByRole("combobox", { name: /Repos/ });
    const label = within(watches).getByRole("combobox", { name: /Skip label/ });
    expect(label).toHaveTextContent("Pick a repository first");
    expect(label).toHaveAttribute("aria-disabled", "true");

    await user.click(screen.getByRole("button", { name: "Turn on" }));
    const summary = await screen.findByTestId("error-summary");
    expect(within(summary).getByRole("link", { name: "Choose at least one repository." })).toBeInTheDocument();

    await waitFor(() => expect(repos).not.toHaveTextContent("Loading"));
    await user.click(repos);
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(label).not.toHaveAttribute("aria-disabled"));
    // It needs no machine, so it runs without one unless told otherwise.
    expect(screen.getByLabelText("Runs on")).toHaveValue("__no_machine__");
    expect(screen.getByText("This workflow needs none: it works through its integrations.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    expect(sentRequest().params.repos?.kind).toEqual({ case: "listValue", value: expect.objectContaining({ values: [expect.objectContaining({ kind: { case: "stringValue", value: "acme/api" } })] }) });
    expect(await screen.findByText(/Runs on the next event .* in acme\/api\./)).toBeInTheDocument();
  });

  it("maps a server refusal to the field it names and keeps every choice", async () => {
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
    const newIssue = { ...prOpened, name: "new-issue", source: { case: "integration", value: { integration: "github", events: ["issues.opened"], match: {} } } } as unknown as DeclaredTrigger;
    const { onEnabled, onClose } = render([nightly, newIssue]);
    await machineReady();
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "bug" } });
    await user.click(screen.getByRole("button", { name: "Turn on" }));

    const summary = await screen.findByTestId("error-summary");
    expect(within(summary).getByText("Nothing was turned on, because “new-issue” could not be.")).toBeInTheDocument();
    const link = within(summary).getByRole("link", { name: /new-issue: connect github first/ });
    expect(link.getAttribute("href")).toMatch(/connection-github$/);
    expect(within(summary).getByText(/Not enabled\./)).toBeInTheDocument();
    expect(onEnabled).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1");
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
    await machineReady();
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "go" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    expect(await screen.findByText("Something is still running that you did not ask for.")).toBeInTheDocument();
    expect(screen.getByText(/Still running\./)).toBeInTheDocument();
    expect(screen.getByText(/Disable it from Automations/)).toBeInTheDocument();
    expect(screen.getByText("ci-deploy: webhook credentials could not be written")).toBeInTheDocument();
  });

  it("shows a request that fails outright in the summary, keeping the choices", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockRejectedValue(new Error("the server is unreachable"));
    render([nightly]);
    await machineReady();
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    expect(await screen.findByText("the server is unreachable")).toBeInTheDocument();
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1");
  });

  it("sends a per-trigger override only for what was changed", async () => {
    const user = userEvent.setup();
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("nightly", WorkflowTriggerOutcome.ENABLED), result("ci-deploy", WorkflowTriggerOutcome.ENABLED)] }));
    render([nightly, hook]);
    await machineReady();
    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "shared prompt" } });
    fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "x" } });
    await user.click(screen.getByRole("button", { name: /Per-trigger exceptions/ }));
    await user.click(screen.getByRole("button", { name: /^nightly/ }));
    const machines = screen.getAllByRole("combobox", { name: "Runs on" });
    expect(machines).toHaveLength(2);
    const noMachine = within(machines[1]!).getByRole("option", { name: /No machine/ }) as HTMLOptionElement;
    fireEvent.change(machines[1]!, { target: { value: noMachine.value } });
    await user.click(screen.getByRole("button", { name: "Turn on" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    expect(sentRequest().overrides).toHaveLength(1);
    expect(sentRequest().overrides[0]).toMatchObject({ trigger: "nightly", noMachine: true });
  });

  it("words the notification as what it adds: failures always notify (O7)", async () => {
    render([nightly]);
    expect(await screen.findByRole("switch", { name: "Also notify me when a run succeeds" })).toBeInTheDocument();
    expect(screen.getByText("Failures always notify you.")).toBeInTheDocument();
  });

  it("offers no per-trigger exceptions for a workflow with one trigger", async () => {
    render([nightly]);
    await screen.findByLabelText("Runs on");
    expect(screen.queryByRole("button", { name: /Per-trigger exceptions/ })).not.toBeInTheDocument();
  });
});

describe("WorkflowSetupDialog: finish setup and settings (O2)", () => {
  it("finish: prefills what was chosen, says what changed, and focuses the missing field", async () => {
    getWorkflow.mockResolvedValue(reviewWorkflowResponse());
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [activation("pr-opened", { workflow: "review", status: TriggerHealthStatus.NEEDS_SETUP, detail: "Repositories is required since this workflow was updated.", params: { guidance: "Be brief" } })],
      }),
    );
    render([prOpened], { workflowRef: "review", title: "PR review" });

    expect(await screen.findByRole("heading", { name: "Finish setting up PR review" })).toBeInTheDocument();
    const banner = await screen.findByTestId("finish-banner");
    expect(banner).toHaveTextContent("Not running yet. Repositories is required since this workflow was updated. Everything else keeps the settings you chose on");
    const repos = await screen.findByRole("combobox", { name: /Repos/ });
    await waitFor(() => expect(repos).toHaveFocus());
    expect(screen.getByRole("button", { name: "Save and turn on" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Reliant bot/ })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Also notify me when a run succeeds" })).toHaveAttribute("aria-checked", "true");
  });

  it("settings: reads the stored setup back and sends it whole, so Enable updates rather than wipes", async () => {
    const user = userEvent.setup();
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, { triggers: [activation("nightly", { params: { label: "bug", depth: 5 } })] }),
    );
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [result("nightly", WorkflowTriggerOutcome.UPDATED)] }));
    render([nightly]);

    expect(await screen.findByRole("heading", { name: "Triage in Reliant" })).toBeInTheDocument();
    // The project is fixed: moving it is a new automation.
    expect(screen.queryByRole("combobox", { name: "Project" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Label")).toHaveValue("bug"));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    const request = sentRequest();
    expect(request.params.label?.kind).toEqual({ case: "stringValue", value: "bug" });
    expect(request.params.depth?.kind).toEqual({ case: "numberValue", value: 5 });
    expect(request.notifyOnComplete).toBe(true);
    expect(request.actingIdentity).toBe(ActingIdentity.APP);
    expect(await screen.findByText("Triage in Reliant is saved")).toBeInTheDocument();
  });

  it("settings on a paused automation saves and resumes, and says so", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [activation("nightly", { enabled: false, params: { label: "bug" } })] }));
    render([nightly]);
    expect(await screen.findByRole("button", { name: "Save and resume" })).toBeInTheDocument();
  });
});

describe("WorkflowSetupDialog: alert triage", () => {
  const alerts = {
    name: "alertmanager",
    filter: "",
    inputs: {},
    prompt: "Triage it.",
    source: { case: "webhook", value: { source: "alerting" } },
  } as unknown as DeclaredTrigger;

  function oncallWorkflowResponse() {
    const str = (description: string) =>
      create(InputSchema, {
        type: "string",
        config: { case: "stringInput", value: create(StringInputConfigSchema, { base: create(InputBaseSchema, { description }), default: "" }) },
      });
    return create(GetWorkflowResponseSchema, {
      source: "builtin",
      workflow: create(WorkflowSchema, { name: "oncall", inputs: { default_repo: str("Fallback repository") } }),
    });
  }

  it("connects without a token and fills default_repo with the project's one repository", async () => {
    const user = userEvent.setup();
    getWorkflow.mockResolvedValue(oncallWorkflowResponse());
    listRepos.mockResolvedValue({ repos: [{ id: "r", projectId: "proj-1", name: "shop", relativePath: "", remoteUrl: "git@github.com:acme/shop.git" }] });
    const managed = create(WorkflowTriggerResultSchema, {
      trigger: "alertmanager",
      outcome: WorkflowTriggerOutcome.ENABLED,
      activation: create(TriggerSchema, { id: "t-a", name: "Oncall · alertmanager", workflowTrigger: "alertmanager", webhookUrl: "/hooks/t-a", webhookManaged: true }),
    });
    enableWorkflow.mockResolvedValue(create(EnableWorkflowResponseSchema, { enabled: true, results: [managed] }));
    render([alerts], { workflowRef: "oncall", title: "Oncall" });

    const note = await screen.findByTestId("alert-triage-note");
    expect(note).toHaveTextContent("connects it to your organization's alerts");
    await waitFor(() => expect(note).toHaveTextContent("acme/shop"));
    await machineReady();
    await user.click(screen.getByRole("button", { name: "Turn on" }));

    await waitFor(() => expect(enableWorkflow).toHaveBeenCalledTimes(1));
    expect(sentRequest().params.default_repo?.kind).toEqual({ case: "stringValue", value: "acme/shop" });
    expect(await screen.findByText(/Connected to your organization's alerts/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Token")).not.toBeInTheDocument();
  });
});
