// Copyright (c) 2025 Reliant Labs

/**
 * A daemon-backed tab on a server whose operator turned the forge API off says
 * so. It must not blame the user's daemon — no daemon was asked — nor offer a
 * retry, which cannot change the server's answer.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { FORGE_API_DISABLED_COPY } from "@/lib/forgeApiDisabled";

import { DAEMON_OFFLINE_COPY, DaemonNeeded } from "../DaemonNeeded";

describe("DaemonNeeded", () => {
  it("blames the daemon, and offers a retry, when it is offline", () => {
    render(<DaemonNeeded what="what forge env up is running for dev on your machine" onRetry={vi.fn()} />);
    expect(screen.getByTestId("daemon-needed")).toHaveTextContent(DAEMON_OFFLINE_COPY);
    expect(screen.getByTestId("daemon-retry")).toBeInTheDocument();
  });

  it("says the forge API is off, with no retry, when the server disabled it", () => {
    render(
      <DaemonNeeded
        what="what forge env up is running for dev on your machine"
        detail="the forge API is disabled in this deployment"
        onRetry={vi.fn()}
        forgeDisabled
      />
    );
    const panel = screen.getByTestId("forge-api-disabled");
    expect(panel).toHaveTextContent(FORGE_API_DISABLED_COPY);
    expect(panel).toHaveTextContent("this tab can't show what forge env up is running for dev on your machine.");
    expect(panel).not.toHaveTextContent(DAEMON_OFFLINE_COPY);
    expect(screen.queryByTestId("daemon-needed")).toBeNull();
    expect(screen.queryByTestId("daemon-retry")).toBeNull();
  });
});
