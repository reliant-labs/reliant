// Copyright (c) 2025 Reliant Labs

/** The app's /forge/demo and /demo leave for the demo bundle (demoHandoff.ts). */

import { afterEach, describe, expect, it, vi } from "vitest";
import { isRedirect } from "@tanstack/react-router";

const openExternalLink = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("@/lib/open-link", () => ({ openExternalLink }));

import { forgeDemoHref, leaveForDemo } from "../demoHandoff";

function hrefOf(searchStr: string): string | undefined {
  try {
    leaveForDemo({ location: { searchStr } });
  } catch (err) {
    if (isRedirect(err)) return err.options.href;
    throw err;
  }
  return undefined;
}

describe("the forge demo handoff", () => {
  afterEach(() => {
    delete (window as { electronAPI?: unknown }).electronAPI;
    openExternalLink.mockClear();
  });

  it("names the demo bundle's /forge/demo, query kept", () => {
    expect(forgeDemoHref("")).toBe("https://demo.reliantlabs.io/forge/demo");
    expect(forgeDemoHref("?tour=forge:fg-intro&view=overview")).toBe("https://demo.reliantlabs.io/forge/demo?tour=forge:fg-intro&view=overview");
  });

  it("leaves the web app for it", () => {
    expect(hrefOf("?tour=forge:fg-intro")).toBe("https://demo.reliantlabs.io/forge/demo?tour=forge:fg-intro");
    expect(openExternalLink).not.toHaveBeenCalled();
  });

  it("opens it in the browser from the desktop app, which stays on its home", () => {
    (window as { electronAPI?: unknown }).electronAPI = {};
    expect(hrefOf("?section=home")).toBe("/");
    expect(openExternalLink).toHaveBeenCalledWith("https://demo.reliantlabs.io/forge/demo?section=home");
  });
});
