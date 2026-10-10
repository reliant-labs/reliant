// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";

import { renderInboxAt } from "../../inbox/__tests__/inboxTestUtils";
import { AreaShell } from "../AreaShell";

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
});
