// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { LiveAvailability } from "@/services/forge/live";

import { DeployStatusUnread, deployStatusUnread } from "../DeployStatusUnread";

const live = (availability: LiveAvailability | undefined) => ({
  data: availability ? { availability } : undefined,
  refetch: vi.fn(),
  isFetching: false,
});

describe("deployStatusUnread", () => {
  it("is nothing while loading, for an answer, and for a build with no control plane", () => {
    expect(deployStatusUnread(live(undefined))).toBeUndefined();
    expect(deployStatusUnread(live("available"))).toBeUndefined();
    expect(deployStatusUnread(live("no-control-plane"))).toBeUndefined();
  });

  it("offers Retry only when retrying can help", () => {
    expect(deployStatusUnread(live("unreachable"))?.onRetry).toBeTypeOf("function");
    expect(deployStatusUnread(live("no-access"))).toEqual({});
    expect(deployStatusUnread(live("not-configured"))).toEqual({});
  });
});

describe("DeployStatusUnread", () => {
  it("says the status could not be loaded, never that nothing is deployed, and retries", async () => {
    const source = live("unreachable");
    render(<DeployStatusUnread state={deployStatusUnread(source)!} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load deployment status");
    expect(alert).not.toHaveTextContent(/nothing deployed yet/i);
    await userEvent.setup().click(screen.getByTestId("deploy-status-unread-retry"));
    expect(source.refetch).toHaveBeenCalledTimes(1);
  });
});
