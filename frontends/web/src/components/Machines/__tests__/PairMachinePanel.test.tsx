/**
 * Connecting your own computer is PAIRING: `reliant daemon start` prints a
 * code, the person types it here, and this panel asks "Approve machine
 * <hostname>?". These pin that flow (no token is ever minted or shown), the
 * layout the earlier panel's defects shaped (install is a numbered step, it
 * folds once Reliant is demonstrably installed, the terminal step says how to
 * open a terminal), and what counts as "connected" in each mode.
 */
import { screen, waitFor } from "@testing-library/react";
import { renderWithQuery as render } from "@/test/renderWithQuery";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

const mockUseDaemonStatus = vi.fn();
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));
vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({ emit: vi.fn() }),
}));

// Pairing goes through the control plane; a build without one gets the
// token fallback instead (pinned at the bottom).
const cp = vi.hoisted(() => ({ url: "https://cp.example.test" as string | undefined }));
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  getControlPlaneURL: () => cp.url,
}));

const mockLookup = vi.fn();
const mockDecide = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: async () => ({
    orgs: [{ id: "org-a", name: "Acme", slug: "acme", role: "owner", isOwner: true, isDefault: true }],
    defaultOrgId: "org-a",
  }),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.example.test", hasControlPlane: true }));
vi.mock("@/services/controlPlane/pairing", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/controlPlane/pairing")>();
  return {
    ...actual,
    lookupPendingMachine: (code: string) => mockLookup(code),
    decidePendingMachine: (code: string, approved: boolean, orgId?: string | null) => mockDecide(code, approved, orgId),
  };
});

import { describeTerminal } from "@/components/ReliantDownloadOptions";
import { PairMachinePanel } from "../PairMachinePanel";

function setPlatform(platform: string) {
  Object.defineProperty(navigator, "platform", { value: platform, configurable: true });
}

function daemonsState(daemons: Partial<DaemonInfo>[]) {
  const list = daemons as DaemonInfo[];
  return {
    daemons: list,
    activeDaemon: list.find((d) => d.status === DaemonStatus.ACTIVE),
    loading: false,
  };
}

beforeEach(() => {
  cp.url = "https://cp.example.test";
  mockUseDaemonStatus.mockReturnValue(daemonsState([]));
  setPlatform("Win32");
  (window as { electronAPI?: unknown }).electronAPI = undefined;
  mockLookup.mockResolvedValue({
    userCode: "BDFG-HJKL",
    hostname: "build-box.local",
    platform: "linux/amd64",
    clientId: "reliant-cli",
    scopes: ["daemon:connect"],
    expiresAt: "",
  });
  mockDecide.mockResolvedValue(undefined);
});

afterEach(() => vi.clearAllMocks());

describe("pairing by code", () => {
  it("shows the start command with nothing to paste into it", () => {
    render(<PairMachinePanel />);
    expect(screen.getByText(/reliant daemon start/)).toBeInTheDocument();
    expect(screen.queryByText(/--token/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Generate token/i })).not.toBeInTheDocument();
  });

  it("looks the typed code up and asks to approve that machine by name", async () => {
    const user = userEvent.setup();
    render(<PairMachinePanel />);

    await user.type(screen.getByLabelText("Pairing code"), "bdfghjkl");
    expect(screen.getByLabelText("Pairing code")).toHaveValue("BDFG-HJKL");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText("Approve machine build-box.local?")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve" }));

    // The machine is approved into the org the picker shows (one org: the default).
    await waitFor(() => expect(mockDecide).toHaveBeenCalledWith("BDFG-HJKL", true, "org-a"));
    expect(await screen.findByText(/Waiting for build-box.local to connect/)).toBeInTheDocument();
  });

  it("declines a machine the person does not recognise", async () => {
    const user = userEvent.setup();
    render(<PairMachinePanel />);
    await user.type(screen.getByLabelText("Pairing code"), "BDFG-HJKL");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Not my machine" }));
    expect(mockDecide).toHaveBeenCalledWith("BDFG-HJKL", false, null);
    expect(await screen.findByText(/Declined/)).toBeInTheDocument();
  });

  it("says so when no machine is waiting with the code", async () => {
    const { PairingError } = await import("@/services/controlPlane/pairing");
    mockLookup.mockRejectedValue(new PairingError("nope", true));
    const user = userEvent.setup();
    render(<PairMachinePanel />);
    await user.type(screen.getByLabelText("Pairing code"), "BDFG-HJKL");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/No machine is waiting with that code/)).toBeInTheDocument();
  });

  it("starts from a code the CLI's link carried", () => {
    render(<PairMachinePanel initialCode="bdfg-hjkl" />);
    expect(screen.getByLabelText("Pairing code")).toHaveValue("BDFG-HJKL");
  });
});

describe("what counts as connected", () => {
  it("in add mode, only a machine that was not there when the panel opened", async () => {
    const onConnected = vi.fn();
    const existing = { daemonId: "d-1", name: "laptop", status: DaemonStatus.ACTIVE };
    mockUseDaemonStatus.mockReturnValue(daemonsState([existing]));
    const { rerender } = render(<PairMachinePanel mode="add" onConnected={onConnected} />);
    expect(onConnected).not.toHaveBeenCalled();

    mockUseDaemonStatus.mockReturnValue(
      daemonsState([existing, { daemonId: "d-2", name: "build-box", status: DaemonStatus.ACTIVE }]),
    );
    rerender(<PairMachinePanel mode="add" onConnected={onConnected} />);
    await waitFor(() => expect(onConnected).toHaveBeenCalledWith("d-2"));
    expect(screen.getByText("build-box is connected")).toBeInTheDocument();
  });

  it("in bootstrap mode, any connected machine", async () => {
    const onConnected = vi.fn();
    mockUseDaemonStatus.mockReturnValue(
      daemonsState([{ daemonId: "d-1", name: "laptop", status: DaemonStatus.ACTIVE }]),
    );
    render(<PairMachinePanel mode="bootstrap" onConnected={onConnected} />);
    await waitFor(() => expect(onConnected).toHaveBeenCalledWith("d-1"));
  });

  it("in reference mode, never reports up", () => {
    const onConnected = vi.fn();
    mockUseDaemonStatus.mockReturnValue(
      daemonsState([{ daemonId: "d-1", name: "laptop", status: DaemonStatus.ACTIVE }]),
    );
    render(<PairMachinePanel mode="reference" onConnected={onConnected} />);
    expect(onConnected).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Pairing code")).toBeInTheDocument();
  });
});

describe("layout", () => {
  it("numbers install, run, approve — nothing the user must do is unnumbered", () => {
    const { container } = render(<PairMachinePanel />);
    const sections = container.querySelectorAll("section");
    expect(sections).toHaveLength(2);
    expect(sections[0]).toHaveTextContent(/Install Reliant on that machine/i);
    expect(sections[1]).toHaveTextContent(/Run this in/i);
    expect(sections[1]).toHaveTextContent(/Approve it here/i);
    for (const n of ["1", "2", "3"]) expect(screen.getByText(n)).toBeInTheDocument();
  });

  it("folds the download block once the account has a machine, but keeps it reachable", async () => {
    mockUseDaemonStatus.mockReturnValue(daemonsState([{ daemonId: "d1" }]));
    const user = userEvent.setup();
    render(<PairMachinePanel />);
    expect(screen.queryByText(/Download for/i)).not.toBeInTheDocument();
    expect(screen.getByText(/setting up another one/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Show download instructions/i }));
    expect(screen.getByText(/Download for/i)).toBeInTheDocument();
  });

  it("names PowerShell on Windows and gives no keystroke on an unknown platform", () => {
    render(<PairMachinePanel />);
    expect(screen.getByText(/Run this in PowerShell on that machine/i)).toBeInTheDocument();
    const generic = describeTerminal("unknown");
    expect(generic.name).toBe("a terminal");
    expect(generic.howToOpen).not.toMatch(/⌘|Win|Ctrl/);
  });
});

describe("a deployment with no control plane", () => {
  it("points to an access token instead of a pairing code", () => {
    cp.url = undefined;
    render(<PairMachinePanel />);
    expect(screen.getByTestId("pair-token-fallback")).toHaveTextContent(/reliant daemon start --token/);
    expect(screen.queryByLabelText("Pairing code")).not.toBeInTheDocument();
  });
});
