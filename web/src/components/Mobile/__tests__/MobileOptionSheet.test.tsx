import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { MobileOptionSheet } from "../MobileSettingsRow";

it("portals options outside the scroll layer and restores focus on close", () => {
  const onSelect = vi.fn();
  const onClose = vi.fn();
  const trigger = document.createElement("button");
  document.body.append(trigger);
  trigger.focus();
  const { container, unmount } = render(<div className="isolate overflow-y-auto"><MobileOptionSheet title="Choose a workflow" options={[{ value: "agent", label: "Agent" }]} value="agent" onSelect={onSelect} onClose={onClose} /></div>);
  const dialog = screen.getByRole("dialog", { name: "Choose a workflow" });
  expect(container.contains(dialog)).toBe(false);
  expect(dialog.parentElement).toHaveClass("z-[10000]");
  fireEvent.click(screen.getByRole("button", { name: "Agent" }));
  expect(onSelect).toHaveBeenCalledWith("agent");
  fireEvent.keyDown(dialog, { key: "Escape" });
  expect(onClose).toHaveBeenCalledOnce();
  unmount();
  expect(document.activeElement).toBe(trigger);
  trigger.remove();
});
