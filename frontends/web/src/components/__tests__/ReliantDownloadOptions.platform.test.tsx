/**
 * The Homebrew cask is macOS-only, and onboarding used to render it on every
 * platform — so Windows users were handed `brew install --cask …`, a command
 * that cannot work, on the one screen meant to unblock them.
 *
 * These tests drive the real `useDetectedOS` by stubbing what it actually
 * reads (`navigator.platform`, plus the async Mac arch refinement), rather
 * than mocking the module under test into agreement with itself.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ReliantDownloadOptions } from "../ReliantDownloadOptions";
import { HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";

function setPlatform(platform: string) {
  Object.defineProperty(navigator, "platform", {
    value: platform,
    configurable: true,
  });
}

/**
 * The Mac branch refines arm64 vs x64 asynchronously. Answering here keeps
 * the two Mac cases distinct and avoids a WebGL probe in jsdom.
 */
function setMacArch(architecture: "arm" | "x86") {
  Object.defineProperty(navigator, "userAgentData", {
    value: {
      getHighEntropyValues: () => Promise.resolve({ architecture }),
    },
    configurable: true,
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  Object.defineProperty(navigator, "userAgentData", {
    value: undefined,
    configurable: true,
  });
});

describe("ReliantDownloadOptions — Homebrew is gated to macOS", () => {
  it.each([
    ["Win32", "windows"],
    ["Linux x86_64", "linux"],
  ])("does not render the cask on %s", async (platform) => {
    setPlatform(platform);
    render(<ReliantDownloadOptions />);

    // The reported bug: `brew install --cask …` is a dead end here.
    await waitFor(() => {
      expect(screen.queryByText(HOMEBREW_CASK_INSTALL)).not.toBeInTheDocument();
    });
    expect(screen.queryByText(/Homebrew/i)).not.toBeInTheDocument();

    // And the platform-neutral CLI guidance stands in for it.
    expect(screen.getByText(/adds it to your PATH/i)).toBeInTheDocument();
  });

  it.each([
    ["arm" as const, "Mac (Apple Silicon)"],
    ["x86" as const, "Mac (Intel)"],
  ])("renders the cask on macOS (%s)", async (architecture, expectedLabel) => {
    setPlatform("MacIntel");
    setMacArch(architecture);
    render(<ReliantDownloadOptions />);

    await waitFor(() => {
      expect(
        screen.getByText(`Download for ${expectedLabel}`),
      ).toBeInTheDocument();
    });
    expect(screen.getByText(HOMEBREW_CASK_INSTALL)).toBeInTheDocument();
    expect(screen.getByText(/Or install via Homebrew/i)).toBeInTheDocument();
  });

  it("labels the cask as macOS-specific when the platform is unknown", () => {
    setPlatform("SomethingElse");
    render(<ReliantDownloadOptions />);

    expect(screen.getByText(HOMEBREW_CASK_INSTALL)).toBeInTheDocument();
    expect(screen.getByText(/On macOS, you can install via Homebrew/i))
      .toBeInTheDocument();
  });

  it("offers .deb builds alongside the AppImage for Linux", async () => {
    setPlatform("Linux x86_64");
    render(<ReliantDownloadOptions />);

    await waitFor(() => {
      expect(
        screen.getByText("Download for Linux x86_64"),
      ).toBeInTheDocument();
    });
    // The .deb builds live behind the "Other platforms" disclosure.
    fireEvent.click(screen.getByText("Other platforms"));
    expect(
      document.querySelector('a[href$="Reliant-latest-linux-amd64.deb"]'),
    ).not.toBeNull();
  });
});
