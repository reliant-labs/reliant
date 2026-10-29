// Copyright (c) 2025 Reliant Labs

/**
 * Run now — the form generated from a workflow's inputs, the RunNow request it
 * builds, the automation-only and No machine cases, and replaying a past event.
 *
 * The RPC client is the only mock. The workflow, its presets and worktrees come
 * back as real proto messages; what is asserted is the RunNowRequest the dialog
 * built and the PreviewReplay/ListReplayEvents calls it made.
 */

import { beforeEach, describe, expect, it, vi, afterEach } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { queryClient as presetQueryClient } from "@/lib/query-client";
import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { ChatSchema, RunNowResponseSchema } from "@/gen/reliant/v1/chat_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import {
  ListReplayEventsResponseSchema,
  PreviewReplayResponseSchema,
  ReplayCandidateSchema,
  TriggerEventKind,
  TriggerEventOutcome,
  TriggerEventSchema,
  TriggerSenderSchema,
  WorkflowTriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { jsToProtoValue } from "@/api/proto-utils";
import { WORKFLOW_LIST, getWorkflowByName, presetsResponse, worktreesResponse } from "./runFormFixtures";

const listWorkflows = vi.fn();
const getWorkflow = vi.fn();
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const runNow = vi.fn();
const startChat = vi.fn();
const listReplayEvents = vi.fn();
const previewReplay = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();

// PresetService and TriggerService are reached through connectClient (the generated hooks), so
// it is answered at the transport by the same fakes.
beforeEach(() => {
  setTransport(
    mockTransport({
      "ChatService.RunNow": (req: unknown) => runNow(req as never),
      "ChatService.StartChat": (req: unknown) => startChat(req as never),
      // The daemon list is read through connectClient(DaemonRegistryService).
      ...serviceHandlers("DaemonRegistryService", { listDaemons }),
      ...serviceHandlers("TriggerService", { listReplayEvents, previewReplay }),
      ...serviceHandlers("WorkflowService", { listWorkflows, getWorkflow }),
      ...serviceHandlers("PresetService", { listPresetsForWorkflow, getDefaultPresetsBatch }),
      ...serviceHandlers("ProjectService", { listProjectDaemons }),
      // The run form lists workspaces through the generated WorktreeService hooks.
      ...serviceHandlers("WorktreeService", { listWorktrees }),
    }),
  );
});
afterEach(() => {
  setTransport(null);
  // The preset reads are cached in the app's query client; no test may see
  // another's answers.
  presetQueryClient.clear();
});

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ currentProject: { id: "proj-1", name: "Forge" }, projects: [] });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

const loadRunContext = vi.fn(async (_chatId: string) => undefined);
vi.mock("@/components/runs/loadRunContext", () => ({
  loadRunContext: (chatId: string) => loadRunContext(chatId),
}));

import { RunNowDialog } from "../RunNowDialog";

function renderDialog(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow", component: () => <>{ui}</> }),
    createRoute({ getParentRoute: () => rootRoute, path: "/project/$projectId", component: () => null }),
    createRoute({ getParentRoute: () => rootRoute, path: "/settings/$section", component: () => null }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ["/workflow"] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { router };
}

function fill(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

/** The library listing, with `triage` declaring triggers and/or its Chat trigger off. */
function listing(extra: Record<string, unknown> = {}) {
  return {
    ...WORKFLOW_LIST,
    workflows: WORKFLOW_LIST.workflows.map((w) =>
      w.name === "triage"
        ? {
            ...w,
            needsMachine: [] as string[],
            triggers: [create(WorkflowTriggerSchema, { name: "new-issue" }), create(WorkflowTriggerSchema, { name: "nightly" })],
            ...extra,
          }
        : { ...w, needsMachine: ["needs a shell"] },
    ),
  };
}

function protoCandidate(options: { id: string; trigger: string; at: string; number: number; login: string }) {
  return create(ReplayCandidateSchema, {
    triggerName: options.trigger,
    event: create(TriggerEventSchema, {
      id: options.id,
      kind: TriggerEventKind.INTEGRATION,
      occurredAt: options.at,
      outcome: TriggerEventOutcome.SKIPPED,
      payload: { data: { issue: { number: options.number } } },
      sender: create(TriggerSenderSchema, { kind: "github", id: "42", displayName: options.login, verified: true }),
    }),
  });
}

describe("RunNowDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listWorkflows.mockResolvedValue(listing());
    getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
    listPresetsForWorkflow.mockResolvedValue(presetsResponse());
    getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
    listWorktrees.mockImplementation(async (request: { projectId: string }) => worktreesResponse(request.projectId));
    listDaemons.mockResolvedValue({
      daemons: [create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE })],
    });
    listProjectDaemons.mockResolvedValue({
      projectDaemons: [
        create(ProjectDaemonSchema, { projectId: "proj-1", daemonId: "daemon-1", installState: ProjectInstallState.INSTALLED }),
      ],
    });
    runNow.mockResolvedValue(
      create(RunNowResponseSchema, {
        chat: create(ChatSchema, { id: "chat-new", projectId: "proj-1", title: "Triage" }),
        workflowId: "wf-1",
        runId: "run-1",
      }),
    );
    listReplayEvents.mockResolvedValue(create(ListReplayEventsResponseSchema, { events: [] }));
    previewReplay.mockResolvedValue(create(PreviewReplayResponseSchema, { inputs: {}, prompt: "" }));
  });

  it("generates the form from the workflow's inputs, blocks on a required one, and sends RunNow with what it shows", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const { router } = renderDialog(<RunNowDialog open onClose={onClose} projectId="proj-1" workflowRef="triage" />);

    const depth = await screen.findByLabelText("Depth");
    expect(depth).toHaveAttribute("type", "number");
    const label = screen.getByLabelText("Label");
    const strictness = screen.getByLabelText("Strictness");
    expect(strictness.tagName).toBe("SELECT");

    // `label` has no default: Run now is refused with a reason, and nothing is sent.
    await user.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText("Fill in the required input: Label.")).toBeInTheDocument();
    expect(runNow).not.toHaveBeenCalled();

    fill(label, "bug");
    fill(depth, "4");
    await user.selectOptions(strictness, "high");
    const workspace = await screen.findByLabelText("Workspace");
    await screen.findByRole("option", { name: "feature (feat/x)" });
    await user.selectOptions(workspace, "wt-1");
    await user.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
    const request = runNow.mock.calls[0]![0];
    expect(request).toMatchObject({ projectId: "proj-1", workflow: "triage", worktreeId: "wt-1", noMachine: false });
    expect(request.replay).toBeUndefined();
    // Typed inputs, nested by group; the optional message is left blank.
    expect(request.message).toBe("");
    expect(request.inputs.depth.kind).toEqual({ case: "numberValue", value: 4 });
    expect(request.inputs.label.kind).toEqual({ case: "stringValue", value: "bug" });
    expect(request.inputs.review.kind.value.fields.strictness.kind).toEqual({ case: "stringValue", value: "high" });

    // It does not go through StartChat, and the new chat opens.
    expect(startChat).not.toHaveBeenCalled();
    await waitFor(() => expect(loadRunContext).toHaveBeenCalledWith("chat-new"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/project/proj-1"));
    expect(onClose).toHaveBeenCalled();
    // Ten user-event steps: ~0.6s idle, past the 5s default on a saturated merge-gate runner.
  }, 20_000);

  it("runs a workflow whose Chat trigger is off: the message is optional and nothing blocks the button", async () => {
    listWorkflows.mockResolvedValue(listing({ automationOnly: true }));
    const user = userEvent.setup();
    renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    fill(await screen.findByLabelText("Label"), "bug");
    const message = screen.getByLabelText("Message");
    expect(message).toHaveAttribute("placeholder", expect.stringMatching(/Optional/));
    await user.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
    expect(runNow.mock.calls[0]![0].workflow).toBe("triage");
  });

  it("sends the message when one is written", async () => {
    const user = userEvent.setup();
    renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);
    fill(await screen.findByLabelText("Label"), "bug");
    fill(screen.getByLabelText("Message"), "  look at the backlog  ");
    await user.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
    expect(runNow.mock.calls[0]![0].message).toBe("look at the backlog");
  });

  it("offers a machine, No machine, and sends the choice", async () => {
    const user = userEvent.setup();
    renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    const machine = await screen.findByLabelText("Runs on");
    await screen.findByRole("option", { name: /laptop \(online, project installed\)/ });
    expect(machine).toHaveValue("");
    fill(await screen.findByLabelText("Label"), "bug");

    await user.selectOptions(machine, "daemon-1");
    await user.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
    expect(runNow.mock.calls[0]![0]).toMatchObject({ daemonId: "daemon-1", noMachine: false });
  });

  it("No machine sends no_machine and no daemon_id", async () => {
    const user = userEvent.setup();
    renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    const machine = await screen.findByLabelText("Runs on");
    fill(await screen.findByLabelText("Label"), "bug");
    // triage can run without a machine, so the option is live.
    const option = await screen.findByRole("option", { name: "No machine (server tools only)" });
    expect(option).not.toBeDisabled();
    await user.selectOptions(machine, option);
    expect(screen.getByText(/nothing touches files/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
    const request = runNow.mock.calls[0]![0];
    expect(request.noMachine).toBe(true);
    expect(request.daemonId).toBeUndefined();
  });

  it("disables No machine for a workflow that needs one, and says why", async () => {
    renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="sweep" />);
    const option = await screen.findByRole("option", { name: /No machine \(unavailable: this workflow needs a machine\)/ });
    expect(option).toBeDisabled();
  });

  it("shows the server's refusal and stays open", async () => {
    runNow.mockRejectedValue(new ConnectError("input 'label': model gpt-x is not available", Code.InvalidArgument));
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderDialog(<RunNowDialog open onClose={onClose} projectId="proj-1" workflowRef="triage" />);
    fill(await screen.findByLabelText("Label"), "bug");
    await user.click(screen.getByRole("button", { name: "Start run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("input 'label': model gpt-x is not available");
    expect(onClose).not.toHaveBeenCalled();
    expect(loadRunContext).not.toHaveBeenCalled();
  });

  describe("replay", () => {
    it("offers no replay for a workflow that declares no triggers", async () => {
      listWorkflows.mockResolvedValue(listing({ triggers: [] }));
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);
      await screen.findByLabelText("Label");
      expect(screen.queryByRole("radio", { name: "A recent event" })).toBeNull();
      expect(listReplayEvents).not.toHaveBeenCalled();
    });

    it("lists past events, fills the payload from the one chosen, maps it, and sends the replay", async () => {
      listReplayEvents.mockResolvedValue(
        create(ListReplayEventsResponseSchema, {
          events: [
            protoCandidate({ id: "ev-2", trigger: "new-issue", at: "2026-10-20T10:00:00Z", number: 7, login: "octocat" }),
            protoCandidate({ id: "ev-1", trigger: "nightly", at: "2026-10-20T09:00:00Z", number: 1, login: "cron" }),
          ],
        }),
      );
      previewReplay.mockResolvedValue(
        create(PreviewReplayResponseSchema, {
          inputs: { label: jsToProtoValue("bug") },
          prompt: "Triage #7.",
        }),
      );
      const user = userEvent.setup();
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

      await user.click(await screen.findByRole("radio", { name: "A recent event" }));
      // Two triggers: the replay says which one read the event.
      await user.selectOptions(await screen.findByLabelText("Through trigger"), "new-issue");
      const picker = await screen.findByLabelText("Past event");
      await waitFor(() => expect(within(picker).getAllByRole("option").length).toBeGreaterThan(1));
      // Only the chosen trigger's events are offered.
      expect(within(picker).queryByRole("option", { name: /nightly/ })).toBeNull();
      await user.selectOptions(picker, "ev-2");

      // The recorded payload is shown, as editable JSON.
      const payload = screen.getByLabelText("Event payload (JSON)") as HTMLTextAreaElement;
      expect(JSON.parse(payload.value)).toEqual({ data: { issue: { number: 7 } } });

      // The trigger maps it: the prompt fills the message, the input fills the form.
      await waitFor(() => expect(screen.getByLabelText("Message")).toHaveValue("Triage #7."));
      await waitFor(() => expect(screen.getByLabelText("Label")).toHaveValue("bug"));
      expect(previewReplay).toHaveBeenLastCalledWith(
        expect.objectContaining({
          projectId: "proj-1",
          workflow: "triage",
          replay: expect.objectContaining({ triggerName: "new-issue", eventId: "ev-2", kind: "integration" }),
          sender: expect.objectContaining({ displayName: "octocat" }),
        }),
      );

      await user.click(screen.getByRole("button", { name: "Start run" }));
      await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
      const request = runNow.mock.calls[0]![0];
      expect(request.message).toBe("Triage #7.");
      expect(request.replay).toMatchObject({ triggerName: "new-issue", eventId: "ev-2", kind: "integration" });
      expect(request.replay.payload).toEqual({ data: { issue: { number: 7 } } });
      expect(request.inputs.label.kind).toEqual({ case: "stringValue", value: "bug" });
    });

    it("re-maps when the payload is edited, and sends the edited payload", async () => {
      previewReplay.mockImplementation(async (request: { replay: { payload: Record<string, unknown> } }) =>
        create(PreviewReplayResponseSchema, {
          inputs: { label: jsToProtoValue(`n=${JSON.stringify(request.replay.payload.number ? "set" : "unset")}`) },
          prompt: "p",
        }),
      );
      const user = userEvent.setup();
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

      await user.click(await screen.findByRole("radio", { name: "A recent event" }));
      await user.selectOptions(await screen.findByLabelText("Through trigger"), "new-issue");
      await waitFor(() => expect(previewReplay).toHaveBeenCalled());
      const before = previewReplay.mock.calls.length;

      fill(screen.getByLabelText("Event payload (JSON)"), '{"number": 99}');
      await waitFor(() => expect(previewReplay.mock.calls.length).toBeGreaterThan(before));
      await waitFor(() => expect(screen.getByLabelText("Label")).toHaveValue('n="set"'));

      await user.click(screen.getByRole("button", { name: "Start run" }));
      await waitFor(() => expect(runNow).toHaveBeenCalledTimes(1));
      expect(runNow.mock.calls[0]![0].replay.payload).toEqual({ number: 99 });
      // A hand-written event has no recorded id.
      expect(runNow.mock.calls[0]![0].replay.eventId).toBe("");
    });

    it("refuses a payload that is not a JSON object, and does not run", async () => {
      const user = userEvent.setup();
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

      await user.click(await screen.findByRole("radio", { name: "A recent event" }));
      await user.selectOptions(await screen.findByLabelText("Through trigger"), "new-issue");
      fill(screen.getByLabelText("Event payload (JSON)"), "[1, 2");
      expect(await screen.findByText(/Not valid JSON/)).toBeInTheDocument();
      expect(screen.getByLabelText("Event payload (JSON)")).toHaveAttribute("aria-invalid", "true");
      expect(screen.getByRole("button", { name: "Start run" })).toBeDisabled();
      expect(runNow).not.toHaveBeenCalled();

      fill(screen.getByLabelText("Event payload (JSON)"), "[1, 2]");
      expect(await screen.findByText(/must be a JSON object/)).toBeInTheDocument();
    });

    it("shows what the trigger could not map, and does not run", async () => {
      previewReplay.mockRejectedValue(new ConnectError("the trigger \"new-issue\" could not read its inputs from this event", Code.InvalidArgument));
      const user = userEvent.setup();
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

      await user.click(await screen.findByRole("radio", { name: "A recent event" }));
      await user.selectOptions(await screen.findByLabelText("Through trigger"), "new-issue");
      expect(await screen.findByText(/could not read its inputs from this event/)).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Start run" })).toBeDisabled();
    });

    it("says so when the workflow has collected no events yet, and still lets one be written", async () => {
      const user = userEvent.setup();
      renderDialog(<RunNowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);
      await user.click(await screen.findByRole("radio", { name: "A recent event" }));
      expect(await screen.findByText(/No events yet/)).toBeInTheDocument();
      expect(screen.getByLabelText("Event payload (JSON)")).toBeEnabled();
    });
  });
});
