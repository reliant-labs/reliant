/**
 * The hamburger carries the Inbox badge: the Inbox lives behind it, so an
 * approval or question waiting on the user must show without opening the
 * drawer.
 */

import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { EMPTY_INBOX_BADGE, useMobileDrawerStore } from "../../../store/mobileDrawerStore";
import { MobileMenuButton } from "../MobileMenuButton";

beforeEach(() => {
  useMobileDrawerStore.setState({ isOpen: false, inboxBadge: EMPTY_INBOX_BADGE });
});

describe("MobileMenuButton", () => {
  it("opens the drawer", async () => {
    render(<MobileMenuButton />);
    await userEvent.click(screen.getByRole("button", { name: "Open menu" }));
    expect(useMobileDrawerStore.getState().isOpen).toBe(true);
  });

  it("has no mark when nothing waits", () => {
    render(<MobileMenuButton />);
    expect(screen.queryByTestId("menu-inbox-dot")).toBeNull();
  });

  it("marks waiting items and names them in its accessible name", () => {
    useMobileDrawerStore.setState({
      inboxBadge: { count: 2, informational: false, degraded: false, announcement: "2 items need you" },
    });
    render(<MobileMenuButton />);
    expect(screen.getByTestId("menu-inbox-dot")).toHaveClass("bg-primary");
    expect(screen.getByRole("button", { name: "Open menu, 2 items need you" })).toBeInTheDocument();
  });

  it("marks automation problems alone in the destructive colour", () => {
    useMobileDrawerStore.setState({
      inboxBadge: { count: 0, informational: true, degraded: false, announcement: "Automation problems" },
    });
    render(<MobileMenuButton />);
    expect(screen.getByTestId("menu-inbox-dot")).toHaveClass("bg-destructive");
  });
});
