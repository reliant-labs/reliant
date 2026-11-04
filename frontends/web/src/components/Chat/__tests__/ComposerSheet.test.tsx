import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, it } from "vitest";
import { MobileOptionSheet } from "../../Mobile/MobileSettingsRow";

it("portals options, labels selection, closes on Escape and restores focus", async () => {
  function Example() {
    const [open, setOpen] = useState(false);
    return <div style={{ overflow: "hidden" }}><button onClick={() => setOpen(true)}>Choose</button>{open && <MobileOptionSheet title="Workflow" value="a" options={[{ value: "a", label: "Agent" }]} onSelect={() => setOpen(false)} onClose={() => setOpen(false)} />}</div>;
  }
  const { container } = render(<Example />);
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  const dialog = screen.getByRole("dialog", { name: "Workflow" });
  expect(container.contains(dialog)).toBe(false);
  expect(dialog.contains(document.activeElement)).toBe(true);
  fireEvent.keyDown(document.activeElement!, { key: "Escape" });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByRole("button", { name: "Choose" })).toHaveFocus();
});

it("restores focus to the opener when the click never focused it (Safari)", () => {
  function Example() {
    const [open, setOpen] = useState(false);
    return <div><button onClick={() => setOpen(true)}>Choose</button>{open && <MobileOptionSheet title="Workflow" value="a" options={[{ value: "a", label: "Agent" }]} onSelect={() => setOpen(false)} onClose={() => setOpen(false)} />}</div>;
  }
  render(<Example />);
  const opener = screen.getByRole("button", { name: "Choose" });
  // fireEvent.click, unlike userEvent.click, does not focus the button: WebKit's behaviour.
  fireEvent.click(opener);
  expect(opener).not.toHaveFocus();
  expect(screen.getByRole("dialog", { name: "Workflow" }).contains(document.activeElement)).toBe(true);
  fireEvent.keyDown(document.activeElement!, { key: "Escape" });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(opener).toHaveFocus();
});
