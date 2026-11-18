// Behaviour of calendar in a real DOM (jsdom) with real React event
// delegation: drag and keyboard rescheduling, empty-day clicks and grid
// navigation. Run by TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import Calendar, { type CalendarItem } from "./calendar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
});

const today = new Date(2026, 10, 12); // Thu 12 Nov 2026
const items: CalendarItem[] = [
  { id: "post", date: new Date(2026, 10, 12, 9, 30), title: "Launch post", channel: "bluesky" },
  { id: "hn", date: "2026-11-13", title: "Show HN", channel: "hn" },
  { id: "pinned", date: "2026-11-10", title: "Published", movable: false },
];

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

const cell = (key: string) =>
  document.querySelector<HTMLElement>(`[data-day="${key}"]`) as HTMLElement;
const chip = (id: string) =>
  document.querySelector<HTMLElement>(`[data-calendar-item="${id}"]`) as HTMLElement;

/** jsdom has no DataTransfer: a minimal one carries the dragged id. */
function dataTransfer() {
  const data = new Map<string, string>();
  return {
    setData: (type: string, value: string) => data.set(type, value),
    getData: (type: string) => data.get(type) ?? "",
    types: ["application/x-calendar-item"],
    effectAllowed: "all",
    dropEffect: "none",
  };
}

function drag(type: string, target: Element, dt: ReturnType<typeof dataTransfer>) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: dt });
  act(() => {
    target.dispatchEvent(event);
  });
}

function keydown(target: Element, key: string) {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });
}

test("items render on their local day, date-only strings included", () => {
  render(<Calendar items={items} today={today} />);
  expect(cell("2026-11-12").contains(chip("post"))).toBe(true);
  expect(cell("2026-11-13").contains(chip("hn"))).toBe(true);
  expect(cell("2026-11-12").getAttribute("aria-current")).toBe("date");
  // November 2026 starts on a Sunday: five full weeks.
  expect(document.querySelectorAll('[role="gridcell"]')).toHaveLength(35);
});

test("dragging a chip to another day calls onMove with the time of day kept", () => {
  const onMove = vi.fn();
  render(<Calendar items={items} today={today} onMove={onMove} />);
  const dt = dataTransfer();
  drag("dragstart", chip("post"), dt);
  drag("dragover", cell("2026-11-19"), dt);
  expect(cell("2026-11-19").className).toContain("ring-accent");
  drag("drop", cell("2026-11-19"), dt);
  expect(onMove).toHaveBeenCalledTimes(1);
  const [id, date] = onMove.mock.calls[0] as [string, Date];
  expect(id).toBe("post");
  expect([
    date.getFullYear(),
    date.getMonth(),
    date.getDate(),
    date.getHours(),
    date.getMinutes(),
  ]).toEqual([2026, 10, 19, 9, 30]);
});

test("a pinned item is not draggable and dropping on its own day is a no-op", () => {
  const onMove = vi.fn();
  render(<Calendar items={items} today={today} onMove={onMove} />);
  expect(chip("pinned").getAttribute("draggable")).toBe("false");
  expect(chip("post").getAttribute("draggable")).toBe("true");
  const dt = dataTransfer();
  drag("dragstart", chip("post"), dt);
  drag("drop", cell("2026-11-12"), dt);
  expect(onMove).not.toHaveBeenCalled();
});

test("a drop carrying a pinned item's id does not move it", () => {
  const onMove = vi.fn();
  render(<Calendar items={items} today={today} onMove={onMove} />);
  const dt = dataTransfer();
  dt.setData("application/x-calendar-item", "pinned");
  drag("dragover", cell("2026-11-19"), dt);
  drag("drop", cell("2026-11-19"), dt);
  expect(onMove).not.toHaveBeenCalled();
});

test("the keyboard can pick up, move and drop an item", () => {
  const onMove = vi.fn();
  render(<Calendar items={items} today={today} onMove={onMove} />);
  act(() => chip("hn").focus());
  keydown(chip("hn"), "m");
  keydown(chip("hn"), "ArrowRight");
  keydown(chip("hn"), "ArrowDown");
  expect(cell("2026-11-21").className).toContain("ring-accent");
  keydown(chip("hn"), "Enter");
  const [id, date] = onMove.mock.calls[0] as [string, Date];
  expect(id).toBe("hn");
  expect([date.getMonth(), date.getDate()]).toEqual([10, 21]);
});

test("Escape cancels a keyboard move", () => {
  const onMove = vi.fn();
  render(<Calendar items={items} today={today} onMove={onMove} />);
  act(() => chip("hn").focus());
  keydown(chip("hn"), "m");
  keydown(chip("hn"), "ArrowRight");
  keydown(chip("hn"), "Escape");
  keydown(chip("hn"), "Enter");
  expect(onMove).not.toHaveBeenCalled();
});

test("a click on a day's empty space is onDayClick; a click on an item is not", () => {
  const onDayClick = vi.fn();
  const onItemClick = vi.fn();
  render(
    <Calendar items={items} today={today} onDayClick={onDayClick} onItemClick={onItemClick} />,
  );
  act(() => cell("2026-11-20").click());
  expect(onDayClick).toHaveBeenCalledTimes(1);
  const day = onDayClick.mock.calls[0]?.[0] as Date;
  expect([day.getDate(), day.getHours()]).toEqual([20, 0]);
  act(() => chip("post").click());
  expect(onItemClick).toHaveBeenCalledWith(items[0]);
  expect(onDayClick).toHaveBeenCalledTimes(1);
});

test("arrow keys move the roving focus, and paging past the month follows it", () => {
  const onDateChange = vi.fn();
  render(<Calendar items={items} today={today} onDateChange={onDateChange} />);
  expect(cell("2026-11-12").tabIndex).toBe(0);
  act(() => cell("2026-11-12").focus());
  keydown(cell("2026-11-12"), "ArrowRight");
  expect(document.activeElement).toBe(cell("2026-11-13"));
  keydown(cell("2026-11-13"), "ArrowDown");
  keydown(cell("2026-11-20"), "ArrowDown");
  keydown(cell("2026-11-27"), "ArrowDown");
  // 4 Dec: a new month, so the view pages to December and focus follows.
  expect(onDateChange).toHaveBeenCalled();
  expect(document.activeElement?.getAttribute("data-day")).toBe("2026-12-04");
});

test("week view shows one week, and +N more opens it", () => {
  const busy: CalendarItem[] = Array.from({ length: 5 }, (_, i) => ({
    id: `p${i}`,
    date: "2026-11-12",
    title: `Post ${i}`,
  }));
  render(<Calendar items={busy} today={today} maxItemsPerDay={2} />);
  expect(cell("2026-11-12").querySelectorAll("[data-calendar-item]")).toHaveLength(2);
  const more = [...cell("2026-11-12").querySelectorAll("button")].find(
    (b) => b.textContent === "+3 more",
  );
  act(() => more?.click());
  expect(document.querySelectorAll('[role="gridcell"]')).toHaveLength(7);
  expect(cell("2026-11-12").querySelectorAll("[data-calendar-item]")).toHaveLength(5);
});
