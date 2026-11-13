/**
 * "Shared with the org": other members' shared chats for the active org, kept
 * current by the org feed, opened read-only. Nothing at all when the person
 * has no org or nothing is shared.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";

const state = vi.hoisted(() => ({
  activeOrgId: "acme" as string | null,
  chats: [] as Array<Record<string, unknown>>,
}));
const mocks = vi.hoisted(() => ({
  useOrgFeed: vi.fn(),
  openSharedChat: vi.fn(),
}));

vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    activeOrgId: state.activeOrgId,
    active: state.activeOrgId ? { id: state.activeOrgId, name: "Acme", role: "member" } : null,
    orgs: [],
  }),
}));
vi.mock("@/hooks/sharing-queries", () => ({
  useOrgFeed: mocks.useOrgFeed,
  useSharedChats: () => ({ data: state.chats }),
  useOrgMemberNames: () => new Map([["dana", "Dana"]]),
}));
vi.mock("@/store/openChat", () => ({ openSharedChat: mocks.openSharedChat }));

const { SharedWithOrgSection } = await import("../SharedWithOrgSection");

beforeEach(() => {
  vi.clearAllMocks();
  state.activeOrgId = "acme";
  state.chats = [
    { id: "c1", title: "Fix the flaky deploy", userId: "dana", activity: ChatActivity.RUNNING },
    { id: "c2", title: "", userId: "someone-left", activity: ChatActivity.IDLE },
  ];
});

describe("SharedWithOrgSection", () => {
  it("lists the org's shared chats with whose they are, and follows the org's feed", () => {
    render(<SharedWithOrgSection activeChatId={null} />);
    expect(screen.getByRole("region", { name: "Shared with Acme" })).toBeInTheDocument();
    const first = screen.getByTestId("shared-chat-c1");
    expect(first).toHaveTextContent("Fix the flaky deploy");
    expect(first).toHaveTextContent("Shared by Dana");
    expect(screen.getByRole("img", { name: "Running" })).toBeInTheDocument();
    expect(screen.getByTestId("shared-chat-c2")).toHaveTextContent("Untitled chat");
    expect(screen.getByTestId("shared-chat-c2")).toHaveTextContent("Shared by a member");
    expect(mocks.useOrgFeed).toHaveBeenCalledWith("acme");
  });

  it("opens a shared chat read-only, without switching the viewer's project", () => {
    render(<SharedWithOrgSection activeChatId={null} />);
    fireEvent.click(screen.getByTestId("shared-chat-c1"));
    expect(mocks.openSharedChat).toHaveBeenCalledWith(expect.objectContaining({ id: "c1", userId: "dana" }));
  });

  it("marks the open shared chat", () => {
    render(<SharedWithOrgSection activeChatId="c1" />);
    expect(screen.getByTestId("shared-chat-c1")).toHaveAttribute("aria-current", "true");
    expect(screen.getByTestId("shared-chat-c2")).not.toHaveAttribute("aria-current");
  });

  it("collapses", () => {
    render(<SharedWithOrgSection activeChatId={null} />);
    fireEvent.click(screen.getByRole("button", { name: /Shared with Acme/ }));
    expect(screen.queryByTestId("shared-chat-c1")).not.toBeInTheDocument();
  });

  it("renders nothing when nothing is shared, or without an org", () => {
    state.chats = [];
    const { container, rerender } = render(<SharedWithOrgSection activeChatId={null} />);
    expect(container).toBeEmptyDOMElement();
    state.activeOrgId = null;
    state.chats = [{ id: "c1", title: "x", userId: "dana" }];
    rerender(<SharedWithOrgSection activeChatId={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
