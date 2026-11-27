// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav body (DemoNav): the app's nav column with its chats
 * (Layout/ChatShellNav.tsx) and the demo's config. The app's header (the
 * sample org Acme on Starter, from the demo twins of hooks/useOrgs and
 * Orgs/orgPlan; Find; the project switcher), the app's Inbox and Agents with
 * its chat list, always the sample shop's sections for one project, none of
 * the account's rows, the drill-in's own anchor on an Observability route,
 * and on a phone the demo's actions in the drawer.
 */

import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useChatListPreferencesStore } from "@/store/chatListPreferencesStore";

const narrow = vi.hoisted(() => ({ value: false }));
vi.mock("@/hooks/useNarrowViewport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useNarrowViewport")>()),
  useNarrowViewport: () => narrow.value,
}));
// The scope as an all-projects pick that names no app: DemoNav still draws shop's sections, one project.
vi.mock("@/components/Layout/sidebar/shellProject", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/Layout/sidebar/shellProject")>()),
  useShellProject: () => ({ mode: "all", ids: ["p1", "p2"], key: "p1", name: "shop", projectId: "p1", app: null, deployed: false, row: undefined, catalog: undefined }),
}));
vi.mock("@/components/Forge/Console/useConsoleNavModel", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/Forge/Console/useConsoleNavModel")>()),
  useConsoleNavModel: (project: string) => ({ project, page: { kind: "overview" }, env: "prod", envs: [], alertsFiring: 1, starred: [], customDashboards: [], navCounts: { tests: 1 } }),
}));
vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => false }));
// The demo bundle's twins, as the demo build substitutes them (vite-plugin-demo-bundle.ts).
vi.mock("@/hooks/useOrgs", () => import("@/hooks/useOrgs.demo"));
vi.mock("@/components/Orgs/orgPlan", () => import("@/components/Orgs/orgPlan.demo"));
vi.mock("@/hooks/sidebar-project-queries", () => import("@/hooks/sidebar-project-queries.demo"));
// What the shop app layer answers over the demo's transport, at the hooks.
const shopChat = vi.hoisted(() => ({
  id: "c1",
  title: "Rate-limit checkout",
  projectId: "p1",
  createdAt: "2026-01-01T00:00:00.000Z",
  updatedAt: "2026-01-01T00:00:00.000Z",
  lastMessageAt: "2026-01-01T00:00:00.000Z",
  unread: false,
  state: 1,
}));
const NO_MAIN_WORKTREES: never[] = [];
vi.mock("@/hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({ data: { chats: [shopChat], lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES }, isPending: false }),
  useArchivedChats: () => ({ data: [], isFetched: true, hasNextPage: false, isFetchingNextPage: false, fetchNextPage: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));
const shopProjects = vi.hoisted(() => [{ id: "p1", name: "shop", path: "/home/alex/code/shop", last_active: "2026-01-01T00:00:00.000Z", is_git_repo: true, is_forge: true }]);
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: shopProjects, isLoading: false }),
  useProjectDaemonInstalls: () => ({ data: undefined }),
}));
vi.mock("@/hooks/project-catalog-queries", () => ({ useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: null, refetch: () => {} }) }));
vi.mock("@/hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useOnboardingQueries")>()),
  useDaemonList: () => ({ data: [] }),
}));
vi.mock("@/hooks/inbox-queries", () => ({
  useInboxCounts: () => ({ data: { blockingCount: 2, hasInformational: false }, isError: false }),
  useInboxProjectId: () => undefined,
}));
vi.mock("@/hooks/run-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/run-queries")>()),
  useLiveRuns: () => ({ data: undefined }),
}));
vi.mock("@/hooks/message-queries", () => ({ useMarkUnread: () => ({ mutate: vi.fn(), mutateAsync: vi.fn() }) }));
// Other members' shared chats: their own test (Sidebar.sharedWithOrg).
vi.mock("@/components/Sharing/SharedWithOrgSection", () => ({ SharedWithOrgSection: () => null }));
vi.mock("@/components/forge-ui/tooltip", () => ({
  default: ({ children, triggerClassName }: { children: ReactNode; triggerClassName?: string }) =>
    triggerClassName ? <span className={triggerClassName}>{children}</span> : <>{children}</>,
}));

import { DemoNav } from "../DemoNav";

function renderAt(href: string) {
  const [pathname, query = ""] = href.split("?");
  const search = Object.fromEntries(new URLSearchParams(query));
  const rootRoute = createRootRoute({ component: () => <DemoNav pathname={pathname} search={search} onNavigate={vi.fn()} /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: [href] }) });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  narrow.value = false;
  useChatListPreferencesStore.setState({ sortOrder: "recent_activity", viewMode: "grouped" });
});

describe("the demo's nav", () => {
  it("draws the app's nav on the demo's own page: Home lit, Inbox and Agents, shop's sections, none of the account's rows", async () => {
    renderAt("/forge/demo?section=home");
    const top = await screen.findByTestId("nav-top");
    expect(within(top).getByTestId("nav-demo-home")).toHaveAttribute("aria-current", "page");
    expect(within(top).getByTestId("nav-demo-home")).toHaveAttribute("href", "/forge/demo?section=home");
    // The app's Inbox, with its count, and Agents, whose first page is the project's chats.
    expect(within(top).getByTestId("sidebar-inbox-button")).toHaveAttribute("href", "/inbox");
    expect(within(top).getByTestId("inbox-badge-count")).toHaveTextContent("2");
    expect(within(top).getByTestId("nav-agents")).toHaveAttribute("href", "/project/p1");
    // One project, shop: its console rows, not the org-wide pages an all-projects pick would open.
    expect(within(top).getByTestId("sidebar-home-button")).toHaveAttribute("href", "/forge/p/shop");
    expect(within(top).getByTestId("nav-observability")).toHaveAttribute("href", "/forge/p/shop/observability?env=prod");
    expect(within(top).getByTestId("nav-data")).toHaveAttribute("href", "/forge/p/shop/data?env=prod");
    // The account's rows are the app's alone.
    for (const id of ["nav-usage-billing", "nav-help", "sidebar-settings-button"]) expect(screen.queryByTestId(id)).toBeNull();
    // The footer: How it was built, a page of the demo's.
    expect(screen.getByTestId("nav-demo-built")).toHaveAttribute("href", "/forge/demo?section=built");
    // The phone's actions are the drawer's only.
    expect(screen.queryByTestId("demo-drawer-actions")).toBeNull();
  });

  it("heads the column with the app's header: the sample org on its plan, Find, the project switcher", async () => {
    renderAt("/forge/demo?section=home");
    const org = await screen.findByTestId("org-switcher");
    expect(within(org).getByTestId("org-switcher-name")).toHaveTextContent("Acme");
    expect(within(org).getByTestId("org-plan-badge")).toHaveTextContent("Starter");
    expect(screen.getByTestId("nav-find")).toBeInTheDocument();
    expect(screen.getByTestId("shell-project-name")).toHaveTextContent("shop");
    expect(screen.getByTestId("shell-project-switcher")).toHaveAttribute("data-mode", "one");
  });

  it("drills into Agents on a project's chats: New chat, the Inbox, Workflows, the chat list", async () => {
    renderAt("/project/p1");
    const drill = await screen.findByTestId("nav-drill-in");
    expect(drill).toHaveAttribute("data-area", "agents");
    expect(within(drill).getByTestId("create-chat-button")).toBeInTheDocument();
    expect(within(drill).getByTestId("sidebar-inbox-button")).toBeInTheDocument();
    expect(within(drill).getByTestId("sidebar-chats")).toHaveTextContent("Rate-limit checkout");
  });

  it("drills into Observability on its routes, under a header the tour can ring", async () => {
    renderAt("/forge/p/shop/observability/traces?env=prod");
    const drill = await screen.findByTestId("nav-drill-in");
    expect(drill).toHaveAttribute("data-area", "observability");
    expect(within(drill).getByTestId("nav-back")).toHaveAttribute("data-tour", "nav-drill-in");
    expect(within(drill).getByTestId("nav-back")).toHaveAttribute("href", "/forge/p/shop");
    expect(screen.queryByTestId("nav-top")).toBeNull();
    expect(document.querySelector("[data-tour='forge-console-nav']")).toBeNull();
    expect(within(drill).getByTestId("nav-obs-traces")).toHaveAttribute("aria-current", "page");
    expect(within(drill).getByTestId("nav-site-insights")).toBeInTheDocument();
  });

  it("puts the tour, Install Forge and the telemetry notice in a phone's drawer, every row drawn flat", async () => {
    narrow.value = true;
    renderAt("/forge/p/shop?env=prod");
    const actions = await screen.findByTestId("demo-drawer-actions");
    expect(within(actions).getByTestId("demo-start-tour")).toBeInTheDocument();
    expect(within(actions).getByRole("button", { name: "Install Forge" })).toBeInTheDocument();
    // Above the footer's own row, as the drawer had it.
    expect(actions.compareDocumentPosition(screen.getByTestId("nav-demo-built")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
