import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MobileInstallMenuItem } from "../MobileInstallMenuItem";

describe("MobileInstallMenuItem", () => {
  it("discloses both platforms' guidance with keyboard controls without opening another dialog", async () => {
    const user = userEvent.setup();
    render(<MobileInstallMenuItem />);
    const trigger = screen.getByRole("button", { name: "Add to Home Screen" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    const instructions = document.getElementById(trigger.getAttribute("aria-controls")!);
    expect(instructions).not.toBeVisible();
    await user.tab();
    expect(trigger).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(instructions).toBeVisible();
    expect(screen.getByRole("region", { name: "iPhone and iPad installation" })).toHaveTextContent("Open as Web App");
    expect(screen.getByRole("region", { name: "Android installation" })).toHaveTextContent("Create shortcut");
    expect(screen.getByText(/does not enable offline use/)).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.keyboard(" ");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(instructions).not.toBeVisible();
    expect(trigger).toHaveFocus();
  });
});

// Read shipped bytes, not fixtures: manifest paths and PNG declarations must agree.
describe("Home Screen assets", () => {
  const publicDir = resolve(process.cwd(), "public");
  const manifest = JSON.parse(readFileSync(resolve(publicDir, "manifest.webmanifest"), "utf8"));

  it("launches chats on the current origin, with standalone display and root scope", () => {
    expect(manifest.start_url).toBe("/m/chats");
    expect(manifest.scope).toBe("/");
    expect(manifest.id).toBe("/");
    expect(manifest.display).toBe("standalone");
    expect(manifest.name).toBe("Reliant");
    for (const origin of ["https://app.example.test", "https://staging.example.test"]) {
      expect(new URL(manifest.start_url, origin).origin).toBe(origin);
    }
  });

  it("ships PNGs at the declared sizes with separate any and maskable purposes", () => {
    expect(manifest.icons.map((icon: { sizes: string; purpose: string }) => [icon.sizes, icon.purpose])).toEqual([
      ["192x192", "any"], ["512x512", "any"], ["512x512", "maskable"],
    ]);
    for (const icon of [...manifest.icons, { src: "/apple-touch-icon.png", sizes: "180x180", type: "image/png" }]) {
      expect(icon.type).toBe("image/png");
      expect(icon.src).toMatch(/^\/(?!\/)/);
      const png = readFileSync(resolve(publicDir, icon.src.slice(1)));
      expect(png.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
      expect(`${png.readUInt32BE(16)}x${png.readUInt32BE(20)}`).toBe(icon.sizes);
    }
  });

  it("links the manifest and iOS metadata while retaining the runtime bootstrap", () => {
    const html = readFileSync(resolve(publicDir, "../index.html"), "utf8");
    expect(html).toContain('rel="manifest" href="/manifest.webmanifest"');
    expect(html).toContain('name="apple-mobile-web-app-capable" content="yes"');
    expect(html).toContain('name="apple-mobile-web-app-title" content="Reliant"');
    expect(html).toContain('name="apple-mobile-web-app-status-bar-style" content="default"');
    expect(html).toContain('sizes="180x180" href="/apple-touch-icon.png"');
    expect(html).toContain('<script src="%BASE_URL%config.js"></script>');
    expect(html).toContain('window.__THEME_APPLIED__ = true');
    expect(html).toContain('src="/src/main.tsx"');
  });
});
