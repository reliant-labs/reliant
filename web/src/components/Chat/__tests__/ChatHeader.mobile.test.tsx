import { useState } from "react";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";

const { rename } = vi.hoisted(() => ({ rename: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "c1", title: "Simple Greeting", lastMessageAt: new Date().toISOString() } }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: rename, isPending: false }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMessages: () => ({ data: { messages: [] } }) }));
vi.mock("../../../store/chatStoreHooks", () => ({
  useContextUsage: () => ({ threadTokenCount: 14000, compactionThreshold: 200000 }),
  useContextUsageByThread: () => ({}),
}));
vi.mock("../thread-views", () => ({ useThreads: () => [], ThreadTabs: () => null }));
vi.mock("../../runs/ChildRunsStrip", () => ({ ChildRunsStrip: () => null }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));

import { ChatHeader } from "../ChatHeader";
function Harness() {
  const [target, setTarget] = useState<HTMLDivElement | null>(null);
  return <SurfaceProvider surface="mobile">
    <header><h1>Simple Greeting</h1><div ref={setTarget} data-testid="header-actions" /></header>
    <ChatHeader chatId="c1" hideTitle actionsTarget={target} />
  </SurfaceProvider>;
}

describe("Mobile chat header", () => {
  it("places actions in the host header without repeating the title or timestamp", () => {
    renderWithQuery(<Harness />);
    expect(screen.getAllByRole("heading", { name: "Simple Greeting" })).toHaveLength(1);
    expect(within(screen.getByTestId("header-actions")).getByRole("button", { name: "More options" })).toBeInTheDocument();
    expect(screen.queryByText(/less than a minute/)).not.toBeInTheDocument();
  });
  it("opens mobile chat actions and renames through a dedicated sheet", async () => {
    renderWithQuery(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "More options" }));
    expect(screen.getByRole("dialog", { name: "Chat actions" })).toBeInTheDocument();
    expect(screen.queryByText("Open Terminal")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit Title" }));
    expect(screen.getByRole("dialog", { name: "Edit chat title" })).toBeInTheDocument();
    fireEvent.change(screen.getByRole("textbox", { name: "Chat title" }), { target: { value: "Updated title" } });
    fireEvent.click(screen.getByRole("button", { name: "Save title" }));
    await waitFor(() => expect(rename).toHaveBeenCalledWith({ chatId: "c1", title: "Updated title" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
