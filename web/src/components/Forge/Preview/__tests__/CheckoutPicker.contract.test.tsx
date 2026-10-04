// Copyright (c) 2025 Reliant Labs

/**
 * THE PICKER'S CONTRACT, and both halves are about not lying to the person
 * about to ship code.
 *
 * A DIRTY checkout builds from uncommitted changes, so what deploys exists on
 * nobody else's machine and in no commit. Legitimate for a preview, and worth
 * knowing before approving a deploy.
 *
 * The distance from main is OMITTED, not zeroed, when forge could not compare.
 * "In step with main" and "I could not tell" are different answers, and
 * rendering the second as the first is a confident claim about how current the
 * code is — made to the one person who needs it to be true.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CheckoutPicker } from "../CheckoutPicker";
import {
  aheadBehindKnown,
  distanceFromMain,
  isOfferedCheckout,
  selectableCheckouts,
  type ForgeCheckoutsReport,
} from "@/services/forge/checkouts";

function report(): ForgeCheckoutsReport {
  return {
    project: "reliant",
    main_ref: "origin/main",
    checkouts: [
      // The remote entry has NO path: there is nothing on disk to render.
      { label: "origin/main", kind: "remote", head: "aaa111" },
      {
        label: "main",
        kind: "worktree",
        path: "/src/reliant",
        branch: "main",
        head: "aaa111",
        dirty: false,
        ahead_of_main: 0,
        behind_main: 0,
        selected: true,
      },
      {
        label: "fix/thing",
        kind: "worktree",
        path: "/src/wt/fix-thing",
        branch: "fix/thing",
        head: "bbb222",
        dirty: true,
        ahead_of_main: 3,
        behind_main: 1,
      },
      {
        // No ahead/behind at all: main was never fetched.
        label: "spike",
        kind: "worktree",
        path: "/src/wt/spike",
        branch: "spike",
        head: "ccc333",
        dirty: false,
      },
    ],
  };
}

describe("the checkout picker", () => {
  it("offers only checkouts that exist on disk", () => {
    render(<CheckoutPicker report={report()} selected="" onSelect={vi.fn()} />);

    const options = screen.getAllByTestId("checkout-option");
    expect(options).toHaveLength(3);
    // The remote entry is filtered out rather than shown-and-disabled: there
    // is nothing to render until someone checks it out.
    expect(screen.queryByText("origin/main")).not.toBeInTheDocument();
  });

  it("marks a checkout with uncommitted changes", () => {
    render(<CheckoutPicker report={report()} selected="" onSelect={vi.fn()} />);

    const dirty = screen.getAllByTestId("checkout-option").find(
      (option) => option.getAttribute("data-dirty") === "true"
    );
    expect(dirty?.textContent).toContain("fix/thing");
    expect(dirty?.textContent).toContain("uncommitted changes");
  });

  it("shows the distance from main when it is known", () => {
    render(<CheckoutPicker report={report()} selected="" onSelect={vi.fn()} />);

    const text = screen.getAllByTestId("checkout-option")
      .map((option) => option.textContent ?? "")
      .join(" | ");
    expect(text).toContain("3 ahead");
    expect(text).toContain("1 behind");
    expect(text).toContain("In step with main");
  });

  it("says NOTHING about the distance when forge could not compare", () => {
    // The regression this prevents: a missing count rendered as "in step with
    // main" tells the user their branch is current when nobody checked.
    const spike = report().checkouts?.[3];
    expect(aheadBehindKnown(spike!)).toBe(false);
    expect(distanceFromMain(spike!)).toBeNull();

    render(<CheckoutPicker report={report()} selected="" onSelect={vi.fn()} />);
    const option = screen
      .getAllByTestId("checkout-option")
      .find((candidate) => candidate.textContent?.includes("spike"));
    expect(option?.textContent).not.toContain("In step with main");
    expect(option?.textContent).not.toContain("ahead");
    expect(option?.textContent).not.toContain("behind");
  });

  it("reports the chosen checkout's path", async () => {
    const onSelect = vi.fn();
    render(<CheckoutPicker report={report()} selected="" onSelect={onSelect} />);

    const option = screen
      .getAllByTestId("checkout-option")
      .find((candidate) => candidate.textContent?.includes("fix/thing"));
    await userEvent.click(option!);

    expect(onSelect).toHaveBeenCalledWith("/src/wt/fix-thing");
  });

  it("renders nothing when there is only one checkout to choose", () => {
    const single: ForgeCheckoutsReport = {
      checkouts: [{ label: "main", kind: "worktree", path: "/src/reliant", head: "a" }],
    };
    const { container } = render(
      <CheckoutPicker report={single} selected="" onSelect={vi.fn()} />
    );
    // A picker with one option is a question with one answer.
    expect(container.querySelector("[data-testid='checkout-picker']")).toBeNull();
  });
});

describe("reading the checkout list", () => {
  it("treats the empty path as the project's own checkout", () => {
    // Always offered: it is the root the caller already named, and what the
    // daemon falls back to when nothing is chosen.
    expect(isOfferedCheckout(report(), "")).toBe(true);
  });

  it("knows which paths this project offers", () => {
    expect(isOfferedCheckout(report(), "/src/wt/fix-thing")).toBe(true);
    expect(isOfferedCheckout(report(), "/etc")).toBe(false);
  });

  it("excludes the pathless remote entry from the selectable set", () => {
    const paths = selectableCheckouts(report()).map((checkout) => checkout.path);
    expect(paths).toEqual(["/src/reliant", "/src/wt/fix-thing", "/src/wt/spike"]);
  });
});
