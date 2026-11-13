// Copyright (c) 2025 Reliant Labs
import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { useConsoleDrawerOpen } from "../DemoCorner";

function Probe() {
  return <output data-testid="drawer-state">{String(useConsoleDrawerOpen())}</output>;
}
const fixtures: HTMLElement[] = [];
afterEach(() => { fixtures.splice(0).forEach(node => node.remove()); });

it("tracks a uniquely identified controlled drawer portal mounting and unmounting", async () => {
  render(<Probe />);
  const trigger = document.createElement("button");
  trigger.setAttribute("aria-controls", "drawer-unique-42");
  trigger.setAttribute("aria-expanded", "true");
  const drawer = document.createElement("aside");
  drawer.id = "drawer-unique-42";
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  drawer.append(document.createElement("nav"));
  fixtures.push(trigger, drawer);
  act(() => { document.body.append(trigger, drawer); });
  await waitFor(() => expect(screen.getByTestId("drawer-state")).toHaveTextContent("true"));
  act(() => drawer.remove());
  await waitFor(() => expect(screen.getByTestId("drawer-state")).toHaveTextContent("false"));
  act(() => document.body.append(drawer));
  await waitFor(() => expect(screen.getByTestId("drawer-state")).toHaveTextContent("true"));
  act(() => trigger.setAttribute("aria-expanded", "false"));
  await waitFor(() => expect(screen.getByTestId("drawer-state")).toHaveTextContent("false"));
});

it("does not mistake the How modal or an uncontrolled modal aside for navigation", async () => {
  const modal = document.createElement("div");
  modal.setAttribute("role", "dialog");
  modal.setAttribute("aria-modal", "true");
  const unrelated = document.createElement("aside");
  unrelated.setAttribute("role", "dialog");
  unrelated.setAttribute("aria-modal", "true");
  unrelated.append(document.createElement("nav"));
  fixtures.push(modal, unrelated);
  document.body.append(modal, unrelated);
  render(<Probe />);
  expect(screen.getByTestId("drawer-state")).toHaveTextContent("false");
});
