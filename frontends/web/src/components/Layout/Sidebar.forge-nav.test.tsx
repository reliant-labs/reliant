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
import { render, screen } from "@testing-library/react";
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
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
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

// The gate under test. Mocked at the hook boundary so each case can state the
// LIVE verdict directly, which is the thing the entry must follow.
// The EXPERIMENTAL gate, independent of the is-this-a-forge-project question.
// Mocked separately because the two conditions are independent and the entry
// requires BOTH — a test that could only vary one of them would pass while the
// other was ignored. Default true here so the pre-existing cases below exercise
// the forge-project logic they were written for.
const experimentalGate = vi.hoisted(() => ({ enabled: true }));

// Pin the build type to a PACKAGED PRODUCTION build. The default-resolution
// cases below consult the real gate, and under jsdom getIsDev() would otherwise
// answer true — which is the one setting where the old default already produced
// the new answer, so the assertions would not discriminate.
vi.mock("../../lib/constants", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/constants")>();
  return { ...actual, getIsDev: () => false, isDev: false };
});

// The mock keeps the real module reachable, so the default-resolution cases at
// the bottom can drive the gate from the genuine implementation rather than
// asserting against a value the test itself chose.
vi.mock("../../lib/forgeFeature", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/forgeFeature")>();
  return {
    ...actual,
    isForgeUIEnabled: () => experimentalGate.enabled,
  };
});

const {
  FORGE_UI_FLAG_KEY,
  isForgeUIEnabled: realIsForgeUIEnabled,
  setForgeUIEnabled: realSetForgeUIEnabled,
} = await vi.importActual<typeof import("../../lib/forgeFeature")>(
  "../../lib/forgeFeature",
);

function renderSidebar(props: Partial<Parameters<typeof Sidebar>[0]> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <Sidebar onOpenForge={vi.fn()} {...props} />
    </QueryClientProvider>
  );
}

describe("Sidebar forge nav entry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    experimentalGate.enabled = true;

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

  it("shows the entry whenever the experimental flag is on", () => {
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

  // The flag is the ONLY gate, so turning it off must remove the entry
  // completely — a packaged build with no opt-in behaves as it did before the
  // feature existed.
  it("hides the entry when the experimental gate is off", () => {
    experimentalGate.enabled = false;
    setProject(true);
    renderSidebar();
    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  /**
   * THE DEFAULT, end to end. Every other case in this file mocks the gate, so
   * none of them can see which way it actually resolves — this one drives the
   * mock from the REAL lib/forgeFeature against a clean localStorage, so the
   * shipped default is what decides the assertion.
   *
   * It fails on the previous default (production OFF, dev ON via getIsDev) and
   * passes now that the feature is on for everyone.
   */
  describe("the shipped default decides the entry", () => {
    it("shows the entry with no stored preference — production included", async () => {
      window.localStorage.removeItem(FORGE_UI_FLAG_KEY);
      experimentalGate.enabled = realIsForgeUIEnabled();

      setProject(true);
      renderSidebar();

      expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
    });

    it("hides it once the user explicitly opts out", async () => {
      realSetForgeUIEnabled(false);
      experimentalGate.enabled = realIsForgeUIEnabled();

      setProject(true);
      renderSidebar();

      expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
      window.localStorage.removeItem(FORGE_UI_FLAG_KEY);
    });
  });

  /** A nav entry's name: its label, without the key cap New chat shows. */
  const navLabel = (b: Element) => b.getAttribute("aria-label") ?? b.textContent?.trim();

  // Apps (the forge entry) sits directly under New chat: after writing code,
  // the next place to go is what that code runs as. Pinned because the
  // position is the product decision, not an accident of where the JSX was
  // appended.
  it("sits directly under New chat", () => {
    setProject(true);
    renderSidebar();
    const labels = screen
      .getByRole("navigation", { name: /chat sidebar navigation/i })
      .querySelectorAll("button");
    const text = Array.from(labels).map(navLabel);
    expect(text.slice(0, 2)).toEqual(["New chat", "Apps"]);
  });

  // The whole nav, in order. Workflows is ONE entry for the area — Library,
  // Runs and Automations are its tabs (WORKFLOW_UI.md §1.2) — and still sits
  // under the Inbox, above Search.
  it("orders the nav New chat, Apps, Inbox, Workflows, Search", () => {
    setProject(true);
    renderSidebar();
    const labels = screen
      .getByRole("navigation", { name: /chat sidebar navigation/i })
      .querySelectorAll("button");
    const text = Array.from(labels).map(navLabel);
    expect(text).toEqual(["New chat", "Apps", "Inbox", "Workflows", "Search"]);
  });

  it("orders the nav New chat, Inbox, Workflows, Search with Apps gated off", () => {
    experimentalGate.enabled = false;
    setProject(true);
    renderSidebar();
    const labels = screen
      .getByRole("navigation", { name: /chat sidebar navigation/i })
      .querySelectorAll("button");
    const text = Array.from(labels).map(navLabel);
    expect(text).toEqual(["New chat", "Inbox", "Workflows", "Search"]);
  });

  // The entry is LABELLED Apps but is still the forge entry underneath: the
  // test id (and the route it opens) keep the forge name.
  it("is labelled Apps under the forge test id", () => {
    setProject(true);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toHaveTextContent("Apps");
  });

  // Apps remains the ONE forge entry; the global-window project filter replaces
  // the former Projects navigation row without restoring a second forge entry.
  it("has no separate Observability entry", () => {
    setProject(true);
    renderSidebar();
    const text = Array.from(
      screen.getByRole("navigation", { name: /chat sidebar navigation/i }).querySelectorAll("button")
    ).map(navLabel);
    expect(text).toEqual(["New chat", "Apps", "Inbox", "Workflows", "Search"]);
    expect(screen.queryByTestId("sidebar-observability-button")).not.toBeInTheDocument();
  });
});

