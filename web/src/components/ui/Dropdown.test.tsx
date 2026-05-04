import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Dropdown } from "./Dropdown";

describe("Dropdown", () => {
  it("renders open content in a body portal", () => {
    const { container } = render(
      <div className="overflow-hidden">
        <Dropdown trigger={<button type="button">Open menu</button>} isOpen>
          <button type="button">Menu item</button>
        </Dropdown>
      </div>
    );

    expect(screen.getByText("Menu item")).toBeInTheDocument();
    expect(container).not.toContainElement(screen.getByText("Menu item"));
  });

  it("closes when clicking outside the trigger and portalled content", () => {
    render(
      <Dropdown>
        <button type="button">Menu item</button>
      </Dropdown>
    );

    fireEvent.click(screen.getByText("Select"));
    expect(screen.getByText("Menu item")).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByText("Menu item"));
    expect(screen.getByText("Menu item")).toBeInTheDocument();

    fireEvent.mouseDown(document.body);
    expect(screen.queryByText("Menu item")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("Select"));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByText("Menu item")).not.toBeInTheDocument();
  });
});
