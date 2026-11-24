// Copyright (c) 2026 Reliant Labs

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ChatViewTabs } from "../ChatViewTabs";

describe("ChatViewTabs", () => {
  it("marks the open view and switches on click", () => {
    const onChange = vi.fn();
    render(<ChatViewTabs view="chat" onChange={onChange} />);
    expect(screen.getByRole("tablist", { name: "Chat view" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Chat" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Trace" })).toHaveAttribute("aria-selected", "false");
    fireEvent.click(screen.getByRole("tab", { name: "Trace" }));
    expect(onChange).toHaveBeenCalledWith("trace");
  });

  it("keeps each tab's name when its label folds to an icon", () => {
    render(<ChatViewTabs view="trace" onChange={() => {}} />);
    expect(screen.getByRole("tab", { name: "Trace" })).toHaveAttribute("aria-selected", "true");
  });
});
