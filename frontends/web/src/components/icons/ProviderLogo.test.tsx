import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ProviderLogo, hasProviderLogo } from "./ProviderLogo";

const PROVIDER_IDS = [
  "reliant",
  "claude",
  "codex",
  "antigravity",
  "copilot",
  "anthropic",
  "openai",
  "gemini",
  "openrouter",
];

describe("ProviderLogo", () => {
  it.each(PROVIDER_IDS)("draws a monochrome inline mark for %s", (id) => {
    const { container } = render(<ProviderLogo provider={id} />);
    const svg = container.querySelector("svg")!;
    expect(svg).toHaveAttribute("data-provider-logo", id);
    expect(svg).toHaveAttribute("fill", "currentColor");
    expect(svg).toHaveAttribute("aria-hidden");
    expect(svg).toHaveClass("h-4", "w-4");
    expect(svg.querySelectorAll("path").length).toBeGreaterThan(0);
    expect(hasProviderLogo(id)).toBe(true);
    // Inherits colour: no per-path fills, no external images.
    for (const path of svg.querySelectorAll("path")) expect(path).not.toHaveAttribute("fill");
    expect(container.querySelector("img, image")).toBeNull();
  });

  it("sizes to 20px on lg", () => {
    const { container } = render(<ProviderLogo provider="claude" size="lg" />);
    expect(container.querySelector("svg")).toHaveClass("h-5", "w-5");
  });

  it("falls back to a neutral mark for an unknown provider", () => {
    const { container } = render(<ProviderLogo provider="acme" />);
    expect(container.querySelector("[data-provider-logo]")).toHaveAttribute(
      "data-provider-logo",
      "fallback",
    );
    expect(hasProviderLogo("acme")).toBe(false);
  });
});
