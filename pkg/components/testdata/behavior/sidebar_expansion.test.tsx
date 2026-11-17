import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import SidebarLayout, { type SidebarNavItem } from "./sidebar_layout";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let media: EventTarget & { matches: boolean };
const user = userEvent.setup();
const button = (label: string) =>
  document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const link = (href: string) => document.querySelector<HTMLAnchorElement>(`nav a[href="${href}"]`);
const click = async (el: Element) =>
  act(async () => {
    await user.click(el);
  });
const keyboard = async (keys: string) =>
  act(async () => {
    await user.keyboard(keys);
  });
const render = (navItems: SidebarNavItem[], footerItems?: SidebarNavItem[], collapsed = false) =>
  act(() =>
    root.render(
      <SidebarLayout
        brand="Forge"
        navItems={navItems}
        footerItems={footerItems}
        collapsed={collapsed}
      >
        Page
      </SidebarLayout>,
    ),
  );
const group = (extra: Partial<SidebarNavItem> = {}): SidebarNavItem => ({
  label: "Observability",
  href: "#obs",
  defaultExpanded: true,
  children: [{ label: "Logs", href: "#logs" }],
  ...extra,
});

beforeEach(() => {
  media = Object.assign(new EventTarget(), { matches: false });
  Object.defineProperty(window, "matchMedia", { configurable: true, value: () => media });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  document.body.removeAttribute("style");
});

test.each([false, true])(
  "initial expansion, explicit collapse and keyboard reopen; mobile=%s",
  async (mobile) => {
    media.matches = mobile;
    render([group()]);
    if (mobile) await click(button("Open navigation"));
    expect(link("#logs")).not.toBeNull();
    expect(link("#obs")?.hasAttribute("aria-current")).toBe(false);
    const toggle = button("Collapse Observability");
    expect(toggle.tagName).toBe("BUTTON");
    expect(toggle.closest("a")).toBeNull();
    expect(toggle.className).toContain("h-[44px]");
    expect(toggle.className).toContain("w-[44px]");
    const controlled = toggle.getAttribute("aria-controls")!;
    expect(document.getElementById(controlled)?.hidden).toBe(false);
    await click(toggle);
    expect(link("#logs")).toBeNull();
    expect(button("Expand Observability").getAttribute("aria-expanded")).toBe("false");
    expect(document.getElementById(controlled)?.hidden).toBe(true);
    render([group()]); // fresh objects must not reset user choice
    expect(link("#logs")).toBeNull();
    button("Expand Observability").focus();
    await keyboard("{Enter}");
    expect(link("#logs")).not.toBeNull();
    await keyboard(" ");
    expect(link("#logs")).toBeNull();
    await click(button("Expand Observability"));
    // Navigation remains navigation, not disclosure; it closes the mobile drawer.
    let navigations = 0;
    link("#obs")!.addEventListener("click", (event) => {
      event.preventDefault();
      navigations++;
    });
    await click(link("#obs")!);
    expect(navigations).toBe(1);
    if (mobile) {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
      await click(button("Open navigation"));
      expect(link("#logs")).not.toBeNull();
    }
  },
);

test("initial values are snapshots, late entries initialize, and identity survives insertion/reordering", async () => {
  render([]);
  const noHref = group({ id: "local", href: undefined, label: "Local" });
  render([group(), noHref]);
  await click(button("Collapse Observability"));
  render([
    noHref,
    { label: "New", href: "#new", section: "First" },
    group({ defaultExpanded: false }),
  ]);
  expect(link("#logs")).not.toBeNull(); // Local's independent child
  const obs = link("#obs")!.closest("li")!;
  expect(obs.querySelector('a[href="#logs"]')).toBeNull();
  await click(button("Expand Observability"));
  expect(obs.querySelector('a[href="#logs"]')).not.toBeNull();
  // An untouched false default also cannot be turned into a controlled prop.
  render([group({ href: "#other", label: "Other", defaultExpanded: false })]);
  expect(button("Expand Other")).not.toBeNull();
  render([group({ href: "#other", label: "Other", defaultExpanded: true })]);
  expect(button("Expand Other")).not.toBeNull();
});

test("active descendants open opted groups; explicit collapse wins until any route change", async () => {
  const items = (route: string) => [
    group({
      defaultExpanded: false,
      children: [
        { label: "Logs", href: "#logs", active: route === "logs" },
        { label: "Alerts", href: "#alerts", active: route === "alerts" },
      ],
    }),
    { label: "Home", href: "#home", active: route === "home" },
  ];
  render(items("logs"));
  expect(link("#logs")?.getAttribute("aria-current")).toBe("page");
  await click(button("Collapse Observability"));
  render(items("logs"));
  expect(link("#logs")).toBeNull();
  render(items("alerts"));
  expect(link("#alerts")?.getAttribute("aria-current")).toBe("page");
  await click(button("Collapse Observability"));
  render(items("home"));
  expect(button("Expand Observability")).not.toBeNull();
  render(items("alerts")); // old route's collapse must not revive
  expect(link("#alerts")).not.toBeNull();
});

test("nested, section and footer identities do not collide; rail and mobile retain choices", async () => {
  const nested = group({
    children: [
      group({ href: undefined, label: "Saved", children: [{ label: "Deep", href: "#deep" }] }),
    ],
  });
  const section = group({
    section: "Other",
    children: [{ label: "Other log", href: "#section-log" }],
  });
  const footer = group({ children: [{ label: "Footer log", href: "#footer-log" }] });
  render([nested, section], [footer]);
  const controls = Array.from(document.querySelectorAll("nav [aria-controls]")).map((el) =>
    el.getAttribute("aria-controls"),
  );
  expect(new Set(controls).size).toBe(controls.length);
  const saved = Array.from(document.querySelectorAll<HTMLButtonElement>("nav button")).find(
    (el) => el.textContent === "Saved",
  )!;
  await click(saved);
  expect(link("#deep")).toBeNull();
  render([section, nested], [footer], true);
  expect(link("#footer-log")).toBeNull();
  render([section, nested], [footer]);
  expect(link("#deep")).toBeNull();
  expect(link("#section-log")).not.toBeNull();
  expect(link("#footer-log")).not.toBeNull();
  act(() => {
    media.matches = true;
    media.dispatchEvent(new Event("change"));
  });
  await click(button("Open navigation"));
  expect(link("#deep")).toBeNull();
  expect(link("#footer-log")).not.toBeNull();
});

test("without the option legacy links, no-href toggles, nested lists and active route behavior are unchanged", async () => {
  const items = (active: boolean): SidebarNavItem[] => [
    group({
      defaultExpanded: undefined,
      active,
      children: [
        { label: "Nested", href: "#nested", children: [{ label: "Deep", href: "#deep", active }] },
      ],
    }),
    group({ label: "Local", href: undefined, defaultExpanded: undefined }),
  ];
  render(items(false));
  expect(link("#obs")?.getAttribute("aria-expanded")).toBe("false");
  expect(button("Expand Observability")).toBeNull();
  const local = Array.from(document.querySelectorAll<HTMLButtonElement>("nav button")).find(
    (el) => el.textContent === "Local",
  )!;
  await click(local);
  expect(link("#logs")).not.toBeNull();
  render(items(true));
  expect(link("#deep")).not.toBeNull();
  render(items(false));
  expect(link("#deep")).toBeNull();
  expect(link("#logs")).not.toBeNull(); // legacy local toggle persists on route change
});

test("controlled open wins over the route and the reader; onOpenChange reports each toggle", async () => {
  const changes: boolean[] = [];
  const items = (open: boolean, route = "home"): SidebarNavItem[] => [
    group({
      defaultExpanded: undefined,
      open,
      onOpenChange: (next) => changes.push(next),
      children: [{ label: "Logs", href: "#logs", active: route === "logs" }],
    }),
    { label: "Home", href: "#home", active: route === "home" },
  ];
  render(items(true));
  expect(link("#logs")).not.toBeNull();
  // The chevron asks; the caller decides. Until it does, nothing changes.
  await click(button("Collapse Observability"));
  expect(changes).toEqual([false]);
  expect(link("#logs")).not.toBeNull();
  render(items(false));
  expect(link("#logs")).toBeNull();
  // A route change does not reopen it, not even onto one of its own sub-pages.
  render(items(false, "logs"));
  expect(link("#logs")).toBeNull();
  render(items(true, "logs"));
  expect(link("#logs")?.getAttribute("aria-current")).toBe("page");
});

test("a collapsed group marks its parent for a hidden current page, and following its row opens it", async () => {
  const changes: boolean[] = [];
  const items = (extra: Partial<SidebarNavItem>): SidebarNavItem[] => [
    group({
      onOpenChange: (next) => changes.push(next),
      children: [{ label: "Logs", href: "#logs", active: true }],
      ...extra,
    }),
  ];
  const marked = () => link("#obs")!.className.split(" ").includes("bg-ink/8");
  render(items({}));
  expect(marked()).toBe(false); // the open sub-page carries the mark
  await click(button("Collapse Observability"));
  expect(changes).toEqual([false]);
  expect(marked()).toBe(true);
  link("#obs")!.addEventListener("click", (event) => event.preventDefault());
  await click(link("#obs")!);
  expect(changes).toEqual([false, true]);
  expect(link("#logs")).not.toBeNull();
  // Controlled: the row asks to open, and an open group's row only navigates.
  changes.length = 0;
  render(items({ defaultExpanded: undefined, open: false }));
  expect(marked()).toBe(true);
  link("#obs")!.addEventListener("click", (event) => event.preventDefault());
  await click(link("#obs")!);
  expect(changes).toEqual([true]);
  render(items({ defaultExpanded: undefined, open: true }));
  await click(link("#obs")!);
  expect(changes).toEqual([true]);
});
