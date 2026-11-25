import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ErrorRow } from "../ErrorRow";

describe("ErrorRow", () => {
  it("is one quiet line: no card border, no filled box", () => {
    render(<ErrorRow data-testid="row">The AI provider failed</ErrorRow>);
    const row = screen.getByTestId("row");
    expect(row.className).not.toMatch(/\bborder\b|\bbg-/);
    expect(screen.getByText("The AI provider failed")).toBeInTheDocument();
    // Nothing to open: the line is not a disclosure.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("opens its details beneath the line from the chevron, by click or keyboard", async () => {
    const user = userEvent.setup();
    render(
      <ErrorRow details={<pre>stack trace here</pre>} ariaLabel="The AI provider failed. Click for details">
        The AI provider failed
      </ErrorRow>,
    );
    const disclosure = screen.getByRole("button", { name: /Click for details/ });
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("stack trace here")).not.toBeInTheDocument();

    await user.click(disclosure);
    expect(disclosure).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("stack trace here")).toBeInTheDocument();
    expect(disclosure.getAttribute("aria-controls")).toBe(screen.getByText("stack trace here").parentElement?.id);

    disclosure.focus();
    await user.keyboard("{Enter}");
    expect(screen.queryByText("stack trace here")).not.toBeInTheDocument();
  });

  it("keeps actions outside the disclosure, reachable by keyboard", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    render(
      <ErrorRow
        details={<pre>detail</pre>}
        ariaLabel="Failed. Click for details"
        actions={<button type="button" onClick={onRetry}>Retry</button>}
        persistentActions={<button type="button">Open Settings</button>}
      >
        Failed
      </ErrorRow>,
    );
    const disclosure = screen.getByRole("button", { name: /Click for details/ });
    const retry = screen.getByRole("button", { name: "Retry" });
    expect(disclosure).not.toContainElement(retry);
    expect(disclosure).not.toContainElement(screen.getByRole("button", { name: "Open Settings" }));

    await user.tab();
    expect(disclosure).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Open Settings" })).toHaveFocus();
    await user.tab();
    expect(retry).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onRetry).toHaveBeenCalledTimes(1);
    // An action never also opens the row.
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
  });

  it("keeps the role and live region it is given", () => {
    render(
      <ErrorRow role="alert" aria-live="assertive" data-testid="row">
        Could not save
      </ErrorRow>,
    );
    const row = screen.getByRole("alert");
    expect(row).toBe(screen.getByTestId("row"));
    expect(row).toHaveAttribute("aria-live", "assertive");
  });
});
