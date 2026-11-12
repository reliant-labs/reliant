import { create } from "@bufbuild/protobuf";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { DaemonStatusDot } from "../DaemonStatusDot";

const state = vi.hoisted(() => ({
  daemons: [] as DaemonInfo[],
  activeDaemon: undefined as DaemonInfo | undefined,
  loading: false,
  lastSeen: null as number | null,
  navigate: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => state.navigate }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => state }));
vi.mock("@/store/globalUpdatesStore", () => ({
  useGlobalUpdatesStore: (selector: (value: { daemonLastSeen: number | null }) => unknown) =>
    selector({ daemonLastSeen: state.lastSeen }),
}));

beforeEach(() => {
  state.daemons = [];
  state.activeDaemon = undefined;
  state.loading = false;
  state.lastSeen = null;
  state.navigate.mockClear();
});
afterEach(() => vi.restoreAllMocks());

describe("DaemonStatusDot", () => {
  it.each(["managed", "self_hosted"])("uses the machine picker icon and opens the active %s machine, not the first listed machine", (daemonType) => {
    const offline = create(DaemonInfoSchema, { daemonId: "offline", status: DaemonStatus.DISCONNECTED });
    const active = create(DaemonInfoSchema, { daemonId: "active", name: "My machine", daemonType, status: DaemonStatus.ACTIVE });
    state.daemons = [offline, active];
    state.activeDaemon = active;
    render(<DaemonStatusDot />);

    const button = screen.getByRole("button", { name: "Machine connected: My machine. Open machine settings" });
    expect(button.querySelector("svg")).toHaveClass("lucide-monitor");
    expect(button.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    fireEvent.click(button);
    expect(state.navigate).toHaveBeenCalledWith({
      to: "/settings/machines/$machineId", params: { machineId: "active" },
    });
  });

  it.each([
    { label: "No machine connected", loading: false, recent: false, hasOffline: false },
    { label: "No machine connected", loading: false, recent: false, hasOffline: true },
    { label: "Checking your machine...", loading: true, recent: false, hasOffline: false },
    { label: "Last seen 10s ago — reconnecting...", loading: false, recent: true, hasOffline: false },
  ])("keeps the machines list available: $label (offline machine: $hasOffline)", ({ label, loading, recent, hasOffline }) => {
    vi.spyOn(Date, "now").mockReturnValue(1_000_000);
    state.loading = loading;
    state.lastSeen = recent ? 990 : null;
    if (hasOffline) state.daemons = [create(DaemonInfoSchema, { daemonId: "offline", status: DaemonStatus.DISCONNECTED })];
    render(<DaemonStatusDot />);

    fireEvent.click(screen.getByRole("button", { name: `${label}. Open machine settings` }));
    expect(state.navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "machines" } });
  });

  it("opens machine settings from the keyboard", async () => {
    const user = userEvent.setup();
    render(<DaemonStatusDot />);
    const button = screen.getByRole("button", { name: /open machine settings/i });
    expect(button).toHaveAttribute("type", "button");
    await user.tab();
    expect(button).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(state.navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "machines" } });
  });
});
