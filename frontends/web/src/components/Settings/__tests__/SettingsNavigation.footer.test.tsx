import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { BuildInfo } from "../../../lib/releaseIdentity";

const build = vi.hoisted(() => ({ current: null as BuildInfo | null }));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../../../lib/appBuild", () => ({ appBuild: () => build.current }));

import { SettingsNavigation } from "../SettingsNavigation";

function renderFooter(version: string) {
  build.current = { version, commit: "8bcfc6ee0123456789abcdef0123456789abcdef", ref: "main", built: "2026-11-18T15:30:00Z" };
  render(<SettingsNavigation activeSection="appearance" onSectionChange={vi.fn()} />);
  return screen.getByTestId("settings-footer-label");
}

/**
 * The footer names the build: `Reliant <version> · About`. Its height must not
 * follow the version's length (a `.dirty` build used to wrap it onto two
 * lines, which moved the visual baseline), so the label stays on one line and
 * is truncated, with the whole text in the tooltip.
 */
describe("settings footer", () => {
  it.each(["1.26.1", "1.26.1-dev+8bcfc6ee.dirty", "1.27.0-rc.1-dev+8bcfc6ee"])(
    "keeps %s on one line, truncated, with the full text in its tooltip",
    (version) => {
      const label = renderFooter(version);
      const text = `Reliant v${version} · About`;
      expect(label).toHaveTextContent(text);
      // `truncate` is overflow-hidden + text-ellipsis + whitespace-nowrap; min-w-0
      // lets the flex item shrink below its text so the ellipsis can apply.
      expect(label).toHaveClass("truncate", "min-w-0");

      const button = screen.getByRole("button", { name: "About" });
      expect(button).toContainElement(label);
      // The tooltip describes the button with the untruncated label.
      expect(button).toHaveAccessibleDescription(text);
    },
  );
});
