// Copyright (c) 2025 Reliant Labs

/**
 * ELECTRON-CC (2026-10-10): opening the Inbox froze the tab with React error
 * #185, "Maximum update depth exceeded".
 *
 * The Inbox route is code-split. A tab opened before a deploy still asks for
 * the previous build's chunk, which no longer exists — the static host answers
 * index.html, so the import rejects with "Failed to fetch dynamically imported
 * module". TanStack's lazyRouteComponent recovers from that by calling
 * `window.location.reload()` while RENDERING the route, and the browser runs
 * `beforeunload` listeners synchronously inside reload(). The app's listener
 * saved the viewer layout into the workspace-state store, and that write
 * always published a new state, so every subscriber re-rendered, the route
 * rendered again, reload() fired again, and so on until React gave up.
 *
 * This harness reproduces that sequence: a real router, a real lazy route
 * whose chunk is gone, the real useAutoSaveWorkspaceState listener, a real
 * subscriber of the store, and a location.reload that dispatches
 * `beforeunload` synchronously the way Chrome does.
 */

import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  RouterProvider,
  type RouteComponent,
} from "@tanstack/react-router";

import { RouteErrorFallback } from "../components/ErrorBoundary";
import { useAutoSaveWorkspaceState } from "../hooks/useWorkspaceRestore";
import { lazyRoute } from "../lib/lazyRoute";
import { useCurrentWorktreeState, useWorkspaceStateStore } from "../store/workspaceStateStore";
import { useProjectStore, type Project } from "../store/projectStore";
import { useViewerStore } from "../store/viewerStore";

const STALE_CHUNK_ERROR = `Failed to fetch dynamically imported module: https://app.reliantlabs.io/assets/InboxPage-CKsZPmVl.js`;

// Unfixed, the loop never yields, so the test would hang rather than fail.
// Past this many reloads the stub stops firing beforeunload — the navigation
// "wins" — which ends the loop. Unfixed, the route renders and reloads until
// this bound stops it; in Chrome, React counted those nested updates and threw
// #185 at its limit of 50, which jsdom's scheduling does not reproduce.
const RELOAD_LOOP_BOUND = 100;

let reloadCalls = 0;
let reportedErrors: unknown[] = [];
const realLocation = window.location;

function onWindowError(event: ErrorEvent) {
  reportedErrors.push(event.error ?? event.message);
  event.preventDefault();
}

function updateDepthErrors() {
  return reportedErrors.filter((error) =>
    String(error instanceof Error ? error.message : error).includes("Maximum update depth exceeded"),
  );
}

beforeEach(() => {
  reloadCalls = 0;
  reportedErrors = [];
  sessionStorage.clear();
  useWorkspaceStateStore.getState().reset();
  useViewerStore.setState({ viewers: [], activeViewerId: null });
  useProjectStore.setState({ currentProject: { id: "proj-1", name: "reliant" } as Project });

  // Chrome runs beforeunload listeners synchronously inside location.reload().
  // Nothing here ever navigates away, which is the state the frozen tab was
  // in: every reload() call restarted the navigation.
  vi.stubGlobal("location", {
    ...realLocation,
    href: realLocation.href,
    reload: () => {
      reloadCalls += 1;
      if (reloadCalls > RELOAD_LOOP_BOUND) return;
      window.dispatchEvent(new Event("beforeunload", { cancelable: true }));
    },
  });
  window.addEventListener("error", onWindowError);
});

afterEach(() => {
  window.removeEventListener("error", onWindowError);
  vi.unstubAllGlobals();
  useProjectStore.setState({ currentProject: null });
});

/** ModernApp: saves the viewer layout on beforeunload. */
function WorkspaceAutoSave() {
  useAutoSaveWorkspaceState();
  return null;
}

/** RightSidebar: reads the current worktree's slice of the store. */
function WorktreeStateReader() {
  const state = useCurrentWorktreeState("proj-1", null);
  return <span data-testid="viewer-count">{state.openViewers.length}</span>;
}

type InboxModule = { InboxPage: () => ReactElement };

function renderAppAt(path: string, inboxComponent: RouteComponent) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <WorkspaceAutoSave />
        <WorktreeStateReader />
        <Outlet />
      </>
    ),
  });
  const inboxRoute = createRoute({ getParentRoute: () => rootRoute, path: "/inbox", component: inboxComponent });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <p>home</p> }),
      inboxRoute,
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
    defaultErrorComponent: RouteErrorFallback,
  });
  render(<RouterProvider router={router} />);
  return { router, inboxRoute };
}

function staleChunk(): Promise<InboxModule> {
  return Promise.reject(new TypeError(STALE_CHUNK_ERROR));
}

async function openInbox(router: ReturnType<typeof renderAppAt>["router"]) {
  await screen.findByText("home");
  await act(async () => {
    await router.navigate({ to: "/inbox" });
  });
}

/** Give any loop time to run its course. */
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 50));
  });
}

describe("a stale route chunk while the workspace auto-save is mounted", () => {
  it("does not loop even through TanStack's reload-during-render", async () => {
    // The incident's exact path: lazyRouteComponent reloading while it renders.
    const { router } = renderAppAt("/", lazyRouteComponent(staleChunk, "InboxPage"));
    await openInbox(router);
    await waitFor(() => expect(reloadCalls).toBeGreaterThan(0));
    await settle();

    expect(updateDepthErrors()).toEqual([]);
    // The loop stopped by itself: the save after the first changes nothing,
    // so it publishes nothing and nothing renders the route again.
    expect(reloadCalls).toBeLessThan(RELOAD_LOOP_BOUND);
  });

  it("reloads exactly once, from an effect, through lazyRoute", async () => {
    const renderPhaseUpdates = vi.spyOn(console, "error");
    const { router } = renderAppAt("/", lazyRoute(staleChunk, "InboxPage"));
    await openInbox(router);

    expect(await screen.findByText("Reliant was updated. Reloading…")).toBeInTheDocument();
    await settle();

    expect(reloadCalls).toBe(1);
    expect(updateDepthErrors()).toEqual([]);
    expect(
      renderPhaseUpdates.mock.calls.filter(([message]) => String(message).includes("while rendering a different component")),
    ).toEqual([]);
  });

  it("shows the update screen instead of reloading again when the chunk is still missing", async () => {
    // This session already spent its reload on this chunk.
    sessionStorage.setItem(`reliant:stale-chunk-reload:${STALE_CHUNK_ERROR}`, "1");
    const { router } = renderAppAt("/", lazyRoute(staleChunk, "InboxPage"));
    await openInbox(router);

    expect(await screen.findByRole("heading", { name: "Reliant was updated" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Reload/ })).toBeInTheDocument();
    // The root route — and everything it renders beside the outlet — is still up.
    expect(screen.getByTestId("viewer-count")).toBeInTheDocument();
    expect(reloadCalls).toBe(0);
  });

  it("does not reload for a stale chunk found by a background preload", async () => {
    const { router, inboxRoute } = renderAppAt("/", lazyRoute(staleChunk, "InboxPage"));
    await screen.findByText("home");
    await act(async () => {
      await router.loadRouteChunk(inboxRoute);
    });
    await settle();

    expect(reloadCalls).toBe(0);
    expect(screen.getByText("home")).toBeInTheDocument();
  });

  it("leaves a module that loads untouched", async () => {
    const { router } = renderAppAt(
      "/",
      lazyRoute(() => Promise.resolve<InboxModule>({ InboxPage: () => <p>inbox</p> }), "InboxPage"),
    );
    await openInbox(router);

    expect(await screen.findByText("inbox")).toBeInTheDocument();
    expect(reloadCalls).toBe(0);
  });
});
