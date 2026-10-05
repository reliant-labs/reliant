import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { Tooltip, estimateTooltipWidth } from "../Tooltip";

describe("Tooltip", () => {
  it("caps the estimated width so long copy wraps", () => {
    expect(estimateTooltipWidth("Copy")).toBe(32);
    expect(estimateTooltipWidth("x".repeat(200))).toBe(320);
  });

  it("wraps long content and links it via aria-describedby on focus", async () => {
    const long = "This is a deliberately long tooltip that explains why the control is disabled";
    render(
      <Tooltip content={long} delay={0}>
        <button>Go</button>
      </Tooltip>,
    );
    const button = screen.getByText("Go");
    Object.defineProperty(button.parentElement!, "offsetParent", { value: document.body });
    await act(async () => {
      fireEvent.focus(button);
      await new Promise((r) => setTimeout(r, 10));
    });
    const tip = screen.getByRole("tooltip");
    expect(tip.className).toContain("whitespace-normal");
    expect(tip.className).not.toContain("whitespace-nowrap");
    expect(button.parentElement!.getAttribute("aria-describedby")).toBe(tip.id);
  });

  it("still shows for a disabled button because the wrapper receives the hover", async () => {
    render(
      <Tooltip content="Save the workflow first" delay={0}>
        <button disabled>Add</button>
      </Tooltip>,
    );
    const wrapper = screen.getByText("Add").parentElement!;
    Object.defineProperty(wrapper, "offsetParent", { value: document.body });
    await act(async () => {
      fireEvent.mouseEnter(wrapper);
      await new Promise((r) => setTimeout(r, 10));
    });
    expect(screen.getByRole("tooltip").textContent).toBe("Save the workflow first");
  });
});
