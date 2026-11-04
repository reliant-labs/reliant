/**
 * MobileDaemonList — pins the mobile machine management entry point: Add
 * machine is offered from both the empty and the populated list, and it opens
 * the one shared Add machine sheet (no mobile-only create form with its own
 * defaults — its fixed "mobile-machine" name refreshed the first machine on
 * every second create).
 */

import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { VirtuosoMockContext } from "react-virtuoso";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo as Daemon } from "@/gen/reliant/v1/daemon_registry_pb";

const mocks = vi.hoisted(() => ({
  daemons: [] as Daemon[],
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, ...props }: { children?: React.ReactNode; to?: string; params?: Record<string, string> }) => {
    const href = to?.replace("$daemonId", params?.daemonId ?? "") ?? "#";
    return <a href={href} {...props}>{children}</a>;
  },
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: mocks.daemons, isLoading: false }),
}));

vi.mock("../../../store/mobileDrawerStore", () => ({
  useMobileDrawerStore: (selector: (state: { open: () => void; inboxBadge: unknown }) => unknown) =>
    selector({ open: vi.fn(), inboxBadge: { count: 0, informational: false, degraded: false, announcement: "" } }),
}));

vi.mock("@/components/Billing/MachineCost", () => ({ MachineCost: () => null }));

vi.mock("@/components/Machines/AddMachineSheet", () => ({
  AddMachineSheet: ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="Add a machine" /> : null),
}));

const { MobileDaemonList } = await import("../MobileDaemonList");

function daemon(overrides: Partial<Daemon> = {}): Daemon {
  return {
    daemonId: "d1",
    hostname: "work-box",
    status: DaemonStatus.ACTIVE,
    size: "m",
    ...overrides,
  } as Daemon;
}

function renderList() {
  return render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 800, itemHeight: 80 }}>
      <MobileDaemonList />
    </VirtuosoMockContext.Provider>,
  );
}

beforeEach(() => {
  mocks.daemons = [];
});

describe("MobileDaemonList", () => {
  it("offers Add machine in the empty state, and it opens the shared sheet", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderList();
    expect(screen.getByText("No machines yet")).toBeInTheDocument();
    const actions = screen.getAllByRole("button", { name: /add machine/i });
    expect(actions).toHaveLength(2);
    await user.click(actions[0]!);
    expect(screen.getByRole("dialog", { name: "Add a machine" })).toBeInTheDocument();
  });

  it("keeps Add machine available when machines already exist", async () => {
    mocks.daemons = [daemon({ daemonId: "d1", hostname: "work-box" })];
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderList();

    expect(await screen.findByText("work-box")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add machine/i }));
    expect(screen.getByRole("dialog", { name: "Add a machine" })).toBeInTheDocument();
  });
});
