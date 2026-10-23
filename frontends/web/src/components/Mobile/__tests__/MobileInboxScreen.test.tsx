/**
 * `/m/inbox` — the desktop Inbox on a phone. Pins that it is the same view
 * (same data, same actions, async questions answerable through the same
 * QuestionPrompt + ResolveQuestion path) and that what a phone cannot follow
 * is translated: a run opens as its chat, desktop-only destinations say so.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
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

import { ListInboxResponseSchema, type InboxItem } from "@/gen/reliant/v1/inbox_pb";
import { useInboxScopeStore } from "@/hooks/inbox-queries";
import { SurfaceProvider } from "@/lib/surfaceContext";
import { useMobileDrawerStore } from "@/store/mobileDrawerStore";
import { approvalItem, needsSetupItem, questionItem, waitingItem } from "../../inbox/__tests__/inboxTestUtils";

const mocks = vi.hoisted(() => ({
  listInbox: vi.fn(),
  approve: vi.fn(),
  deny: vi.fn(),
  resolveQuestion: vi.fn(),
  resumeDaemon: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    inbox: () => ({ listInbox: mocks.listInbox, dismissInboxItem: vi.fn(), restoreInboxItem: vi.fn(), cleanupStorage: vi.fn() }),
    daemonRegistry: () => ({ clearMachineCaches: vi.fn() }),
  },
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useResumeDaemon: () => ({ mutate: (id: string) => mocks.resumeDaemon(id), isPending: false, variables: undefined }),
}));
vi.mock("@/hooks/trigger-queries", () => ({
  useSetTriggerEnabled: () => ({ mutate: vi.fn(), isPending: false }),
  useTriggerById: () => undefined,
}));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/toast-manager", () => ({ toast: { notify: vi.fn(), error: vi.fn() } }));

const { MobileInboxScreen } = await import("../MobileInboxScreen");

function respond(items: InboxItem[]) {
  mocks.listInbox.mockResolvedValue(
    create(ListInboxResponseSchema, {
      items,
      blockingCount: items.filter((i) => i.kind <= 3).length,
      hasInformational: items.some((i) => i.kind >= 4),
    }),
  );
}

function renderScreen() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/m/inbox", component: MobileInboxScreen }),
    createRoute({ getParentRoute: () => rootRoute, path: "/m/chats/$chatId", component: () => <p>chat screen</p> }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ["/m/inbox"] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <SurfaceProvider surface="mobile">
        <RouterProvider router={router} />
      </SurfaceProvider>
    </QueryClientProvider>,
  );
  return router;
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.approve.mockResolvedValue({ success: true, message: "" });
  mocks.resolveQuestion.mockResolvedValue({});
  // Approvals and questions are on their generated hooks: answered at the transport.
  setTransport(
    mockTransport({
      "ApprovalService.Approve": (req) => {
        const { requestId, actionTaken } = req as { requestId: string; actionTaken?: string };
        return mocks.approve(requestId, actionTaken);
      },
      "ApprovalService.Deny": (req) => {
        const { requestId, denialReason, actionTaken } = req as { requestId: string; denialReason?: string; actionTaken?: string };
        return mocks.deny(requestId, denialReason, actionTaken);
      },
      "QuestionService.ResolveQuestion": (req) => {
        const { questionId, action, responseData } = req as { questionId: string; action: string; responseData?: string };
        return mocks.resolveQuestion(questionId, action, responseData);
      },
    }),
  );
  useInboxScopeStore.setState({ scope: "all" });
  useMobileDrawerStore.setState({ isOpen: false });
});

describe("MobileInboxScreen", () => {
  it("renders the Inbox under the mobile header, with the way into the menu", async () => {
    respond([approvalItem()]);
    renderScreen();
    expect(await screen.findByRole("heading", { level: 1, name: "Inbox" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Open menu/ }));
    expect(useMobileDrawerStore.getState().isOpen).toBe(true);
  });

  it("approves from the row, through the same approval call as desktop", async () => {
    respond([approvalItem()]);
    renderScreen();
    const row = await screen.findByTestId("inbox-item-approval:appr-1");
    await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(mocks.approve).toHaveBeenCalledWith("appr-1", undefined));
  });

  it("answers an async question through the Inbox's own answer form", async () => {
    const base = questionItem();
    const asyncQuestion = questionItem({
      payload: { case: "question", value: { ...(base.payload.value as object), async: true } as never },
    });
    respond([asyncQuestion]);
    renderScreen();
    const row = await screen.findByTestId("inbox-item-question:q-1");
    expect(within(row).getByText(/Not blocking/)).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: /Answer/ }));
    await userEvent.click(within(row).getByRole("radio", { name: /main/ }));
    await userEvent.click(within(row).getByRole("button", { name: /Submit/ }));
    await waitFor(() =>
      expect(mocks.resolveQuestion).toHaveBeenCalledWith(
        "q-1",
        "reply",
        JSON.stringify({ answers: [{ question: "Which branch?", selected: ["main"] }] }),
      ),
    );
  });

  it("opens a run as its chat: the Runs area is not on a phone", async () => {
    respond([waitingItem()]);
    const router = renderScreen();
    const row = await screen.findByTestId("inbox-item-waiting_for_machine:chat-1@1");
    const link = within(row).getByRole("link", { name: "Nightly triage" });
    expect(link).toHaveAttribute("href", "/m/chats/chat-1");
    await userEvent.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe("/m/chats/chat-1"));
  });

  it("says a desktop-only fix is on desktop rather than linking away", async () => {
    respond([needsSetupItem()]);
    renderScreen();
    expect(await screen.findByText("Finish on desktop")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Finish setup" })).toBeNull();
  });
});
