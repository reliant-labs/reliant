import { afterEach, describe, expect, it } from "vitest";
import { dialogOpener } from "../dialogOpener";

// A bare dispatched click focuses nothing — exactly what Safari does when a
// button is clicked or tapped.
function safariClick(el: Element) {
  el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

const nextTask = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("dialogOpener", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("is the control clicked in this task, even though the click did not focus it", () => {
    document.body.innerHTML = '<textarea></textarea><button type="button"><svg></svg></button>';
    const field = document.querySelector("textarea")!;
    const button = document.querySelector("button")!;
    field.focus();

    // The click lands on the icon inside the button.
    let opener: HTMLElement | null = null;
    button.addEventListener("click", () => {
      opener = dialogOpener();
    });
    safariClick(button.querySelector("svg")!);

    expect(document.activeElement).toBe(field);
    expect(opener).toBe(button);
  });

  it("falls back to the focused element once the click's task is over", async () => {
    document.body.innerHTML = '<input /><button type="button">open</button>';
    const input = document.querySelector("input")!;
    safariClick(document.querySelector("button")!);
    await nextTask();

    input.focus();
    expect(dialogOpener()).toBe(input);
  });

  it("is nothing when only <body> has focus", async () => {
    document.body.innerHTML = '<button type="button">open</button>';
    await nextTask();
    expect(dialogOpener()).toBeNull();
  });

  it("skips a clicked control that unmounted with the click", () => {
    document.body.innerHTML = '<input /><div role="menu"><button type="button">Rename</button></div>';
    const input = document.querySelector("input")!;
    const item = document.querySelector("button")!;
    input.focus();
    safariClick(item);
    item.closest('[role="menu"]')!.remove();

    expect(dialogOpener()).toBe(input);
  });
});
