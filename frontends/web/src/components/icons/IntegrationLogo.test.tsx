/**
 * The integration logo is keyed off the manifest's `icon:` name. Each catalog
 * integration gets its own mark; an unknown name gets a neutral fallback, never
 * an empty box; and GitHub's near-black mark follows the text colour so it
 * stays visible on the dark card.
 */

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { IntegrationLogo, IntegrationLogoTile, hasBrandLogo, integrationLogoKey } from "./IntegrationLogo";

const logo = (container: HTMLElement) => container.querySelector("[data-integration-logo]");

describe("IntegrationLogo", () => {
  it.each(["github", "slack", "gmail", "twilio"])("draws the %s brand mark", (icon) => {
    const { container } = render(<IntegrationLogo icon={icon} />);
    expect(logo(container)).toHaveAttribute("data-integration-logo", icon);
    expect(logo(container)?.tagName.toLowerCase()).toBe("svg");
    expect(hasBrandLogo(icon)).toBe(true);
  });

  it("draws the HTTP integration (icon: globe) as a globe, under either name", () => {
    expect(integrationLogoKey("globe")).toBe("globe");
    expect(integrationLogoKey("http")).toBe("globe");
    expect(hasBrandLogo("globe")).toBe(false);
  });

  it("falls back to a neutral mark for an unknown or missing name", () => {
    for (const icon of ["acme-crm", "", undefined]) {
      const { container, unmount } = render(<IntegrationLogo icon={icon} />);
      expect(logo(container)).toHaveAttribute("data-integration-logo", "fallback");
      unmount();
    }
  });

  it("matches the name case-insensitively", () => {
    expect(integrationLogoKey(" GitHub ")).toBe("github");
  });

  it("keeps brand colours, but draws GitHub in the text colour", () => {
    const github = render(<IntegrationLogo icon="github" />).container;
    expect(github.querySelector("path")).toHaveAttribute("fill", "currentColor");
    expect(logo(github)).toHaveClass("text-foreground");

    const slack = render(<IntegrationLogo icon="slack" />).container;
    const fills = [...slack.querySelectorAll("path")].map((path) => path.getAttribute("fill"));
    expect(new Set(fills)).toEqual(new Set(["#E01E5A", "#36C5F0", "#2EB67D", "#ECB22E"]));
  });

  it("is decorative: the integration's name is always beside it", () => {
    const { container } = render(<IntegrationLogoTile icon="twilio" size="sm" className="extra" />);
    const tile = container.firstElementChild!;
    expect(tile).toHaveAttribute("aria-hidden");
    expect(tile).toHaveClass("h-6", "w-6", "bg-background", "extra");
    expect(logo(container)).toHaveAttribute("data-integration-logo", "twilio");
  });
});
