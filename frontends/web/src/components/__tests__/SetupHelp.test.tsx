import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { SetupHelp } from "../SetupHelp";
import { OAuthHelperPanel } from "../OAuthHelperPanel";
import { vi } from "vitest";

it("uses a closed native disclosure with a named, touch-sized keyboard target", async () => {
  render(<SetupHelp><p>Open Terminal, paste, then press Enter.</p></SetupHelp>);
  const summary = screen.getByText("More help");
  const details = summary.closest("details")!;
  expect(details).not.toHaveAttribute("open");
  expect(summary.tagName).toBe("SUMMARY");
  expect(summary).toHaveClass("min-h-[44px]");
  await userEvent.click(summary);
  expect(details).toHaveAttribute("open");
  await userEvent.click(summary);
  expect(details).not.toHaveAttribute("open");
  // Native summary handles Enter/Space in browsers; browser screenshot harness
  // verifies those keys (jsdom doesn't implement their default action).
});

it("keeps provider setup actions and Copy visible; secondary help starts closed", async () => {
  render(<OAuthHelperPanel providerName="Claude" available={false} loading={false} connecting={false} onRetry={vi.fn()} onConnect={vi.fn()} />);
  expect(screen.getByText("1. Download / install")).toBeVisible();
  expect(screen.getByText("2. Run the command")).toBeVisible();
  const copy = screen.getByRole("button", { name: "Copy sign-in helper command" });
  expect(copy).toBeVisible();
  expect(copy).toHaveClass("min-h-[44px]", "min-w-[44px]");
  const summary = screen.getByText("More help");
  expect(summary.closest("details")).not.toHaveAttribute("open");
  await userEvent.click(summary);
  expect(summary.closest("details")).toHaveAttribute("open");
  expect(screen.getByText(/Keep the terminal open/)).toBeVisible();
});
