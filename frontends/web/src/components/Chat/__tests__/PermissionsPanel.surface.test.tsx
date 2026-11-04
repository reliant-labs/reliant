import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { PermissionsPanel } from "../PermissionsPanel";
import { create } from "@bufbuild/protobuf";
import { ApprovalSchema, ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

// The approvals ChatPresenter mounts above the composer. On a 390px viewport
// a desktop `inline-flex` row does not fit, and the keyboard-shortcut badge
// is meaningless without a keyboard.
const approval = (id: string, title: string) =>
  create(ApprovalSchema, {
    id,
    chatId: "chat-1",
    entityId: `block-${id}`,
    title,
    status: ApprovalStatus.PENDING,
    createdAt: new Date().toISOString(),
  });
const pending: Approval[] = [approval("approval-1", "Run the migration?"), approval("approval-2", "Deploy?")];

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: { approvals: pending } }),
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
  useBatchApproveInChat: () => ({ mutate: vi.fn() }),
  useBatchDenyInChat: () => ({ mutate: vi.fn() }),
}));

describe("PermissionsPanel approvals", () => {
  it("renders per-item and batch actions on every surface", () => {
    for (const surface of ["desktop", "mobile", "embed"] as const) {
      const { unmount } = render(
        <SurfaceProvider surface={surface}>
          <PermissionsPanel chatId="chat-1" />
        </SurfaceProvider>,
      );

      expect(screen.getByText("2 requests")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Approve: Run the migration?" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /approve all 2/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /deny all 2/i })).toBeInTheDocument();

      unmount();
    }
  });

  it("gives the buttons 44px touch targets and drops the shortcut badge on mobile", () => {
    render(
      <SurfaceProvider surface="mobile">
        <PermissionsPanel chatId="chat-1" />
      </SurfaceProvider>,
    );

    const approve = screen.getByRole("button", { name: /approve all/i });
    const deny = screen.getByRole("button", { name: /deny all/i });
    expect(approve.className).toMatch(/min-h-\[44px\]/);
    expect(deny.className).toMatch(/min-h-\[44px\]/);
    expect(screen.getByRole("button", { name: "Approve: Deploy?" }).className).toMatch(/min-h-\[44px\]/);
    expect(approve.textContent).not.toMatch(/\+↵/);
  });

  it("lets the batch bar span the viewport instead of sizing to content on mobile", () => {
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
    expect(screen.getByRole("button", { name: /approve all/i }).textContent).toMatch(/\+↵/);
  });
});
