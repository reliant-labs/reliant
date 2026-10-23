// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { Modal } from "../Modal";

describe("Modal footer and sheet", () => {
  it("pins the footer outside the scrolling body and marks large dialogs as sheets on a phone", () => {
    render(
      <Modal isOpen onClose={() => undefined} title="Set up PR review" size="lg" footer={<button type="button">Turn on</button>}>
        <p>Body</p>
      </Modal>,
    );
    const footer = screen.getByTestId("modal-footer");
    expect(footer).toContainElement(screen.getByRole("button", { name: "Turn on" }));
    expect(footer.previousElementSibling).toHaveTextContent("Body");
    expect(document.querySelector("[data-sheet-on-mobile]")).not.toBeNull();
  });

  it("keeps small dialogs floating", () => {
    render(
      <Modal isOpen onClose={() => undefined} title="Confirm" size="sm">
        <p>Body</p>
      </Modal>,
    );
    expect(document.querySelector("[data-sheet-on-mobile]")).toBeNull();
    expect(screen.queryByTestId("modal-footer")).toBeNull();
  });
});
