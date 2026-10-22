/**
 * The builder page's lifecycle (research/WORKFLOW_EDITOR_UX_REVIEW.md issues
 * 1 and 2), through the real route adapter (WorkflowPage) with the builder
 * itself stubbed and the workflow API mocked:
 *
 *   - "New workflow" creates exactly one draft, on the Create click — never
 *     on mount (React StrictMode mounts twice, which made two drafts).
 *   - Saving a rename moves the URL to the new slug in place and keeps the
 *     canvas, instead of reloading by the old name, 404ing and dumping the
 *     user in the Library.
 */
import { StrictMode, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

const mocks = vi.hoisted(() => ({
  createWorkflowDraft: vi.fn(),
  saveWorkflow: vi.fn(),
  listWorkflowsWithErrors: vi.fn(),
  getWorkflowWithDraftId: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("../../../api/workflow-grpc", () => ({
  workflowGrpc: {
    createWorkflowDraft: mocks.createWorkflowDraft,
    saveWorkflow: mocks.saveWorkflow,
    listWorkflowsWithErrors: mocks.listWorkflowsWithErrors,
  },
  getWorkflowWithDraftId: mocks.getWorkflowWithDraftId,
}));

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: mocks.toastError, success: vi.fn(), info: vi.fn() }),
}));

vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("../../../store/projectStore", () => {
  const state = {
    currentProject: { id: "proj-1", name: "Reliant" },
    projects: [{ id: "proj-1", name: "Reliant" }],
    isLoading: false,
    loadProjects: async () => undefined,
    selectProject: async () => undefined,
    restoreLastProject: async () => true,
  };
  const useProjectStore = Object.assign(
    (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state),
    { getState: () => state },
  );
  return { useProjectStore };
});

vi.mock("../../../store/globalDataStore", () => {
  const state = { presets: [{ name: "general" }], refetchPresets: vi.fn(), refetchWorkflows: vi.fn(async () => undefined) };
  const useGlobalDataStore = Object.assign(
    (selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state),
    { getState: () => state },
  );
  return { useGlobalDataStore, useWorkflows: () => ({ workflows: [] }) };
});

vi.mock("../WorkflowHeader", () => ({ WorkflowHeader: () => null }));

// The canvas is out of scope here; the stub exposes what the page hands it
// and a way to save a renamed workflow.
vi.mock("../WorkflowBuilder", () => ({
  WorkflowBuilder: (props: {
    initialWorkflow?: { name?: string };
    onSave: (workflow: { name: string }) => Promise<unknown>;
  }) => (
    <div data-testid="builder-stub">
      <span data-testid="builder-workflow">{props.initialWorkflow?.name ?? ""}</span>
      <button type="button" onClick={() => void props.onSave({ ...props.initialWorkflow, name: "renamed-flow" })}>
        save renamed
      </button>
    </div>
  ),
}));

import { WorkflowPage } from "../WorkflowPage";

function renderAt(path: string, { strict = false }: { strict?: boolean } = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow/new", component: () => <WorkflowPage isNew /> }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow/$workflowName", component: () => <WorkflowPage /> }),
    createRoute({
      getParentRoute: () => rootRoute,
      path: "/workflows/library",
      component: () => <div data-testid="at-library" />,
    }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const wrap = (node: ReactNode) => (strict ? <StrictMode>{node}</StrictMode> : node);
  render(
    wrap(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    ),
  );
  return router;
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.listWorkflowsWithErrors.mockResolvedValue({ workflows: [], invalidWorkflows: [] });
});

describe("New workflow", () => {
  it("creates nothing on mount, even when React mounts twice", async () => {
    renderAt("/workflow/new", { strict: true });
    expect(await screen.findByRole("dialog", { name: "New workflow" })).toBeInTheDocument();
    // Give any effect every chance to fire.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(mocks.createWorkflowDraft).not.toHaveBeenCalled();
  });

  it("creates exactly one draft per Create, then opens it at its slug", async () => {
    let resolveCreate: (value: unknown) => void = () => {};
    mocks.createWorkflowDraft.mockReturnValue(new Promise((resolve) => (resolveCreate = resolve)));
    mocks.getWorkflowWithDraftId.mockResolvedValue({
      workflow: { name: "triage-issues", title: "Triage issues" },
      draftId: "d1",
      version: 1,
      status: "draft",
      source: "user",
      validationErrors: [],
    });
    const router = renderAt("/workflow/new", { strict: true });

    await userEvent.type(await screen.findByLabelText("Name"), "Triage issues");
    const create = screen.getByRole("button", { name: "Create workflow" });
    await userEvent.click(create);
    await userEvent.click(create);
    await userEvent.keyboard("{Enter}");
    resolveCreate({ draftId: "d1", slug: "triage-issues", name: "triage-issues", title: "Triage issues" });

    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/triage-issues"));
    expect(mocks.createWorkflowDraft).toHaveBeenCalledTimes(1);
    expect(mocks.createWorkflowDraft).toHaveBeenCalledWith("proj-1", { title: "Triage issues", template: undefined });
    expect(await screen.findByTestId("builder-workflow")).toHaveTextContent("triage-issues");
  });

  it("offers the Agent loop as a labelled template, not as a silent default", async () => {
    mocks.createWorkflowDraft.mockResolvedValue({ draftId: "d2", slug: "my-agent", name: "my-agent", title: "My agent" });
    mocks.getWorkflowWithDraftId.mockResolvedValue({ workflow: { name: "my-agent" }, version: 1, status: "draft", source: "user", validationErrors: [] });
    renderAt("/workflow/new");

    await userEvent.type(await screen.findByLabelText("Name"), "My agent");
    await userEvent.click(screen.getByRole("radio", { name: /Agent loop template/ }));
    await userEvent.click(screen.getByRole("button", { name: "Create workflow" }));
    await waitFor(() =>
      expect(mocks.createWorkflowDraft).toHaveBeenCalledWith("proj-1", { title: "My agent", template: "builtin://agent" }),
    );
  });
});

describe("Rename, then save", () => {
  it("moves the URL to the new slug in place and keeps the canvas", async () => {
    let renamed = false;
    mocks.getWorkflowWithDraftId.mockImplementation(async (_projectId: string, name: string) => {
      // After the save the server knows the workflow only by its new slug.
      if (name === "old-flow" && renamed) throw new Error("[not_found] workflow not found: old-flow");
      return {
        workflow: { name },
        draftId: "d1",
        version: 1,
        status: "draft",
        source: "user",
        validationErrors: [],
      };
    });
    mocks.saveWorkflow.mockImplementation(async () => {
      renamed = true;
      return { success: true, message: "", isValid: true, validationErrors: [], id: "d1", slug: "renamed-flow", version: 2, status: "draft" };
    });
    const router = renderAt("/workflow/old-flow");
    expect(await screen.findByTestId("builder-workflow")).toHaveTextContent("old-flow");
    const historyLength = router.history.length;

    await userEvent.click(screen.getByRole("button", { name: "save renamed" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/renamed-flow"));
    // Replaced, not pushed: Back must not lead to a name that no longer exists.
    expect(router.history.length).toBe(historyLength);
    // Still on the canvas, with no "Failed to load" and no bounce to the Library.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByTestId("builder-stub")).toBeInTheDocument();
    expect(screen.queryByTestId("at-library")).toBeNull();
    expect(mocks.toastError).not.toHaveBeenCalled();
    // The canvas already holds the saved workflow; the route change reloads nothing.
    expect(mocks.getWorkflowWithDraftId).toHaveBeenCalledTimes(1);
    expect(mocks.saveWorkflow).toHaveBeenCalledWith("proj-1", expect.objectContaining({ name: "renamed-flow" }), 1, undefined, "d1", undefined);
  });
});
