// Copyright (c) 2025 Reliant Labs

/**
 * Run… — the form renders a workflow's typed inputs, blocks a run with a
 * required input unset, and sends the values it shows.
 *
 * The RPC client is the only mock: the definition, presets and worktrees come
 * back as real proto messages, and what is asserted is the StartChatRequest
 * the dialog built. The project store says the CURRENT project is proj-2
 * while the form is asked about proj-1 — every read must follow the prop.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
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

import { ChatSchema, StartChatResponseSchema } from "@/gen/reliant/v1/chat_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import {
  WORKFLOW_LIST,
  getWorkflowByName,
  presetsResponse,
  worktreesResponse,
} from "./runFormFixtures";

const listWorkflows = vi.fn();
const getWorkflow = vi.fn();
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const startChat = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ listWorkflows, getWorkflow }),
    preset: () => ({ listPresetsForWorkflow, getDefaultPresetsBatch }),
    worktree: () => ({ listWorktrees }),
    chat: () => ({ startChat }),
    daemonRegistry: () => ({ listDaemons }),
    project: () => ({ listProjectDaemons }),
  },
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ currentProject: { id: "proj-2", name: "Forge" }, projects: [] });
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

import { RunWorkflowDialog } from "../RunWorkflowDialog";

function renderDialog(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
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

/** One change event, as a paste would. */
function fill(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

describe("RunWorkflowDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listWorkflows.mockResolvedValue(WORKFLOW_LIST);
    getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
    listPresetsForWorkflow.mockResolvedValue(presetsResponse());
    getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
    listWorktrees.mockImplementation(async (request: { projectId: string }) =>
      worktreesResponse(request.projectId),
    );
    listDaemons.mockResolvedValue({
      daemons: [
        create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE }),
        create(DaemonInfoSchema, { daemonId: "daemon-2", hostname: "cloud-box", status: DaemonStatus.ACTIVE }),
      ],
    });
    // proj-1 is installed on daemon-1 only, so daemon-2 must not be offered.
    listProjectDaemons.mockResolvedValue({
      projectDaemons: [
        create(ProjectDaemonSchema, {
          projectId: "proj-1",
          daemonId: "daemon-1",
          installState: ProjectInstallState.INSTALLED,
        }),
      ],
    });
    startChat.mockResolvedValue(
      create(StartChatResponseSchema, {
        chat: create(ChatSchema, { id: "chat-new", projectId: "proj-1", title: "Triage" }),
        workflowId: "wf-1",
        runId: "run-1",
      }),
    );
  });

  it("renders typed inputs, blocks Run while a required one is unset, then sends what it shows", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const { router } = renderDialog(
      <RunWorkflowDialog open onClose={onClose} projectId="proj-1" workflowRef="triage" />,
    );

    // Each input renders with its type: a number, a text field, an enum select.
    const depth = await screen.findByLabelText("Depth");
    expect(depth).toHaveAttribute("type", "number");
    expect(depth).toHaveValue(2); // the declared default
    const label = screen.getByLabelText("Label");
    const strictness = screen.getByLabelText("Strictness");
    expect(strictness.tagName).toBe("SELECT");
    expect(strictness).toHaveValue("low");

    // Every read is in the project the dialog was given, never the current one.
    expect(getWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", name: "triage" }));
    await waitFor(() =>
      expect(listPresetsForWorkflow).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1" })),
    );
    expect(listPresetsForWorkflow).not.toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-2" }));
    expect(listWorktrees).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1" }));

    // `label` has no default, so it is required: Run is refused with a reason.
    fill(screen.getByLabelText("Message"), "Triage the new issues");
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText("Fill in the required input: Label.")).toBeInTheDocument();
    expect(startChat).not.toHaveBeenCalled();

    fill(label, "bug");
    fill(depth, "4");
    await user.selectOptions(strictness, "high");
    // The main checkout is the default; a project worktree can be chosen.
    const workspace = await screen.findByLabelText("Workspace");
    await screen.findByRole("option", { name: "feature (feat/x)" });
    expect(screen.queryByRole("option", { name: /^main \(main\)/ })).not.toBeInTheDocument();
    await user.selectOptions(workspace, "wt-1");
    await waitFor(() => expect(screen.queryByText(/Fill in the required/)).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    const request = startChat.mock.calls[0]![0];
    expect(request).toMatchObject({
      projectId: "proj-1",
      worktreeId: "wt-1",
      workflow: "triage",
      selectedPresets: {},
    });
    expect(request.messages[0].content).toBe("Triage the new issues");
    // Nested by group, as the server validates them; integers stay numbers.
    expect(request.workflowParams.depth.kind).toEqual({ case: "numberValue", value: 4 });
    expect(request.workflowParams.label.kind).toEqual({ case: "stringValue", value: "bug" });
    const review = request.workflowParams.review.kind;
    expect(review.case).toBe("structValue");
    expect(review.value.fields.strictness.kind).toEqual({ case: "stringValue", value: "high" });

    // Attended: the new chat opens in its project.
    await waitFor(() => expect(loadRunContext).toHaveBeenCalledWith("chat-new"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/project/proj-1"));
    expect(onClose).toHaveBeenCalled();
  });

  it("starts from the workflow's default preset, shows its values, and sends the preset by reference", async () => {
    getDefaultPresetsBatch.mockResolvedValue({
      presetsByWorkflow: { triage: { presets: { "": "careful" } } },
    });
    const user = userEvent.setup();
    renderDialog(<RunWorkflowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    // The preset supplies label, so the required input is satisfied.
    await waitFor(() => expect(screen.getByLabelText("Label")).toHaveValue("bug"));
    expect(screen.getByLabelText("Depth")).toHaveValue(5);

    // Choosing the group preset for `review` applies its value.
    const reviewGroup = screen
      .getAllByText("review")
      .map((node) => node.closest(".cpv2-param-group"))
      .find(Boolean) as HTMLElement;
    // The group's preset picker reads "review" until a preset is chosen.
    const picker = within(reviewGroup)
      .getAllByRole("button")
      .find((button) => !button.classList.contains("cpv2-param-group-header"))!;
    await user.click(picker);
    await user.click(await screen.findByRole("button", { name: /strict/ }));
    await waitFor(() => expect(screen.getByLabelText("Strictness")).toHaveValue("high"));

    fill(screen.getByLabelText("Message"), "Go");
    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    const request = startChat.mock.calls[0]![0];
    expect(request.selectedPresets).toEqual({ "": "careful", review: "strict" });
    // Presets travel as names; their values are not copied into params.
    expect(request.workflowParams).toEqual({});
    expect(request.worktreeId).toBeUndefined();
  });

  it("shows the server's validation error and stays open", async () => {
    startChat.mockRejectedValue(
      new ConnectError("input 'label': model gpt-x is not available", Code.InvalidArgument),
    );
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderDialog(<RunWorkflowDialog open onClose={onClose} projectId="proj-1" workflowRef="triage" />);

    fill(await screen.findByLabelText("Label"), "bug");
    fill(screen.getByLabelText("Message"), "Go");
    await user.click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("input 'label': model gpt-x is not available");
    // Only a missing provider key points at Settings.
    expect(screen.queryByRole("link", { name: /Your providers/ })).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(loadRunContext).not.toHaveBeenCalled();
  });

  it("a run with no model provider links to Settings → AI → Your providers", async () => {
    startChat.mockRejectedValue(
      new ConnectError("no API keys configured: please add an API key in Settings > API Keys", Code.FailedPrecondition),
    );
    const user = userEvent.setup();
    const onClose = vi.fn();
    const { router } = renderDialog(<RunWorkflowDialog open onClose={onClose} projectId="proj-1" workflowRef="triage" />);

    fill(await screen.findByLabelText("Label"), "bug");
    fill(screen.getByLabelText("Message"), "Go");
    await user.click(screen.getByRole("button", { name: "Run" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("no API keys configured");
    const link = within(alert).getByRole("link", { name: "Add a provider in Settings → AI → Your providers" });
    // The AI section opens on its "Your providers" tab.
    expect(link).toHaveAttribute("href", "/settings/general");
    await user.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/general"));
    expect(onClose).toHaveBeenCalled();
  });

  it("requires a message", async () => {
    const user = userEvent.setup();
    renderDialog(<RunWorkflowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);
    fill(await screen.findByLabelText("Label"), "bug");
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText("Write the message the run starts from.")).toBeInTheDocument();
    expect(screen.getByLabelText("Message")).toHaveAttribute("aria-invalid", "true");
    expect(startChat).not.toHaveBeenCalled();
  });
  it("offers only machines the project is installed on, and sends the chosen one", async () => {
    const user = userEvent.setup();
    renderDialog(<RunWorkflowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    const machine = await screen.findByLabelText("Runs on");
    await screen.findByRole("option", { name: /laptop \(online, project installed\)/ });
    expect(screen.queryByRole("option", { name: /cloud-box/ })).not.toBeInTheDocument();
    // Optional, and nothing is forced even though only one machine qualifies.
    expect(machine).toHaveValue("");

    fill(screen.getByLabelText("Message"), "Triage the new issues");
    fill(await screen.findByLabelText("Label"), "bug");
    await user.selectOptions(machine, "daemon-1");
    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    expect(startChat.mock.calls[0]![0].daemonId).toBe("daemon-1");
  });

  it("sends no daemon_id when no machine is chosen", async () => {
    const user = userEvent.setup();
    renderDialog(<RunWorkflowDialog open onClose={vi.fn()} projectId="proj-1" workflowRef="triage" />);

    await screen.findByLabelText("Runs on");
    fill(screen.getByLabelText("Message"), "Triage the new issues");
    fill(await screen.findByLabelText("Label"), "bug");
    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    expect(startChat.mock.calls[0]![0].daemonId).toBeUndefined();
  });
});
