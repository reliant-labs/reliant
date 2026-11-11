/**
 * The global `a:not(.no-color)` rule (index.css) out-ranks `.text-white`, which
 * turned the primary download button's label primary-blue on sky-600 — so it
 * read as disabled. Every link here that sets its own colour must opt out.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { ReliantDownloadOptions } from "../ReliantDownloadOptions";

function setPlatform(platform: string) {
  Object.defineProperty(navigator, "platform", { value: platform, configurable: true });
}

afterEach(() => {
  Object.defineProperty(navigator, "userAgentData", { value: undefined, configurable: true });
});

describe("ReliantDownloadOptions — link colour", () => {
  it("opts the primary download button out of the global anchor colour", () => {
    setPlatform("Win32");
    render(<ReliantDownloadOptions />);

    const primary = screen.getByRole("link", { name: /Download for Windows x64/i });
    expect(primary).toHaveClass("text-white", "no-color");
  });

  it("opts the 'Other platforms' links out too", () => {
    setPlatform("Win32");
    render(<ReliantDownloadOptions />);
    fireEvent.click(screen.getByRole("button", { name: /Other platforms/i }));

    const links = screen
      .getAllByRole("link")
      .filter((link) => !/Download for/i.test(link.textContent ?? ""));
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) expect(link).toHaveClass("no-color");
  });

  it("opts every link out when the platform is unknown and all builds are listed", () => {
    setPlatform("PlayStation");
    render(<ReliantDownloadOptions />);

    const links = screen.getAllByRole("link");
    expect(links.length).toBeGreaterThan(1);
    for (const link of links) expect(link).toHaveClass("no-color");
  });
});
