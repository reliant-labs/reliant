/**
 * WorkflowStarterCards — the grid responds to its OWN width, not the viewport.
 *
 * THE BUG THIS LOCKS IN: the terminal now defaults to open, so the chat pane
 * is narrow while the WINDOW is still wide. The grid was keyed to viewport
 * breakpoints (`lg:grid-cols-3`), so a wide window kept three columns inside a
 * ~600px pane and every card collapsed to one word per line. The featured
 * card's "Recommended" badge was absolutely positioned against a fixed `pr-32`
 * gutter, so it landed on top of the title at the same widths.
 *
 * Container queries are the fix, and asserting on them is the only way to
 * catch a regression here: jsdom computes no layout, so a "does it wrap" test
 * would pass against the broken code.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/store/chatParamsStore", () => ({
  useChatParamsStore: {
    getState: () => ({
      setTempNewChatWorkflow: vi.fn(),
      setTempNewChatParams: vi.fn(),
      setTempNewChatPresets: vi.fn(),
    }),
  },
}));

import { WorkflowStarterCards } from "../WorkflowStarterCards";

/** The element that actually carries the grid-template-columns utilities. */
function renderGrid() {
  const { container } = render(<WorkflowStarterCards />);
  const grid = container.querySelector(".grid");
  if (!grid) throw new Error("starter card grid not found");
  return { container, grid };
}

describe("WorkflowStarterCards layout", () => {
  it("sizes columns from the container, not the viewport", () => {
    const { container, grid } = renderGrid();

    // A container must be established, or the @-prefixed utilities below
    // resolve against the nearest ancestor container — or nothing at all.
    expect(container.querySelector(".\\@container")).not.toBeNull();

    const cls = grid.className;
    expect(cls).toContain("grid-cols-1");
    expect(cls).toContain("@2xl:grid-cols-2");
    expect(cls).toContain("@4xl:grid-cols-3");

    // The regression itself: viewport breakpoints are what made a wide window
    // force three columns into a narrow pane.
    expect(cls).not.toMatch(/(?:^|\s)(?:sm|md|lg|xl):grid-cols-/);
  });

  it("spans the featured card using the same container thresholds", () => {
    renderGrid();

    const featured = screen
      .getByText("Build something new with Forge")
      .closest("[role='button']");
    expect(featured).not.toBeNull();

    const cls = (featured as HTMLElement).className;
    expect(cls).toContain("@2xl:col-span-2");
    expect(cls).toContain("@4xl:col-span-3");

    // A viewport-keyed span against a container-keyed grid is the worst case:
    // the card spans 3 while the grid has 1 column, overflowing the pane.
    expect(cls).not.toMatch(/(?:^|\s)(?:sm|md|lg|xl):col-span-/);
  });

  it("keeps the Recommended badge in flow with the title", () => {
    renderGrid();

    const badge = screen.getByText("Recommended");
    const title = screen.getByText("Build something new with Forge");

    // Absolute positioning + a fixed pr-32 reservation is what put the badge
    // on top of the title once the card got narrow.
    expect(badge.className).not.toContain("absolute");
    expect(title.className).not.toContain("pr-32");

    // Same flex row, so they wrap onto separate lines instead of overlapping.
    expect(title.parentElement).toBe(badge.parentElement);
    expect(title.parentElement?.className).toContain("flex-wrap");
  });
});
