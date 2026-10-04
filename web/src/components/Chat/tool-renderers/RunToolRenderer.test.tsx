import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import type { ReactNode } from "react";

import { ToolContentArea } from "./index";
import { RunToolRenderer } from "./RunToolRenderer";
import type { ToolRenderContext } from "./types";

// start_run and get_run name a run the agent started or read. The run id they
// report is the chat id, which is what /runs/$runId takes — so the renderer's
// one job is to make that id a link a person can follow.

function renderWithRouter(ui: ReactNode) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <>{ui}</> }),
    createRoute({ getParentRoute: () => rootRoute, path: "/runs/$runId" }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return render(<RouterProvider router={router} />);
}

function ctx(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: "start_run",
    toolCallId: "tool-1",
    input: { workflow: "builtin://agent", message: "Fix the flaky test", title: "Fix flake" },
    result: undefined,
    chatId: "parent-chat",
    isExpanded: true,
    isCompleted: false,
    isExecuting: true,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

describe("RunToolRenderer", () => {
  it("start_run links the started run from the result metadata", async () => {
    renderWithRouter(
      <RunToolRenderer
        ctx={ctx({
          isCompleted: true,
          isExecuting: false,
          result: {
            name: "start_run",
            content: "Started run chat-77. It is running now",
            metadata: JSON.stringify({ run_id: "chat-77", chat_id: "chat-77" }),
          },
        })}
      />,
    );
    const link = await screen.findByRole("link", { name: /Open run/ });
    expect(link).toHaveAttribute("href", "/runs/chat-77");
    expect(screen.getByText("Fix flake")).toBeInTheDocument();
  });

  it("start_run that attached to an earlier start says so", async () => {
    renderWithRouter(
      <RunToolRenderer
        ctx={ctx({
          isCompleted: true,
          isExecuting: false,
          result: {
            name: "start_run",
            content: "This start was already done",
            metadata: JSON.stringify({ run_id: "chat-77", chat_id: "chat-77", already_started: true }),
          },
        })}
      />,
    );
    expect(await screen.findByText(/already started/i)).toBeInTheDocument();
  });

  it("get_run links the run it read, from its input before the result arrives", async () => {
    renderWithRouter(<RunToolRenderer ctx={ctx({ toolName: "get_run", input: { run_id: "chat-5" } })} />);
    expect(await screen.findByRole("link", { name: /Open run/ })).toHaveAttribute("href", "/runs/chat-5");
  });

  it("get_run shows the run's title and state from the result", async () => {
    renderWithRouter(
      <RunToolRenderer
        ctx={ctx({
          toolName: "get_run",
          input: { run_id: "chat-5" },
          isCompleted: true,
          isExecuting: false,
          result: {
            name: "get_run",
            content: "Run chat-5: Nightly",
            metadata: JSON.stringify({ run_id: "chat-5", title: "Nightly", state: "failed" }),
          },
        })}
      />,
    );
    expect(await screen.findByText("Nightly")).toBeInTheDocument();
    expect(screen.getByText("failed")).toBeInTheDocument();
  });

  it("a failed start shows the error and no link", async () => {
    renderWithRouter(
      <RunToolRenderer
        ctx={ctx({
          isCompleted: true,
          isExecuting: false,
          hasFailed: true,
          result: { name: "start_run", content: "workflow is required", is_error: true },
        })}
      />,
    );
    expect(await screen.findByText("workflow is required")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("ToolContentArea routing for run tools", () => {
  it.each(["start_run", "get_run", "mcp__reliant__start_run"])("routes %s to the run renderer", async (toolName) => {
    renderWithRouter(<ToolContentArea ctx={ctx({ toolName, input: { run_id: "chat-5" } })} />);
    expect(await screen.findByTestId("run-tool-renderer")).toBeInTheDocument();
  });

  it("does not route list_runs or control_run", async () => {
    renderWithRouter(<ToolContentArea ctx={ctx({ toolName: "list_runs", input: { limit: 5 } })} />);
    await screen.findByText(/limit/);
    expect(screen.queryByTestId("run-tool-renderer")).not.toBeInTheDocument();
  });
});
