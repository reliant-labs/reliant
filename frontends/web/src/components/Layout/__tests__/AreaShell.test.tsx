// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";

import { renderInboxAt } from "../../inbox/__tests__/inboxTestUtils";
import { AreaShell } from "../AreaShell";
import { ShellFrameContext } from "../shell/shellFrame";

function Crashing(): never {
  throw new Error("inbox render failed");
}

describe("AreaShell", () => {
  it("keeps the title bar's way out when the area's content crashes", async () => {
    // React and the boundary both log the caught error; keep the output clean.
    vi.spyOn(console, "error").mockImplementation(() => {});
    renderInboxAt(
      <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
        <Crashing />
      </AreaShell>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("inbox render failed");
    expect(screen.getByRole("button", { name: "Back to app" })).toBeInTheDocument();
  });

  // Inside the one shell the nav is the way around (U6): no exit, no org
  // switcher of its own, no window chrome — a slim bar over the content.
  it("drops its own way out and org switcher inside the one shell", async () => {
    renderInboxAt(
      <ShellFrameContext.Provider value={{ inShell: true, navDocked: true }}>
        <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
          <p>items</p>
        </AreaShell>
      </ShellFrameContext.Provider>,
    );

    expect(await screen.findByText("items")).toBeInTheDocument();
    expect(screen.getByTestId("area-shell")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Back to app" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Switch organization/ })).toBeNull();
    // Docked, the nav's own toggle is in the nav, not here.
    expect(screen.queryByTestId("shell-nav-toggle")).toBeNull();
  });

  it("offers the nav's toggle when the nav is not docked", async () => {
    renderInboxAt(
      <ShellFrameContext.Provider value={{ inShell: true, navDocked: false }}>
        <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
          <p>items</p>
        </AreaShell>
      </ShellFrameContext.Provider>,
    );
    expect(await screen.findByTestId("shell-nav-toggle")).toHaveAccessibleName("Show sidebar");
  });
});
