/**
 * MobileDaemonList — pins the mobile machine management entry point: users can
 * create a cloud machine from both empty and populated states, and the request
 * goes through the shared useCreateDaemon hook/cache path.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { VirtuosoMockContext } from "react-virtuoso";
import { DaemonSize, DaemonStatus, DaemonType } from "@/gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "@/services/controlPlane/daemon";

const mocks = vi.hoisted(() => ({
  daemons: [] as Daemon[],
  createDaemon: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, ...props }: { children?: React.ReactNode; to?: string; params?: Record<string, string> }) => {
    const href = to?.replace("$daemonId", params?.daemonId ?? "") ?? "#";
    return <a href={href} {...props}>{children}</a>;
  },
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  isEntitlementDenial: () => false,
  useDaemonList: () => ({ data: mocks.daemons, isLoading: false }),
  useCreateDaemon: () => ({ mutateAsync: mocks.createDaemon, isPending: false }),
}));

vi.mock("../../../store/mobileDrawerStore", () => ({
  useMobileDrawerStore: (selector: (state: { open: () => void }) => unknown) =>
    selector({ open: vi.fn() }),
}));

const { MobileDaemonList } = await import("../MobileDaemonList");

function daemon(overrides: Partial<Daemon> = {}): Daemon {
  return {
    id: "d1",
    name: "work-box",
    status: DaemonStatus.ACTIVE,
    size: DaemonSize.SMALL,
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
  mocks.createDaemon.mockReset().mockResolvedValue("d-new");
});

describe("MobileDaemonList", () => {
  it("offers a new machine action in the empty state", () => {
    renderList();
    expect(screen.getByText("No machines yet")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /new machine/i })).toHaveLength(2);
  });

  it("creates a small managed machine through the shared hook", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderList();

    const [headerAction] = screen.getAllByRole("button", { name: /new machine/i });
    await user.click(headerAction!);
    await user.clear(screen.getByLabelText(/^name$/i));
    await user.type(screen.getByLabelText(/^name$/i), "phone-box");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() =>
      expect(mocks.createDaemon).toHaveBeenCalledWith({
        name: "phone-box",
        daemonType: DaemonType.MANAGED,
        size: DaemonSize.SMALL,
        idleTimeout: "30m",
        gitRepo: undefined,
        gitBranch: "main",
      }),
    );
  });

  it("keeps the new machine action available when machines already exist", async () => {
    mocks.daemons = [daemon({ id: "d1", name: "work-box" })];
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderList();

    expect(await screen.findByText("work-box")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /new machine/i }));
    expect(screen.getByRole("dialog", { name: /new machine/i })).toBeInTheDocument();
  });
});
