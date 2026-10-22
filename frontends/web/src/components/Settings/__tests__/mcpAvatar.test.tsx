import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import {
  buildMcpAvatarContainerClassName,
  getMcpAvatarColorClass,
  getMcpIconUrl,
  getMcpInitials,
  mcpVisualSeed,
  MCPAvatar,
} from "../mcpAvatar";

describe("mcpAvatar helpers", () => {
  it("returns deterministic seed for same input", () => {
    expect(mcpVisualSeed("github", "GitHub")).toBe(mcpVisualSeed("github", "GitHub"));
  });

  it("returns deterministic color class for same input", () => {
    expect(getMcpAvatarColorClass("github", "GitHub")).toBe(getMcpAvatarColorClass("github", "GitHub"));
  });

  it("builds two-letter initials from multi-word names", () => {
    expect(getMcpInitials("Chrome DevTools")).toBe("CD");
  });

  it("builds single-letter initials for single-word names", () => {
    expect(getMcpInitials("Supabase")).toBe("S");
    expect(getMcpInitials("Fetch")).toBe("F");
  });

  it("falls back to M initials for empty names", () => {
    expect(getMcpInitials("")).toBe("M");
  });

  it("maps known discover MCPs to icon URLs", () => {
    expect(getMcpIconUrl("chrome-devtools")).toContain("chrome-devtools-square-64.svg");
    expect(getMcpIconUrl("supabase")).toContain("supabase-logo-icon.svg");
    expect(getMcpIconUrl("github")).toContain("cdn.simpleicons.org/github");
    expect(getMcpIconUrl("sqlite")).toContain("cdn.simpleicons.org/sqlite");
    expect(getMcpIconUrl("postgres")).toContain("cdn.simpleicons.org/postgresql");
    expect(getMcpIconUrl("slack")).toContain("icons/slack.svg");
    expect(getMcpIconUrl("sentry")).toContain("cdn.simpleicons.org/sentry");
    expect(getMcpIconUrl("aws")).toContain("icons/amazonaws.svg");
    expect(getMcpIconUrl("docker")).toContain("cdn.simpleicons.org/docker");
  });

  it("returns undefined for unknown icon mappings", () => {
    expect(getMcpIconUrl("some-unknown-mcp")).toBeUndefined();
  });

  it("uses neutral background when image icon exists", () => {
    const className = buildMcpAvatarContainerClassName({
      hasImageIcon: true,
      colorClass: "from-sky-500/30 to-blue-500/20",
    });

    expect(className).toContain("bg-white/95");
    expect(className).not.toContain("bg-gradient-to-br");
  });

  it("uses gradient background when no image icon exists", () => {
    const className = buildMcpAvatarContainerClassName({
      hasImageIcon: false,
      colorClass: "from-sky-500/30 to-blue-500/20",
    });

    expect(className).toContain("bg-gradient-to-br");
    expect(className).toContain("from-sky-500/30 to-blue-500/20");
    expect(className).toContain("text-foreground/90");
  });
});

describe("MCPAvatar", () => {
  it("renders initials fallback when icon is not provided", () => {
    render(<MCPAvatar name="Custom Server" />);
    expect(screen.getByText("CS")).toBeInTheDocument();
  });

  it("falls back to initials when image fails to load", () => {
    render(<MCPAvatar name="Docker" iconSrc="https://example.com/broken.svg" />);

    const img = screen.getByRole("img", { name: "Docker icon" });
    fireEvent.error(img);

    expect(screen.getByText("D")).toBeInTheDocument();
  });
});
