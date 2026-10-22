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

  it("toggles an uncontrolled dropdown from a custom trigger", () => {
    render(
      <Dropdown trigger={<button type="button">Open menu</button>}>
        <button type="button">Menu item</button>
      </Dropdown>
    );

    expect(screen.queryByText("Menu item")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("Open menu"));
    expect(screen.getByText("Menu item")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Open menu"));
    expect(screen.queryByText("Menu item")).not.toBeInTheDocument();
  });

  it("leaves a controlled dropdown's custom trigger to the caller", () => {
    render(
      <Dropdown trigger={<button type="button">Open menu</button>} isOpen={false} onOpenChange={() => {}}>
        <button type="button">Menu item</button>
      </Dropdown>
    );

    fireEvent.click(screen.getByText("Open menu"));
    expect(screen.queryByText("Menu item")).not.toBeInTheDocument();
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
