// Copyright (c) 2025 Reliant Labs

/**
 * The automation dialog with its own WHEN (an inline trigger row): what the
 * person sees first, what is inferred, and the CreateTrigger / UpdateTrigger
 * request it builds. The RPC client is the only mock.
 *
 * Pinned: When comes first and defaults to every weekday at 09:00; the
 * common case needs only the prompt (name, project and machine are
 * inferred); the cut fields are gone; an edit sends back what the form does
 * not edit; a webhook's URL and token are shown once on create; "Describe it"
 * fills the form and creates nothing.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { serviceHandlers } from "@/test/serviceTransport";
import { mockTransport } from "@/lib/test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  AutomationDraftSchema,
  ActingIdentity,
  IntegrationSourceSchema,
  CreateTriggerResponseSchema,
  DraftAutomationResponseSchema,
  ListTriggersResponseSchema,
  ScheduleSourceSchema,
  PreviewReplayResponseSchema,
  TriggerDefinitionSchema,
  TriggerOverlapPolicy,
  TriggerSchema,
  UpdateTriggerResponseSchema,
  WebhookCredentialSchema,
  type CreateTriggerRequest,
  type UpdateTriggerRequest,
} from "@/gen/reliant/v1/trigger_pb";
import { ChatSchema } from "@/gen/reliant/v1/chat_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { GetWorkflowResponseSchema, WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";
import { WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { jsToProtoValue } from "@/api/proto-utils";
import { triggerFromProto } from "@/hooks/trigger-model";
import { worktreesResponse } from "@/components/workflow/run/__tests__/runFormFixtures";
import { renderAtRoute } from "../../__tests__/automationTestUtils";

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));

const listChats = vi.fn();
const createTrigger = vi.fn();
const updateTrigger = vi.fn();
const previewReplay = vi.fn();
const fireTrigger = vi.fn();
const setTriggerEnabled = vi.fn();
const listTriggers = vi.fn();
const draftAutomation = vi.fn();
const listWorkflows = vi.fn();
const getWorkflow = vi.fn();
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const getWorktree = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();
const listConnections = vi.fn(async () => ({ connections: [] }));
const searchCatalog = vi.fn(async () => ({ entries: [], nextPageToken: "", totalSize: 0, categoryFacets: [] }));

vi.mock("@/lib/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/connect")>()),
  reliantApiBaseUrl: () => "https://api.example.com",
}));

setTransport(
  mockTransport({
    ...serviceHandlers("ChatService", { listChats }),
    ...serviceHandlers("ConnectionService", { listConnections }),
    ...serviceHandlers("CatalogService", { searchCatalog }),
    ...serviceHandlers("TriggerService", { createTrigger, updateTrigger, listTriggers, draftAutomation, previewReplay, fireTrigger, setTriggerEnabled }),
    ...serviceHandlers("WorkflowService", { listWorkflows, getWorkflow }),
    ...serviceHandlers("PresetService", { listPresetsForWorkflow, getDefaultPresetsBatch }),
    ...serviceHandlers("WorktreeService", { listWorktrees, getWorktree }),
    ...serviceHandlers("DaemonRegistryService", { listDaemons }),
    ...serviceHandlers("ProjectService", { listProjectDaemons }),
  }),
);

// The project list the dialog names projects from (ListProjects, through useProjects).
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({
    projects: [
      { id: "proj-1", name: "Reliant" },
      { id: "proj-2", name: "Forge" },
    ],
    isLoading: false,
    error: null,
    refetch: () => undefined,
  }),
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    currentProject: { id: "proj-1", name: "Reliant" },
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { InlineAutomationForm } from "../InlineAutomationForm";

const fill = (element: HTMLElement, value: string) => fireEvent.change(element, { target: { value } });

beforeEach(() => {
  for (const mock of [createTrigger, updateTrigger, listTriggers, draftAutomation, listWorkflows, getWorkflow, listPresetsForWorkflow, getDefaultPresetsBatch, listWorktrees, getWorktree, listDaemons, listProjectDaemons]) {
    mock.mockReset();
  }
  listChats.mockResolvedValue({ chats: [create(ChatSchema, { id: "recent", projectId: "proj-1", activeDaemonId: "daemon-1", updatedAt: "2026-10-27T12:00:00Z" })] });
  listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
  listWorkflows.mockResolvedValue({
    workflows: [
      { name: "triage", source: "project", stepCount: 2, nodes: [], edges: [], validationErrors: [], status: WorkflowDraftStatus.COMPLETE, needsMachine: ["bash"] },
      { name: "digest", source: "project", stepCount: 1, nodes: [], edges: [], validationErrors: [], status: WorkflowDraftStatus.COMPLETE, needsMachine: [] },
    ],
    invalidWorkflows: [],
  });
  getWorkflow.mockImplementation(async (request: { name: string }) =>
    create(GetWorkflowResponseSchema, { source: "project", workflow: create(WorkflowSchema, { name: request.name, inputs: {} }) }),
  );
  listPresetsForWorkflow.mockResolvedValue({ presets: [], invalidPresets: [] });
  getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  listWorktrees.mockImplementation(async (request: { projectId: string }) => worktreesResponse(request.projectId));
  // Two machines with the project, the laptop online: it is the one in use.
  listDaemons.mockResolvedValue({
    daemons: [
      create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE }),
      create(DaemonInfoSchema, { daemonId: "daemon-2", hostname: "cloud-box", status: DaemonStatus.SUSPENDED }),
    ],
  });
  listProjectDaemons.mockResolvedValue({
    projectDaemons: [
      create(ProjectDaemonSchema, { projectId: "proj-1", daemonId: "daemon-1", installState: ProjectInstallState.INSTALLED }),
      create(ProjectDaemonSchema, { projectId: "proj-1", daemonId: "daemon-2", installState: ProjectInstallState.INSTALLED }),
    ],
  });
});

const created = () =>
  create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "new-1", name: "Weekdays 9 AM · Summarise failed CI", projectId: "proj-1" }) });
const sentCreate = () => createTrigger.mock.calls[0]![0] as CreateTriggerRequest;
const machineReady = () => waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));

describe("InlineAutomationForm: new", () => {
  it("keeps the default-agent identity reversible after choosing You", async () => {
    createTrigger.mockResolvedValue(created());
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} prefill={{ message: "Triage" }} />);
    await machineReady();
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    await userEvent.click(screen.getByRole("radio", { name: /You/ }));
    expect(screen.getByRole("radio", { name: /You/ })).toBeChecked();
    await userEvent.click(screen.getByRole("radio", { name: /Reliant bot/ }));
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    expect(sentCreate().trigger!.actingIdentity).toBe(ActingIdentity.APP);
  });

  it("can change a stored owner default-agent automation back to the bot", async () => {
    const stored = triggerFromProto(create(TriggerSchema, {
      id: "saved", name: "Saved schedule", projectId: "proj-1", daemonId: "daemon-1", enabled: true,
      message: "Triage", actingIdentity: ActingIdentity.OWNER,
      source: { case: "schedule", value: { cron: ["0 9 * * 1-5"] } },
    }));
    updateTrigger.mockResolvedValue(create(UpdateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "saved" }) }));
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} trigger={stored} />);
    await machineReady();
    // A stored non-default actor opens Advanced automatically.
    expect(screen.getByRole("radio", { name: /You/ })).toBeChecked();
    await userEvent.click(screen.getByRole("radio", { name: /Reliant bot/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(updateTrigger).toHaveBeenCalledTimes(1));
    expect((updateTrigger.mock.calls[0]![0] as UpdateTriggerRequest).trigger!.actingIdentity).toBe(ActingIdentity.APP);
  });

  it.each([
    ["github", false, ActingIdentity.APP],
    ["github", true, ActingIdentity.OWNER],
    ["slack", false, ActingIdentity.APP],
    ["slack", true, ActingIdentity.OWNER],
  ] as const)("defaults a new %s source to the bot and preserves explicit owner (%s)", async (integration, chooseOwner, expected) => {
    createTrigger.mockResolvedValue(created());
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} prefill={{ message: "Triage", source: {
      kind: "passthrough", arm: { case: "integration", value: create(IntegrationSourceSchema, { integration, events: ["issues.opened"] }) },
    } }} />);
    await machineReady();
    // GitHub writes of a Slack-triggered automation are the bot's too.
    expect(screen.getByRole("radio", { name: /Reliant bot/ })).toBeChecked();
    if (chooseOwner) await userEvent.click(screen.getByRole("radio", { name: /You/ }));
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    expect((createTrigger.mock.calls[0]![0] as CreateTriggerRequest).trigger!.actingIdentity).toBe(expected);
    // ~0.4s idle; a saturated merge-gate runner pushed it past the 5s default.
  }, 20_000);

  it.each([ActingIdentity.OWNER, ActingIdentity.APP])("preserves stored GitHub actor %s when editing", async (identity) => {
    const stored = triggerFromProto(create(TriggerSchema, {
      id: "saved", name: "Saved GitHub", projectId: "proj-1", daemonId: "daemon-1", enabled: true,
      message: "Triage", actingIdentity: identity,
      source: { case: "integration", value: { integration: "github", events: ["issues.opened"] } },
    }));
    updateTrigger.mockResolvedValue(create(UpdateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "saved" }) }));
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} trigger={stored} />);
    await machineReady();
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(updateTrigger).toHaveBeenCalledTimes(1));
    expect((updateTrigger.mock.calls[0]![0] as UpdateTriggerRequest).trigger!.actingIdentity).toBe(identity);
  });

  it("asks When first, then what to do; everything else is inferred", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(created());
    const onClose = vi.fn();
    renderAtRoute(<InlineAutomationForm onClose={onClose} />);

    const form = await screen.findByRole("form", { name: "New automation" });
    const headings = within(form).getAllByRole("heading").map((h) => h.textContent);
    expect(headings.slice(0, 2)).toEqual(["When", "Do"]);
    expect(screen.getByLabelText("What starts it")).toHaveValue("schedule");
    expect(screen.getByLabelText("Repeat")).toHaveValue("weekdays");
    expect(screen.getByLabelText("At")).toHaveValue("09:00");
    expect(screen.getByLabelText("Workflow")).toHaveValue("");
    expect(within(screen.getByLabelText("Workflow")).getByRole("option", { name: "Agent (your default)" })).toBeInTheDocument();
    // The cut fields are gone.
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
    expect(screen.queryByText(/Catch-up window/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Also every/)).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Enabled" })).not.toBeInTheDocument();

    // The machine in use is the default, shown as one line.
    await machineReady();
    expect(screen.getByTestId("where-summary")).toHaveTextContent("Runs in Reliant · on laptop · main checkout");

    fill(screen.getByLabelText("What should it do?"), "Summarise failed CI runs. Open an issue for each.");
    expect(screen.getByTestId("automation-name")).toHaveTextContent("Weekdays 9 AM · Summarise failed CI runs");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const def = sentCreate().trigger!;
    expect(def).toMatchObject({
      name: "Weekdays 9 AM · Summarise failed CI runs",
      projectId: "proj-1",
      workflow: "",
      daemonId: "daemon-1",
      noMachine: false,
      message: "Summarise failed CI runs. Open an issue for each.",
    });
    expect(def.source.case).toBe("schedule");
    expect(def.source.case === "schedule" && def.source.value.cron).toEqual(["0 9 * * 1-5"]);
    expect(def.source.case === "schedule" && def.source.value.catchupWindow).toBeUndefined();
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("validates on blur, links the error, and clears it while fixing the field", async () => {
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    await machineReady();
    const prompt = screen.getByLabelText("What should it do?");
    expect(prompt).toHaveAttribute("aria-invalid", "false");
    expect(screen.queryByText("Say what each run should do.")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("1 required field left");
    fireEvent.blur(prompt);
    expect(prompt).toHaveAttribute("aria-invalid", "true");
    const error = screen.getByText("Say what each run should do.");
    expect(prompt.getAttribute("aria-describedby")?.split(" ")).toContain(error.id);
    fill(prompt, "Summarise CI");
    expect(prompt).toHaveAttribute("aria-invalid", "false");
    expect(screen.queryByText("Say what each run should do.")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Ready to save");
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("validates the When group only on blur and clears it on correction", async () => {
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    await machineReady();
    fill(screen.getByLabelText("At"), "");
    const when = screen.getByRole("group", { name: "When it runs" });
    expect(when).toHaveAttribute("aria-invalid", "false");
    fireEvent.blur(screen.getByLabelText("At"));
    expect(when).toHaveAttribute("aria-invalid", "true");
    expect(document.getElementById(when.getAttribute("aria-describedby")!)).toHaveAttribute("role", "alert");
    fill(screen.getByLabelText("At"), "10:00");
    expect(when).toHaveAttribute("aria-invalid", "false");
    expect(when).not.toHaveAttribute("aria-describedby");
  });

  it("says what is missing instead of creating", async () => {
    const user = userEvent.setup();
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    await machineReady();
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("Say what each run should do.")).toBeInTheDocument();
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("defaults from the newest available chat in this project, not the selected machine", async () => {
    listChats.mockResolvedValue({ chats: [
      create(ChatSchema, { id: "other-project", projectId: "proj-2", activeDaemonId: "daemon-1", updatedAt: "2026-10-29T12:00:00Z" }),
      create(ChatSchema, { id: "missing-machine", projectId: "proj-1", activeDaemonId: "deleted", updatedAt: "2026-10-28T12:00:00Z" }),
      create(ChatSchema, { id: "recent", projectId: "proj-1", activeDaemonId: "daemon-2", updatedAt: "2026-10-27T12:00:00Z" }),
    ] });
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-2"));
    expect(listChats).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1" }));
  });

  it("runs with no machine when the chosen workflow needs none", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(created());
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} workflowRef="digest" workflowTitle="Digest" />);
    await screen.findByTestId("where-summary");
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("__no_machine__"));
    expect(screen.getByTestId("where-summary")).toHaveTextContent("with no machine");
    fill(screen.getByLabelText("What should it do?"), "Post the digest.");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    expect(sentCreate().trigger).toMatchObject({ workflow: "digest", noMachine: true, daemonId: "" });
  });

  it("creates a webhook and shows its URL and token once", async () => {
    const user = userEvent.setup();
    createTrigger.mockResolvedValue(
      create(CreateTriggerResponseSchema, {
        trigger: create(TriggerSchema, { id: "hook-1", name: "On webhook · Deploy", projectId: "proj-1", webhookUrl: "/hooks/hook-1", source: { case: "webhook", value: {} } }),
        webhook: create(WebhookCredentialSchema, { token: "tok_once", url: "/hooks/hook-1/tok_once" }),
      }),
    );
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    await machineReady();
    fireEvent.change(screen.getByLabelText("What starts it"), { target: { value: "webhook" } });
    fill(screen.getByLabelText("What should it do?"), "Deploy what was pushed.");
    expect(screen.getByText(/with the request body attached/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create and show URL" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    expect(sentCreate().trigger!.source.case).toBe("webhook");
    expect(await screen.findByRole("heading", { name: "On webhook · Deploy is on" })).toBeInTheDocument();
    expect(screen.getByLabelText("Token")).toHaveValue("tok_once");
    expect((screen.getByLabelText("Try it") as HTMLInputElement).value).toContain("Authorization: Bearer tok_once");
    previewReplay.mockResolvedValue(create(PreviewReplayResponseSchema, { prompt: 'Deploy what was pushed. <trigger_event>{"body":{"hello":"world"}}</trigger_event>' }));
    await user.click(screen.getByRole("button", { name: "Send a test event" }));
    expect(screen.getByText(/Read-only preview: no workflow runs/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Preview message" }));
    expect(await screen.findByText(/Deploy what was pushed. <trigger_event>/)).toBeInTheDocument();
    expect(previewReplay).toHaveBeenCalledWith(expect.objectContaining({ triggerId: "hook-1", replay: expect.objectContaining({ payload: { body: { hello: "world" } } }) }));
    expect(createTrigger).toHaveBeenCalledTimes(1);
    expect(fireTrigger).not.toHaveBeenCalled();
    expect(setTriggerEnabled).not.toHaveBeenCalled();
  });

  it("Describe it fills the form from a draft and creates nothing", async () => {
    const user = userEvent.setup();
    draftAutomation.mockResolvedValue(
      create(DraftAutomationResponseSchema, {
        draft: create(AutomationDraftSchema, {
          definition: create(TriggerDefinitionSchema, {
            message: "Check the uptime dashboard and report any outage.",
            source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: [], interval: "30m" }) },
          }),
          summary: "Every 30 minutes, check uptime",
          assumptions: ["Assumed the uptime dashboard is the team's status page"],
        }),
      }),
    );
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Describe the automation"), "every half hour check uptime");
    await user.click(screen.getByRole("button", { name: "Draft it" }));

    await waitFor(() => expect(screen.getByLabelText("What should it do?")).toHaveValue("Check the uptime dashboard and report any outage."));
    expect(draftAutomation).toHaveBeenCalledWith(expect.objectContaining({ description: "every half hour check uptime", projectId: "proj-1" }));
    expect(screen.getByLabelText("Repeat")).toHaveValue("interval");
    expect(screen.getByText("Assumed the uptime dashboard is the team's status page")).toBeInTheDocument();
    expect(screen.getByText(/nothing is created until you press Create/)).toBeInTheDocument();
    expect(createTrigger).not.toHaveBeenCalled();
  });
});

describe("InlineAutomationForm: edit", () => {
  function stored() {
    return triggerFromProto(
      create(TriggerSchema, {
        id: "trig-9",
        name: "Nightly deps",
        projectId: "proj-1",
        worktreeId: "wt-1",
        enabled: true,
        workflow: "triage",
        presets: { "": "fast" },
        params: { depth: jsToProtoValue(2) },
        message: "Bump dependencies",
        daemonId: "daemon-2",
        source: {
          case: "schedule",
          value: create(ScheduleSourceSchema, { cron: ["0 2 * * *"], interval: "6h", timezone: "Europe/Berlin", overlap: TriggerOverlapPolicy.ALLOW, catchupWindow: "30m" }),
        },
      }),
    );
  }

  it("keeps everything it does not edit: catch-up, the extra interval, overlap, workspace and inputs", async () => {
    const user = userEvent.setup();
    updateTrigger.mockResolvedValue(create(UpdateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "trig-9", name: "Nightly deps" }) }));
    getWorktree.mockRejectedValue(new Error("not needed here"));
    renderAtRoute(<InlineAutomationForm onClose={vi.fn()} trigger={stored()} />);

    expect(await screen.findByRole("heading", { name: "Edit automation" })).toBeInTheDocument();
    expect(screen.getByTestId("automation-name")).toHaveTextContent("Nightly deps");
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-2");
    fill(screen.getByLabelText("What should it do?"), "Bump dependencies and open a PR");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(updateTrigger).toHaveBeenCalledTimes(1));
    const req = updateTrigger.mock.calls[0]![0] as UpdateTriggerRequest;
    expect(req.id).toBe("trig-9");
    expect(req.trigger).toMatchObject({ name: "Nightly deps", worktreeId: "wt-1", workflow: "triage", daemonId: "daemon-2", message: "Bump dependencies and open a PR", presets: { "": "fast" } });
    expect(req.trigger!.params.depth?.kind).toEqual({ case: "numberValue", value: 2 });
    const source = req.trigger!.source;
    expect(source.case).toBe("schedule");
    if (source.case === "schedule") {
      expect(source.value).toMatchObject({ cron: ["0 2 * * *"], interval: "6h", timezone: "Europe/Berlin", overlap: TriggerOverlapPolicy.ALLOW, catchupWindow: "30m" });
    }
  });
});
