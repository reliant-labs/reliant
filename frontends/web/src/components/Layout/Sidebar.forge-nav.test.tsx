/**
 * The Forge sidebar entry is gated on the EXPERIMENTAL FLAG ONLY — never on
 * whether the current project is a forge project.
 *
 * WHY IT IS NOT ALSO GATED ON THE PROJECT. Forge is a top-level feature, so
 * the entry sits beside New chat / Workflows and stays PUT. A nav
 * item that appears and disappears as you switch projects is harder to learn
 * than one that is always there, and "is this a forge project?" is a question
 * the destination answers better than the nav can: clicking it in an ordinary
 * project lands on the NotForgeProject screen, which says so in words.
 *
 * It also removes a failure mode that actually shipped. The old gate required
 * a LIVE GetTopology call to succeed, so when that RPC broke on a report-type
 * mismatch the entire feature silently vanished — a missing nav entry is
 * indistinguishable from a feature nobody built, and nothing in the UI said
 * otherwise. A nav entry whose visibility depends on an RPC is a nav entry
 * that a bug can delete.
 *
 * `Project.is_forge` is irrelevant here now, and these cases pin that: it is
 * populated at create time and never recomputed (control-plane's row carries
 * is_forge=false while its forge.yaml exists), so neither value may affect
 * the entry either way.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatActivity } from "../../gen/reliant/v1/chat_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";

// The machine filter's daemon list: no machines, so the filter is hidden.
vi.mock("../../hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/useOnboardingQueries")>()),
  useDaemonList: () => NO_MACHINES,
}));
const NO_MACHINES = { data: [] };
vi.mock("../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../forge-ui/context_menu", () => ({ default: () => null }));
vi.mock("../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

vi.mock("../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: [], lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));

// The Inbox nav item reads its own counts; nothing is waiting here.
vi.mock("../../hooks/inbox-queries", () => ({
  useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
  useInboxProjectId: () => undefined,
}));
// The automation pill polls live runs; there are none here.
vi.mock("../../hooks/run-queries", async () => {
  const actual = await vi.importActual<typeof import("../../hooks/run-queries")>(
    "../../hooks/run-queries",
  );
  return { ...actual, useLiveRuns: () => ({ data: undefined }) };
});

vi.mock("../../hooks/message-queries", () => ({
  useMarkUnread: () => ({ mutateAsync: vi.fn() }),
}));

// Pin the build type to a PACKAGED PRODUCTION build: the Apps section must not
// depend on it (it once did, behind an experimental gate that is gone).
vi.mock("../../lib/constants", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/constants")>();
  return { ...actual, getIsDev: () => false, isDev: false };
});

// The org's apps (hooks/apps-queries.ts): two, one with a live site.
const appsState = vi.hoisted(() => ({ apps: [] as unknown[] }));
vi.mock("../../hooks/apps-queries", () => ({
  useApps: () => ({ apps: appsState.apps, loading: false, availability: "available", detail: "" }),
}));

// The open app's console model (Forge/Console/useConsoleNavModel.ts), read
// from the URL: the page the test names, no network.
vi.mock("../Forge/Console/useConsoleNavModel", async () => {
  const { consolePageOf } = await vi.importActual<typeof import("../Forge/Console/paths")>("../Forge/Console/paths");
  return {
    useConsoleNavModel: (project: string, pathname: string) => ({
      project,
      page: consolePageOf(pathname)?.page ?? { kind: "overview" },
      env: "prod",
      envs: [
        { name: "prod", preview: false },
        { name: "staging", preview: false },
      ],
      alertsFiring: 2,
      starred: [],
    }),
  };
});

const SHOP = {
  name: "shop",
  envs: [{ id: "e1", name: "prod", preview: false, verdict: "converged", release: "v1", links: [{ workload: "web", url: "https://shop.example", host: "shop.example", custom: true }], loading: false }],
  previews: 0,
};
const BLOG = { name: "blog", envs: [], previews: 0 };

function renderSidebar(props: Partial<Parameters<typeof Sidebar>[0]> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <Sidebar pathname="/" onNavigate={vi.fn()} {...props} />
    </QueryClientProvider>
  );
}

/** A nav entry's name: its label, without the key cap New chat shows. */
const navLabel = (b: Element) => b.getAttribute("aria-label") ?? b.textContent?.trim();

/** The places, in order: every link and button of the top nav. */
function navEntries() {
  return Array.from(
    screen.getByRole("navigation", { name: /chat sidebar navigation/i }).querySelectorAll("a, button")
  ).map(navLabel);
}

describe("Sidebar forge nav entry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    appsState.apps = [];

    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });

    useChatStore.setState({
      chats: new Map(),
      activeChatId: null,
      selectChat: vi.fn(),
    } as Partial<ReturnType<typeof useChatStore.getState>>);

    useActivityStore.setState({
      activities: new Map<string, ChatActivity>(),
    } as Partial<ReturnType<typeof useActivityStore.getState>>);

    useWorktreeStore.setState({
      currentWorktreeId: null,
      currentWorktreeProjectId: null,
      switchWorktreeContext: vi.fn(async () => undefined),
    } as Partial<ReturnType<typeof useWorktreeStore.getState>>);

    useChatListPreferencesStore.setState({
      sortOrder: "recent_activity",
      viewMode: "grouped",
      filters: {},
      setSortOrder: vi.fn(),
      setViewMode: vi.fn(),
      setFilters: vi.fn(),
      resetFilters: vi.fn(),
      resetAll: vi.fn(),
    });
  });

  /**
   * setProject writes a project row carrying an explicit `is_forge`. The value is
   * deliberately independent of the live gate, so a test can put the two in
   * conflict — which is the real-world state for control-plane.
   */
  function setProject(isForge: boolean) {
    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "control-plane",
        path: "/tmp/project",
        is_git_repo: true,
        is_forge: isForge,
        worktree_count: 1,
        created_at: "2024-01-01T00:00:00.000Z",
        updated_at: "2024-01-01T00:00:00.000Z",
        last_active: "2024-01-01T00:00:00.000Z",
      },
    } as unknown as Partial<ReturnType<typeof useProjectStore.getState>>);
  }

  it("shows the entry, always: nothing gates the Apps section", () => {
    setProject(true);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // THE REVERSAL. An ordinary project still gets the entry; the destination
  // explains itself rather than the nav hiding the feature.
  it("shows the entry for a project that is NOT a forge project", () => {
    setProject(false);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // is_forge is stale by construction — populated at create time, never
  // recomputed — so neither value may move the entry.
  it.each([true, false])("ignores Project.is_forge = %s", (isForge) => {
    setProject(isForge);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // The retired Developer toggle's stored opt-out (`reliant.experimental.forgeUI`)
  // is ignored: nothing reads it any more.
  it("ignores the retired forge UI opt-out still in a browser's storage", () => {
    window.localStorage.setItem("reliant.experimental.forgeUI", "false");
    setProject(true);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
    window.localStorage.removeItem("reliant.experimental.forgeUI");
  });

  // The places, in order (U6, design §4.1): New chat, then what needs you,
  // what you work on, what runs by itself, what it costs, and Search. Apps is
  // no longer a place among them: it is the section below (every app, the
  // open one's console inline).
  it("orders the nav New chat, Inbox, Projects, Workflows, Usage, Search", () => {
    setProject(true);
    renderSidebar();
    expect(navEntries()).toEqual(["New chat", "Inbox", "Projects", "Workflows", "Usage", "Search"]);
  });

  // The section is LABELLED Apps but is still the forge entry underneath: the
  // test id keeps the forge name. It opens Projects, narrowed to the apps (the
  // retired /apps redirects there too).
  it("heads the Apps section with the forge test id, a link to every app", () => {
    setProject(true);
    const onNavigate = vi.fn();
    renderSidebar({ onNavigate });
    const heading = screen.getByTestId("sidebar-forge-button");
    expect(heading).toHaveTextContent("Apps");
    expect(heading).toHaveAttribute("href", "/projects?show=apps");
    fireEvent.click(heading);
    expect(onNavigate).toHaveBeenCalledWith("/projects?show=apps");
  });

  it("has no separate Observability entry", () => {
    setProject(true);
    renderSidebar();
    expect(navEntries()).not.toContain("Observability");
    expect(screen.queryByTestId("sidebar-observability-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("sidebar-org-observability")).not.toBeInTheDocument();
  });

  it("lists every app, each one click from its console, with no sections while none is open", () => {
    appsState.apps = [SHOP, BLOG];
    const onNavigate = vi.fn();
    renderSidebar({ onNavigate });
    const apps = within(screen.getByTestId("sidebar-apps"));
    expect(apps.getByTestId("sidebar-app-shop")).toHaveAttribute("href", "/forge/p/shop");
    expect(apps.getByTestId("sidebar-app-blog")).toHaveAttribute("href", "/forge/p/blog");
    expect(apps.getByTestId("sidebar-app-link-shop")).toHaveAttribute("href", "https://shop.example");
    expect(screen.queryByTestId("sidebar-app-sections")).not.toBeInTheDocument();
    fireEvent.click(apps.getByTestId("sidebar-app-blog"));
    expect(onNavigate).toHaveBeenCalledWith("/forge/p/blog");
  });

  // The owner's amendment (2026-11-19): an app's console sections are rows of
  // the one nav, nested under the open app — never a second column.
  it("expands the open app into its console sections, the page on screen marked", () => {
    appsState.apps = [SHOP, BLOG];
    const onNavigate = vi.fn();
    renderSidebar({ pathname: "/forge/p/shop/observability/errors", onNavigate });
    expect(screen.getByTestId("sidebar-app-shop")).toHaveAttribute("aria-expanded", "true");
    const sections = screen.getByTestId("sidebar-app-sections");
    expect(sections).toHaveAttribute("data-tour", "forge-console-nav");
    const labels = Array.from(sections.querySelectorAll("a, button")).map((row) => row.textContent?.trim());
    // Only Issues is open: its Errors is the page on screen.
    expect(labels).toEqual([
      "Overview",
      "Issues",
      "Errors",
      "Alerts2 firing",
      "Performance",
      "Sessions",
      "Releases",
      "Data",
      "Users",
      "More tools",
      "Environments",
      "Secrets",
      "Domains",
      "Activity",
    ]);
    expect(within(sections).getByText("Configure")).toBeInTheDocument();
    const errors = within(sections).getByTestId("sidebar-app-section-errors");
    expect(errors).toHaveAttribute("aria-current", "page");
    expect(errors).toHaveAttribute("href", "/forge/p/shop/observability/errors?env=prod");
    expect(within(sections).getByTestId("sidebar-app-section-overview")).not.toHaveAttribute("aria-current");
    // Only the open app expands.
    expect(screen.getByTestId("sidebar-app-blog")).not.toHaveAttribute("aria-expanded", "true");
    fireEvent.click(within(sections).getByTestId("sidebar-app-section-releases"));
    expect(onNavigate).toHaveBeenCalledWith("/forge/p/shop/releases?env=prod");
  });

  // An open app's sections stay short (owner preview 2026-11-19: on a 900px
  // window an expanded app pushed Chats below the fold): a group's rows show
  // only while one of them is the page, and the group's row links to its
  // first row, a real URL.
  it("keeps every group closed until one of its rows is the page, each group a link to its first row", () => {
    appsState.apps = [SHOP];
    const onNavigate = vi.fn();
    renderSidebar({ pathname: "/forge/p/shop", onNavigate });
    const sections = screen.getByTestId("sidebar-app-sections");
    const labels = Array.from(sections.querySelectorAll("a, button")).map((row) => row.textContent?.trim());
    expect(labels).toEqual([
      "Overview",
      // Closed, it carries its rows' alerts firing.
      "Issues2 firing",
      "Performance",
      "Sessions",
      "Releases",
      "Data",
      "Users",
      "More tools",
      "Environments",
      "Secrets",
      "Domains",
      "Activity",
    ]);
    for (const [group, href] of [
      ["issues", "/forge/p/shop/observability/errors?env=prod"],
      ["performance", "/forge/p/shop/observability/traces?env=prod"],
      ["more-tools", "/forge/p/shop/observability/dashboards?env=prod"],
      ["environments", "/forge/p/shop/environments?env=prod"],
    ] as const) {
      const row = within(sections).getByTestId(`sidebar-app-section-${group}`);
      expect(row.tagName).toBe("A");
      expect(row).toHaveAttribute("href", href);
      expect(row).toHaveAttribute("aria-expanded", "false");
      expect(row).not.toHaveAttribute("aria-current");
    }
    expect(within(sections).queryByText("Errors")).toBeNull();
    expect(within(sections).queryByText("Logs")).toBeNull();
    expect(within(sections).queryByTestId("sidebar-app-section-staging")).toBeNull();
    fireEvent.click(within(sections).getByTestId("sidebar-app-section-performance"));
    expect(onNavigate).toHaveBeenCalledWith("/forge/p/shop/observability/traces?env=prod");
  });

  it.each([
    ["/forge/p/shop/observability/logs", "more-tools", "Logs"],
    ["/forge/p/shop/observability/rpc", "performance", "RPC explorer"],
  ])("opens the group whose row is the page on %s", (pathname, group, row) => {
    appsState.apps = [SHOP];
    renderSidebar({ pathname });
    const sections = screen.getByTestId("sidebar-app-sections");
    expect(within(sections).getByTestId(`sidebar-app-section-${group}`)).toHaveAttribute("aria-expanded", "true");
    expect(within(sections).getByText(row).closest("a")).toHaveAttribute("aria-current", "page");
    // Every other group stays closed.
    expect(within(sections).getByTestId("sidebar-app-section-issues")).toHaveAttribute("aria-expanded", "false");
  });

  it("shows an environment's page under Environments, and keeps the env rows closed on the environments list", () => {
    appsState.apps = [SHOP];
    const view = renderSidebar({ pathname: "/forge/p/shop/environments/staging" });
    let sections = screen.getByTestId("sidebar-app-sections");
    expect(within(sections).getByTestId("sidebar-app-section-environments")).toHaveAttribute("aria-expanded", "true");
    expect(within(sections).getByTestId("sidebar-app-section-staging")).toHaveAttribute("aria-current", "page");
    view.unmount();

    renderSidebar({ pathname: "/forge/p/shop/environments" });
    sections = screen.getByTestId("sidebar-app-sections");
    const environments = within(sections).getByTestId("sidebar-app-section-environments");
    expect(environments).toHaveAttribute("aria-current", "page");
    expect(environments).toHaveAttribute("aria-expanded", "false");
    expect(within(sections).queryByTestId("sidebar-app-section-staging")).toBeNull();
  });

  // The apps and the chats scroll on their own, so an open app's sections
  // never push the chat list off the window.
  it("puts the apps and the chats in two regions that scroll on their own", () => {
    appsState.apps = [SHOP, BLOG];
    renderSidebar({ pathname: "/forge/p/shop/observability/errors" });
    const regions = screen.getByTestId("sidebar-regions");
    const apps = within(regions).getByTestId("sidebar-apps-scroller");
    const chats = within(regions).getByTestId("sidebar-chats");
    expect(Array.from(regions.children)).toEqual([apps, chats]);
    expect(apps).toContainElement(screen.getByTestId("sidebar-app-sections"));
    expect(apps.className).toMatch(/\boverflow-y-auto\b/);
    expect(apps.className).toMatch(/\bmin-h-0\b/);
    // The chats keep their heading in view and scroll their list.
    expect(chats).toContainElement(screen.getByTestId("chat-list-heading"));
    const list = within(chats).getByTestId("sidebar-scroller");
    expect(list.className).toMatch(/\boverflow-y-auto\b/);
    expect(list).not.toContainElement(screen.getByTestId("chat-list-heading"));
    // A floor of 40% of the room, and never under the heading plus five rows.
    expect(chats.className).toContain("min-h-[min(100%,max(40%,calc(5*var(--row-h)+3.25rem)))]");
    expect(chats.className).toMatch(/\bflex-1\b/);
    expect(apps).not.toContainElement(screen.getByTestId("chat-list-heading"));
  });

  it("gives the open app its rows before the app list answers", () => {
    renderSidebar({ pathname: "/forge/p/shop" });
    expect(screen.getByTestId("sidebar-app-shop")).toHaveAttribute("aria-expanded", "true");
    expect(within(screen.getByTestId("sidebar-app-sections")).getByTestId("sidebar-app-section-overview")).toHaveAttribute(
      "aria-current",
      "page"
    );
  });

  it("marks the place on screen, and nests Workflows' sections while it is open", () => {
    const onNavigate = vi.fn();
    const view = renderSidebar({ pathname: "/inbox", onNavigate });
    expect(screen.getByTestId("sidebar-inbox-button")).toHaveAttribute("aria-current", "page");
    expect(screen.queryByTestId("sidebar-workflows-sections")).not.toBeInTheDocument();
    view.unmount();

    renderSidebar({ pathname: "/workflows/runs", search: { project: "p1" }, onNavigate });
    expect(screen.getByTestId("sidebar-inbox-button")).not.toHaveAttribute("aria-current");
    const runs = screen.getByTestId("sidebar-workflows-runs");
    expect(runs).toHaveAttribute("aria-current", "page");
    expect(runs).toHaveAttribute("href", "/workflows/runs?project=p1");
    expect(screen.getByTestId("sidebar-workflows-workflows")).toHaveTextContent("Library");
    fireEvent.click(screen.getByTestId("sidebar-projects-button"));
    expect(onNavigate).toHaveBeenCalledWith("/projects");
  });
});
