// Copyright (c) 2025 Reliant Labs
import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";

import { useNavDrawer } from "@/components/Layout/shell/shellFrame";

import { DemoCorner, useConsoleDrawerOpen } from "../DemoCorner";

function Probe() {
  return <output data-testid="drawer-state">{String(useConsoleDrawerOpen())}</output>;
}

afterEach(() => {
  act(() => useNavDrawer.getState().setOpen(false));
  document.getElementById("forge-demo-corner")?.remove();
});

it("follows the shell's nav drawer (the phone's nav), open and closed", () => {
  render(<Probe />);
  expect(screen.getByTestId("drawer-state")).toHaveTextContent("false");
  act(() => useNavDrawer.getState().setOpen(true));
  expect(screen.getByTestId("drawer-state")).toHaveTextContent("true");
  act(() => useNavDrawer.getState().setOpen(false));
  expect(screen.getByTestId("drawer-state")).toHaveTextContent("false");
});

it("steps the corner's cards aside while the drawer is open, so they never cover its rows", () => {
  render(<DemoCorner order={1}><p data-testid="corner-card">tip</p></DemoCorner>);
  expect(screen.getByTestId("corner-card")).toBeInTheDocument();
  act(() => useNavDrawer.getState().setOpen(true));
  expect(screen.queryByTestId("corner-card")).toBeNull();
  act(() => useNavDrawer.getState().setOpen(false));
  expect(screen.getByTestId("corner-card")).toBeInTheDocument();
});

it("does not mistake a modal aside with a nav (a dialog, the old console drawer's shape) for the shell's drawer", () => {
  const unrelated = document.createElement("aside");
  unrelated.setAttribute("role", "dialog");
  unrelated.setAttribute("aria-modal", "true");
  unrelated.append(document.createElement("nav"));
  document.body.append(unrelated);
  render(<Probe />);
  expect(screen.getByTestId("drawer-state")).toHaveTextContent("false");
  unrelated.remove();
});
