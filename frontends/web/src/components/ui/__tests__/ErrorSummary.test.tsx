// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ErrorSummary } from "../ErrorSummary";
import { Modal } from "../Modal";

describe("ErrorSummary", () => {
  it("takes focus, is announced, and moves focus to the field a link names", async () => {
    const user = userEvent.setup();
    render(
      <>
        <ErrorSummary title="Fix these to continue" items={[{ fieldId: "repos", message: "Choose at least one repository." }, { message: "The server is unreachable." }]} />
        <input id="repos" aria-label="Repositories" />
      </>,
    );
    const summary = screen.getByRole("alert", { name: "Fix these to continue" });
    expect(summary).toHaveFocus();
    expect(screen.getByText("The server is unreachable.").closest("a")).toBeNull();
    await user.click(screen.getByRole("link", { name: "Choose at least one repository." }));
    expect(screen.getByLabelText("Repositories")).toHaveFocus();
  });
});

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
