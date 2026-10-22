// Copyright (c) 2025 Reliant Labs

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PLATFORM_NOTE } from "../plainHealth";
import { HealthStatus } from "../workloadParts";

const base = { health: "crash_looping" as const, healthReason: "Restarting repeatedly (14 times in the last 24 hours). It ran out of memory (limit 1 GiB)" };

describe("HealthStatus", () => {
  it("shows the plain reason, and the raw machinery only behind a Details disclosure", () => {
    render(<HealthStatus withReason workload={{ ...base, healthCause: "workload", healthDetail: "last exit OOMKilled; OOMKilling: Memory cgroup out of memory" }} />);
    const cell = screen.getByTestId("health-details").closest("span[data-health]") as HTMLElement;
    expect(cell).toHaveTextContent("Restarting repeatedly (14 times in the last 24 hours)");
    const details = screen.getByTestId("health-details");
    expect(details.tagName).toBe("DETAILS");
    expect(details).not.toHaveAttribute("open");
    expect(within(details).getByText("Details")).toBeInTheDocument();
    expect(details).toHaveTextContent("last exit OOMKilled");
    // the visible line (outside the disclosure) carries no Kubernetes word
    const visible = cell.cloneNode(true) as HTMLElement;
    visible.querySelector("[data-testid=health-details]")?.remove();
    expect(visible).not.toHaveTextContent(/OOMKilled|BackOff|Unhealthy/);
  });

  it("says a platform-side fault is ours, so the user does not go looking for their own", () => {
    render(<HealthStatus withReason workload={{ health: "pending", healthReason: "Waiting for capacity. This is on us: we are adding room and it will start by itself", healthCause: "platform", healthDetail: "FailedScheduling: 0/3 nodes are available" }} />);
    expect(screen.getByTestId("health-platform-note")).toHaveTextContent(PLATFORM_NOTE);
    expect(screen.getByTestId("health-platform-note").closest("[data-cause]")).toHaveAttribute("data-cause", "platform");
  });

  it("does not blame the platform for the workload's own fault, or for nothing", () => {
    const { rerender } = render(<HealthStatus withReason workload={{ ...base, healthCause: "workload", healthDetail: "" }} />);
    expect(screen.queryByTestId("health-platform-note")).toBeNull();
    rerender(<HealthStatus withReason workload={{ health: "healthy", healthReason: "", healthCause: "platform", healthDetail: "" }} />);
    expect(screen.queryByTestId("health-platform-note")).toBeNull();
    rerender(<HealthStatus withReason workload={{ ...base, healthCause: "undetermined", healthDetail: "" }} />);
    expect(screen.queryByTestId("health-platform-note")).toBeNull();
  });

  it("shows no disclosure when there is nothing to disclose, and works for callers that pass no cause", () => {
    render(<HealthStatus withReason workload={{ health: "degraded", healthReason: "Not running as intended" }} />);
    expect(screen.queryByTestId("health-details")).toBeNull();
    expect(screen.queryByTestId("health-platform-note")).toBeNull();
    expect(screen.getByText("Not running as intended")).toBeInTheDocument();
  });

  it("keeps the compact form compact: the verdict and the note, no sentence", () => {
    render(<HealthStatus workload={{ health: "degraded", healthReason: "Its machine had a problem. This is on us", healthCause: "platform", healthDetail: "NodeNotReady" }} />);
    expect(screen.getByTestId("health-platform-note")).toBeInTheDocument();
    expect(screen.queryByTestId("health-details")).toBeNull();
    expect(screen.queryByText(/Its machine had a problem/)).toBeNull();
  });
});
