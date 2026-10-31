import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { PermissionsPanel } from "../PermissionsPanel";
import { create } from "@bufbuild/protobuf";
import { ApprovalSchema, ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

// The batch "N Pending / Approve All / Deny All" bar ChatPresenter mounts
// above the composer. On a 390px viewport its desktop `inline-flex` row does
// not fit, and the keyboard-shortcut badge is meaningless without a keyboard.
// Two, so the bar is the batch's ("Approve all 2"); one alone reads "Approve".
const pending: Approval[] = ["approval-1", "approval-2"].map((id) =>
  create(ApprovalSchema, {
    id,
    chatId: "chat-1",
    entityId: `block-${id}`,
    status: ApprovalStatus.PENDING,
    createdAt: new Date().toISOString(),
  }),
);

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: { approvals: pending } }),
  useBatchApproveInChat: () => ({ mutate: vi.fn() }),
  useBatchDenyInChat: () => ({ mutate: vi.fn() }),
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));

describe("PermissionsPanel batch approval bar", () => {
  it("renders both batch actions on every surface", () => {
    for (const surface of ["desktop", "mobile", "embed"] as const) {
      const { unmount } = render(
        <SurfaceProvider surface={surface}>
          <PermissionsPanel chatId="chat-1" />
        </SurfaceProvider>,
      );

      expect(screen.getByText("2 Pending")).toBeInTheDocument();
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
