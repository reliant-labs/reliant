/**
 * The no-target step card (JourneyTour's TourCard): narrow and non-modal by
 * default; with JourneyStep.card `{ wide, modal }` it is a dialog that holds
 * the page still (scroll locked while it is open, restored after) and takes
 * the backdrop's clicks. Neither draws a spotlight cutout.
 */
import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { TourCard } from "../JourneyTour";

afterEach(() => {
  document.documentElement.style.overflow = "";
  document.body.style.overflow = "";
});

describe("<TourCard>", () => {
  it("is a narrow, non-modal status card by default, and leaves the page's scroll alone", () => {
    render(<TourCard title="A card" description="Words" />);
    const card = screen.getByTestId("tour-card");
    expect(card).toHaveAttribute("role", "status");
    expect(card.className).toContain("max-w-md");
    expect(screen.getByTestId("tour-card-backdrop").className).not.toContain("pointer-events-auto");
    expect(document.body.style.overflow).toBe("");
  });

  it("wide and modal: a dialog with room, the page scroll-locked until it closes, the backdrop taking clicks", () => {
    document.body.style.overflow = "auto";
    const { unmount } = render(<TourCard title="The run" description={<div data-testid="inside">player</div>} wide modal />);
    const card = screen.getByRole("dialog", { name: "The run" });
    expect(card).toHaveAttribute("aria-modal", "true");
    expect(card.className).toContain("max-w-4xl");
    expect(card.className).toContain("overflow-y-auto");
    expect(screen.getByTestId("inside")).toBeInTheDocument();
    expect(screen.getByTestId("tour-card-backdrop").className).toContain("pointer-events-auto");
    expect(document.documentElement.style.overflow).toBe("hidden");
    expect(document.body.style.overflow).toBe("hidden");
    unmount();
    expect(document.documentElement.style.overflow).toBe("");
    expect(document.body.style.overflow).toBe("auto");
  });
});
