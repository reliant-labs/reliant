/**
 * The chats layout (mobile-chat-redesign §4.1, §9): one pane below 840px,
 * list–detail at and above it, and a routed chat that survives the window
 * crossing the breakpoint.
 *
 * Rendered against a real router with the real route shape (a pathless
 * layout owning `/m/chats` and `/m/chats/$chatId`), so the layout reads the
 * router's own params. The width comes from a controllable `matchMedia`, the
 * one input `useExpandedLayout` reads.
 */

import { useEffect } from "react";
import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useParams,
} from "@tanstack/react-router";

// The list has its own tests; here it only has to be findable.
vi.mock("../MobileChatList", () => ({
  MobileChatList: () => <div data-testid="chat-list">chat list</div>,
}));

const { MobileSplitLayout, MobileChatsEmptyDetail } = await import("../MobileSplitLayout");
const { EXPANDED_LAYOUT_QUERY, useChatListPaneVisible } = await import("../mobileSplitPane");

// ─── matchMedia with a width we can move ─────────────────────────────────────

let viewportWidth = 390;
const listeners = new Set<() => void>();

function setWidth(width: number) {
  viewportWidth = width;
  act(() => listeners.forEach((l) => l()));
}

beforeEach(() => {
  viewportWidth = 390;
  vi.stubGlobal("matchMedia", (query: string) => ({
    get matches() {
      // Only the one query the layout asks; anything else would be a new input.
      expect(query).toBe(EXPANDED_LAYOUT_QUERY);
      return viewportWidth >= 840;
    },
    media: query,
    addEventListener: (_: string, l: () => void) => listeners.add(l),
    removeEventListener: (_: string, l: () => void) => listeners.delete(l),
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  listeners.clear();
});

// ─── Route tree mirroring routes.tsx's chats section ─────────────────────────

let chatMounts = 0;

function ChatScreenProbe() {
  const { chatId } = useParams({ strict: false });
  const listPaneVisible = useChatListPaneVisible();
  useEffect(() => {
    chatMounts += 1;
  }, []);
  return (
    <div data-testid="chat-screen">
      chat:{chatId} list-pane:{String(listPaneVisible)}
    </div>
  );
}

function renderAt(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const chatsLayout = createRoute({ getParentRoute: () => rootRoute, id: "_chats", component: MobileSplitLayout });
  const chats = createRoute({ getParentRoute: () => chatsLayout, path: "/m/chats", component: MobileChatsEmptyDetail });
  const chat = createRoute({ getParentRoute: () => chatsLayout, path: "/m/chats/$chatId", component: ChatScreenProbe });
  const newChat = createRoute({ getParentRoute: () => rootRoute, path: "/m/new", component: () => <div>new chat</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([chatsLayout.addChildren([chats, chat]), newChat]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  chatMounts = 0;
});

describe("MobileSplitLayout below 840px", () => {
  it("shows only the list at /m/chats", async () => {
    renderAt("/m/chats");
    expect(await screen.findByTestId("chat-list")).toBeVisible();
    expect(screen.getByTestId("mobile-chats-layout")).toHaveAttribute("data-layout", "single");
    // The empty detail is kept hidden, not shown under the list.
    expect(screen.getByTestId("mobile-chat-detail")).toHaveClass("hidden");
  });

  it("shows only the chat at /m/chats/$chatId, with its back control", async () => {
    renderAt("/m/chats/c1");
    expect(await screen.findByTestId("chat-screen")).toHaveTextContent("chat:c1 list-pane:false");
    expect(screen.queryByTestId("chat-list")).not.toBeInTheDocument();
  });
});

describe("MobileSplitLayout at 840px and up", () => {
  beforeEach(() => {
    viewportWidth = 1024;
  });

  it("puts the list beside a calm empty detail at /m/chats", async () => {
    renderAt("/m/chats");
    expect(await screen.findByTestId("chat-list")).toBeInTheDocument();
    expect(screen.getByTestId("mobile-chats-layout")).toHaveAttribute("data-layout", "split");
    expect(screen.getByRole("complementary", { name: "Chats" })).toHaveClass("w-80");
    expect(screen.getByTestId("mobile-chat-detail")).not.toHaveClass("hidden");
    expect(screen.getByText("Select a chat")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New chat" })).toHaveAttribute("href", "/m/new");
  });

  it("keeps the list beside the chat and tells the chat it is there", async () => {
    renderAt("/m/chats/c1");
    expect(await screen.findByTestId("chat-screen")).toHaveTextContent("chat:c1 list-pane:true");
    expect(screen.getByTestId("chat-list")).toBeInTheDocument();
  });

  it("splits from exactly 840px", async () => {
    viewportWidth = 840;
    renderAt("/m/chats/c1");
    expect(await screen.findByTestId("chat-list")).toBeInTheDocument();
    setWidth(839);
    expect(screen.queryByTestId("chat-list")).not.toBeInTheDocument();
  });
});

describe("MobileSplitLayout across the breakpoint", () => {
  it("does not remount the routed chat when the window crosses 840px", async () => {
    renderAt("/m/chats/c1");
    await screen.findByTestId("chat-screen");
    expect(chatMounts).toBe(1);

    setWidth(1024);
    expect(screen.getByTestId("chat-list")).toBeInTheDocument();
    expect(screen.getByTestId("chat-screen")).toHaveTextContent("list-pane:true");

    setWidth(600);
    expect(screen.queryByTestId("chat-list")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-screen")).toHaveTextContent("list-pane:false");

    expect(chatMounts).toBe(1);
  });

  it("keeps the list mounted while switching chats beside it", async () => {
    viewportWidth = 1024;
    const router = renderAt("/m/chats/c1");
    const list = await screen.findByTestId("chat-list");
    await act(async () => {
      router.history.push("/m/chats/c2");
    });
    expect(await screen.findByText(/chat:c2/)).toBeInTheDocument();
    expect(screen.getByTestId("chat-list")).toBe(list);
  });
});
