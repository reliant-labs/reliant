/**
 * The self-hosted connect panel is ONE screen carrying two unlike jobs: get
 * Reliant onto a machine, then configure it from a terminal. Three reported
 * defects, all of them about the seam between those jobs:
 *
 *  1. "The download instructions blend too much into the following
 *     post-download instructions." The download block had no heading, so it
 *     read as body copy under the note above it, and a 1px `border-t` was the
 *     only thing separating it from the terminal steps.
 *  2. A user who has already downloaded Reliant — often reading this from
 *     inside the desktop app — was shown the full download block first, above
 *     the step they actually needed.
 *  3. "2. Start the daemon" over a code block assumes the reader knows they
 *     need a terminal and how to open one. That is where a non-developer
 *     stops, and it was the only step with no instruction attached.
 *
 * These tests pin the OUTCOMES (a numbered sequence that includes installing,
 * a collapsed-but-reachable download block, OS-correct terminal wording), not
 * the classnames that currently produce them.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

const mockUseDaemonStatus = vi.fn();

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));

vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({ emit: vi.fn() }),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    token: () => ({
      createToken: vi.fn().mockResolvedValue({ token: "rlat_test" }),
    }),
  },
}));

// `describeTerminal` lives beside `useDetectedOS` rather than in either
// caller: the self-hosted panel and the OAuth helper both print a command and
// both need to name the terminal, so one platform module owns the answer.
import { describeTerminal } from "@/components/ReliantDownloadOptions";
import { SelfHostedDaemonConnect } from "../SelfHostedDaemonConnect";

/** `useDetectedOS` reads `navigator.platform`, so drive the real hook. */
function setPlatform(platform: string) {
  Object.defineProperty(navigator, "platform", {
    value: platform,
    configurable: true,
  });
}

function setMacArch(architecture: "arm" | "x86") {
  Object.defineProperty(navigator, "userAgentData", {
    value: {
      getHighEntropyValues: () => Promise.resolve({ architecture }),
    },
    configurable: true,
  });
}

/** No daemons, not loading — the brand-new-user case. */
function noDaemons() {
  return { activeDaemon: null, daemons: [] as DaemonInfo[], loading: false };
}

beforeEach(() => {
  mockUseDaemonStatus.mockReturnValue(noDaemons());
  setPlatform("Win32");
  // Not Electron by default: the plain-browser path is the one that shows
  // every instruction, so it is the strictest case for the layout tests.
  (window as { electronAPI?: unknown }).electronAPI = undefined;
});

afterEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(navigator, "userAgentData", {
    value: undefined,
    configurable: true,
  });
});

describe("installing is part of the numbered sequence", () => {
  it("numbers the install step, so nothing the user must do is unnumbered", () => {
    const { container } = render(<SelfHostedDaemonConnect />);

    // The defect: the sequence used to start at "1. Generate an access
    // token", with downloading numbered as nothing at all.
    //
    // Scoped to the step headings rather than `getByText`, because the
    // panel's intro prose also says "Generate an access token below" — a bare
    // text query matches both and cannot tell a heading from a sentence.
    const sections = container.querySelectorAll("section");
    expect(sections[0]).toHaveTextContent(/Install Reliant on that machine/i);
    expect(sections[1]).toHaveTextContent(/Generate an access token/i);
    expect(sections[1]).toHaveTextContent(/Start the daemon/i);

    expect(screen.getByText("1")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("puts the download block and the terminal steps in different containers", () => {
    const { container } = render(<SelfHostedDaemonConnect />);

    // The separation is structural, not a hairline between siblings: two
    // sections, so a tint/elevation change can carry the boundary.
    const sections = container.querySelectorAll("section");
    expect(sections).toHaveLength(2);

    // And each numbered step lives in the section that owns it.
    expect(sections[0]).toHaveTextContent(/Install Reliant on that machine/i);
    expect(sections[1]).toHaveTextContent(/Generate an access token/i);
    expect(sections[1]).toHaveTextContent(/Start the daemon/i);
  });
});

describe("download instructions fold once Reliant is demonstrably installed", () => {
  it("shows them expanded for a user with no daemon and no CLI", () => {
    render(<SelfHostedDaemonConnect />);

    expect(screen.getByText(/Download for/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Show download instructions/i }),
    ).not.toBeInTheDocument();
  });

  it("collapses them when the account already has a daemon", () => {
    mockUseDaemonStatus.mockReturnValue({
      activeDaemon: null,
      // A registered-but-not-active daemon: enough evidence that Reliant has
      // been downloaded at least once, not enough to skip the panel.
      daemons: [{ id: "d1" }] as unknown as DaemonInfo[],
      loading: false,
    });

    render(<SelfHostedDaemonConnect />);

    expect(screen.queryByText(/Download for/i)).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Show download instructions/i }),
    ).toBeInTheDocument();
    // The note names the EVIDENCE, so a user setting up a second machine can
    // see why it folded and that reopening it is expected.
    expect(screen.getByText(/setting up another one/i)).toBeInTheDocument();
  });

  it("keeps them reachable — the daemon may be on a different machine", async () => {
    const user = userEvent.setup();
    mockUseDaemonStatus.mockReturnValue({
      activeDaemon: null,
      daemons: [{ id: "d1" }] as unknown as DaemonInfo[],
      loading: false,
    });

    render(<SelfHostedDaemonConnect />);
    await user.click(
      screen.getByRole("button", { name: /Show download instructions/i }),
    );

    // This is the whole reason it folds rather than disappearing.
    expect(screen.getByText(/Download for/i)).toBeInTheDocument();
  });
});

describe("the daemon step says how to open a terminal", () => {
  it("names PowerShell and the Windows key on Windows", () => {
    setPlatform("Win32");
    render(<SelfHostedDaemonConnect />);

    expect(
      screen.getByText(/Start the daemon in PowerShell/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Open PowerShell/i)).toBeInTheDocument();
    // The failure this prevents: Mac instructions shown to a Windows user.
    expect(screen.queryByText(/⌘ \+ Space/)).not.toBeInTheDocument();
  });

  it("names Terminal and the Spotlight shortcut on macOS", async () => {
    setPlatform("MacIntel");
    setMacArch("arm");
    render(<SelfHostedDaemonConnect />);

    await waitFor(() => {
      expect(
        screen.getByText(/Start the daemon in Terminal/i),
      ).toBeInTheDocument();
    });
    expect(screen.getByText(/⌘ \+ Space/)).toBeInTheDocument();
    expect(screen.queryByText(/PowerShell/i)).not.toBeInTheDocument();
  });

  it("gives no app name or keystroke on an unknown platform", () => {
    // Guessing wrong is worse than staying generic: a keystroke for software
    // the machine may not have is a dead end dressed as an instruction.
    const generic = describeTerminal("unknown");
    expect(generic.name).toBe("a terminal");
    expect(generic.howToOpen).not.toMatch(/⌘|Win|Ctrl/);
  });
});
