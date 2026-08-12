import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { PermissionsPanel } from "../PermissionsPanel";
import { ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import type { ToolApprovalRequest } from "../../../api/client";

// The batch "N Pending / Approve All / Deny All" bar ChatPresenter mounts
// above the composer. On a 390px viewport its desktop `inline-flex` row does
// not fit, and the keyboard-shortcut badge is meaningless without a keyboard.
const pending: ToolApprovalRequest[] = [
  {
    id: "approval-1",
    chat_id: "chat-1",
    content_block_id: "block-1",
    tool_name: "edit",
    status: ApprovalStatus.PENDING,
    created_at: new Date().toISOString(),
  },
];

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: pending }),
  useBatchApprove: () => ({ mutate: vi.fn() }),
  useBatchDeny: () => ({ mutate: vi.fn() }),
}));

describe("PermissionsPanel batch approval bar", () => {
  it("renders both batch actions on every surface", () => {
    for (const surface of ["desktop", "mobile", "embed"] as const) {
      const { unmount } = render(
        <SurfaceProvider surface={surface}>
          <PermissionsPanel chatId="chat-1" />
        </SurfaceProvider>,
      );

      expect(screen.getByText("1 Pending")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /approve all/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /deny all/i })).toBeInTheDocument();

      unmount();
    }
  });

  it("gives the batch buttons 44px touch targets and drops the shortcut badge on mobile", () => {
    render(
      <SurfaceProvider surface="mobile">
        <PermissionsPanel chatId="chat-1" />
      </SurfaceProvider>,
    );

    const approve = screen.getByRole("button", { name: /approve all/i });
    const deny = screen.getByRole("button", { name: /deny all/i });
    expect(approve.className).toMatch(/min-h-\[44px\]/);
    expect(deny.className).toMatch(/min-h-\[44px\]/);
    expect(approve.textContent).not.toMatch(/\+↵/);
  });

  it("lets the panel span the viewport instead of sizing to content on mobile", () => {
    render(
      <SurfaceProvider surface="mobile">
        <PermissionsPanel chatId="chat-1" />
      </SurfaceProvider>,
    );

    const panel = screen.getByTestId("permissions-popup");
    expect(panel.className).toMatch(/w-full/);
    expect(panel.className).not.toMatch(/inline-flex/);
  });

  it("keeps the content-sized desktop bar with its shortcut hint", () => {
    render(
      <SurfaceProvider surface="desktop">
        <PermissionsPanel chatId="chat-1" />
      </SurfaceProvider>,
    );

    const panel = screen.getByTestId("permissions-popup");
    expect(panel.className).toMatch(/inline-flex/);
    expect(
      screen.getByRole("button", { name: /approve all/i }).textContent,
    ).toMatch(/\+↵/);
  });
});
