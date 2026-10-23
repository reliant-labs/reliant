// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ErrorSummary } from "../ErrorSummary";

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
    // One alert, not a wrapper around the banner's own: announced once.
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.getByText("The server is unreachable.").closest("a")).toBeNull();
    await user.click(screen.getByRole("link", { name: "Choose at least one repository." }));
    expect(screen.getByLabelText("Repositories")).toHaveFocus();
  });
});
