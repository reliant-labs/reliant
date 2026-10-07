// Copyright (c) 2025 Reliant Labs

/**
 * The builder's Test run panel: Run saves the draft first and then starts it
 * by slug with the builder_test flag. Only the RPC client is mocked; what is
 * asserted is the order of the two calls and the StartChatRequest they build.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
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
import { WORKFLOW_LIST, getWorkflowByName, presetsResponse, worktreesResponse } from "./runFormFixtures";

const listWorkflows = vi.fn();
const getWorkflow = vi.fn();
const listPresetsForWorkflow = vi.fn();
const getDefaultPresetsBatch = vi.fn();
const listWorktrees = vi.fn();
const startChat = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();
const getChat = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ listWorkflows, getWorkflow }),
    preset: () => ({ listPresetsForWorkflow, getDefaultPresetsBatch }),
    worktree: () => ({ listWorktrees }),
    chat: () => ({ startChat, getChat }),
    daemonRegistry: () => ({ listDaemons }),
    project: () => ({ listProjectDaemons }),
  },
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ currentProject: { id: "proj-1", name: "Forge" }, projects: [] });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

// The panel reads the test chat's state through useChat; keep that off the wire.
const chatQuery = vi.fn((_id?: string) => ({ data: undefined as undefined | Record<string, unknown> }));
vi.mock("@/hooks/chat-queries", () => ({ useChat: (id?: string) => chatQuery(id) }));

import { BuilderTestRunPanel } from "../BuilderTestRunPanel";

function renderPanel(ui: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow", component: () => <>{ui}</> }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/runs/$runId", component: () => null }),
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

function baseProps(overrides: Partial<Parameters<typeof BuilderTestRunPanel>[0]> = {}) {
  return {
    projectId: "proj-1",
    workflowRef: "triage",
    saveDraft: vi.fn(async () => "triage"),
    testChatId: null,
    onStarted: vi.fn(),
    onClose: vi.fn(),
    ...overrides,
  };
}

async function fillRequiredAndMessage(message = "Try it") {
  fireEvent.change(await screen.findByLabelText("Message"), { target: { value: message } });
  fireEvent.change(await screen.findByLabelText("Label"), { target: { value: "bug" } });
}

describe("BuilderTestRunPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    chatQuery.mockReturnValue({ data: undefined });
    listWorkflows.mockResolvedValue(WORKFLOW_LIST);
    getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
    listPresetsForWorkflow.mockResolvedValue(presetsResponse());
    getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
    listWorktrees.mockImplementation(async (request: { projectId: string }) => worktreesResponse(request.projectId));
    listDaemons.mockResolvedValue({ daemons: [] });
    listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
    startChat.mockResolvedValue(
      create(StartChatResponseSchema, {
        chat: create(ChatSchema, { id: "test-chat-1", projectId: "proj-1", title: "Triage" }),
        workflowId: "test-chat-1",
        runId: "run-1",
      }),
    );
  });

  it("saves the draft first, then starts it by slug with builder_test", async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    const saveDraft = vi.fn(async () => {
      calls.push("save");
      return "triage-saved";
    });
    startChat.mockImplementation(async () => {
      calls.push("start");
      return create(StartChatResponseSchema, {
        chat: create(ChatSchema, { id: "test-chat-1", projectId: "proj-1" }),
      });
    });
    const props = baseProps({ saveDraft });
    renderPanel(<BuilderTestRunPanel {...props} />);

    await fillRequiredAndMessage("Triage the new issues");
    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    expect(calls).toEqual(["save", "start"]);
    const request = startChat.mock.calls[0]![0];
    expect(request).toMatchObject({
      projectId: "proj-1",
      workflow: "triage-saved", // the stored slug, never inline YAML
      builderTest: true,
    });
    expect(request.messages[0].content).toBe("Triage the new issues");
    expect(props.onStarted).toHaveBeenCalledWith(
      "test-chat-1",
      expect.objectContaining({ prompt: "Triage the new issues", value: expect.objectContaining({ params: expect.objectContaining({ label: "bug" }) }) }),
    );
  });

  it("does not start when the save fails", async () => {
    const user = userEvent.setup();
    const saveDraft = vi.fn(async () => null);
    const props = baseProps({ saveDraft });
    renderPanel(<BuilderTestRunPanel {...props} />);

    await fillRequiredAndMessage();
    await user.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(saveDraft).toHaveBeenCalledTimes(1));
    expect(startChat).not.toHaveBeenCalled();
    expect(props.onStarted).not.toHaveBeenCalled();
  });

  it("does not start when the save throws, and does not leak the error as a run", async () => {
    const user = userEvent.setup();
    const saveDraft = vi.fn(async () => {
      throw new Error("workflow was modified since");
    });
    const props = baseProps({ saveDraft });
    renderPanel(<BuilderTestRunPanel {...props} />);

    await fillRequiredAndMessage();
    await user.click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("workflow was modified since");
    expect(startChat).not.toHaveBeenCalled();
  });

  it("shows the server's error and starts nothing more when StartChat rejects the draft", async () => {
    const user = userEvent.setup();
    startChat.mockRejectedValue(new Error("workflow tree validation failed"));
    const props = baseProps();
    renderPanel(<BuilderTestRunPanel {...props} />);

    await fillRequiredAndMessage();
    await user.click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("workflow tree validation failed");
    expect(props.onStarted).not.toHaveBeenCalled();
  });

  it("refuses an empty message before saving anything", async () => {
    const user = userEvent.setup();
    const props = baseProps();
    renderPanel(<BuilderTestRunPanel {...props} />);

    await screen.findByLabelText("Label");
    await user.click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByText("Write the message the run starts from.")).toBeInTheDocument();
    expect(props.saveDraft).not.toHaveBeenCalled();
    expect(startChat).not.toHaveBeenCalled();
  });

  it("links a started run to its full view and offers Run again", async () => {
    chatQuery.mockReturnValue({ data: { id: "test-chat-1" } });
    const { router } = renderPanel(<BuilderTestRunPanel {...baseProps({ testChatId: "test-chat-1" })} />);

    const link = await screen.findByRole("link", { name: "Watch full run" });
    expect(link).toHaveAttribute("href", "/workflows/runs/test-chat-1");
    expect(screen.getByRole("button", { name: "Run again" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/workflow");
  });

  it("reopens with the last run's message and inputs, so Run again repeats that run", async () => {
    const user = userEvent.setup();
    chatQuery.mockReturnValue({ data: { id: "test-chat-1" } });
    const props = baseProps({
      testChatId: "test-chat-1",
      initialRequest: { prompt: "Triage the new issues", value: { presets: {}, params: { label: "bug" } } },
    });
    renderPanel(<BuilderTestRunPanel {...props} />);

    expect(await screen.findByLabelText("Message")).toHaveValue("Triage the new issues");
    expect(await screen.findByLabelText("Label")).toHaveValue("bug");
    await user.click(screen.getByRole("button", { name: "Run again" }));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    const request = startChat.mock.calls[0]![0];
    expect(request.messages[0].content).toBe("Triage the new issues");
    expect(request.workflowParams).toBeDefined();
  });

  it("names the step a finished run failed at and takes the author to it", async () => {
    const user = userEvent.setup();
    chatQuery.mockReturnValue({ data: { id: "test-chat-1" } });
    const onGoToProblem = vi.fn();
    renderPanel(
      <BuilderTestRunPanel
        {...baseProps({
          testChatId: "test-chat-1",
          failure: { label: "Slack · Post message · post", message: "channel_not_found" },
          onGoToProblem,
        })}
      />,
    );

    const failure = await screen.findByTestId("test-run-failure");
    expect(failure).toHaveTextContent("Slack · Post message · post failed");
    expect(failure).toHaveTextContent("channel_not_found");
    await user.click(screen.getByRole("button", { name: "Go to problem" }));
    expect(onGoToProblem).toHaveBeenCalledTimes(1);
  });
});
