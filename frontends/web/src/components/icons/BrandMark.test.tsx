import { render, screen } from "@testing-library/react";
import { BrandMark } from "./BrandMark";

it("renders decorative brand mark by default", () => {
  const { container } = render(<BrandMark />);

  const svg = container.querySelector("svg");
  expect(svg).toHaveAttribute("aria-hidden", "true");
  expect(svg).not.toHaveAttribute("aria-label");
});

it("renders titled brand mark when a title is provided", () => {
  render(<BrandMark title="Reliant" />);

  expect(screen.getByTitle("Reliant")).toBeInTheDocument();
});
