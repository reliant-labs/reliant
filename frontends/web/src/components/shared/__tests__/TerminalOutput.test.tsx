import { render } from "@testing-library/react";
import DOMPurify from "dompurify";
import { beforeAll, describe, expect, it } from "vitest";
import { BackgroundProcessStatus } from "../../../hooks/background-queries";
import { TerminalOutput, type ProcessInfo } from "../TerminalOutput";

const process: ProcessInfo = {
  id: "p1",
  command: "npm run dev",
  status: BackgroundProcessStatus.COMPLETED,
  start_time: "2026-11-01T00:00:00Z",
  end_time: "2026-11-01T00:00:05Z",
  exit_code: 0,
};

beforeAll(() => {
  // jsdom has no layout; the auto-scroll effect calls this on every render.
  Element.prototype.scrollIntoView = () => {};
});

describe("TerminalOutput", () => {
  it("renders process output as ANSI-coloured HTML with no live markup", () => {
    const { container } = render(
      <TerminalOutput
        process={process}
        output={{
          stdout: "",
          stderr: "",
          combined: [
            { type: "stdout", text: "\x1b[31mred\x1b[0m <img src=x onerror=alert(1)>" },
          ],
        }}
      />,
    );

    expect(container.querySelector(".ansi-red-fg")?.textContent).toBe("red");
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  // Process output is attacker-influenced, so the sanitizer stays on a release
  // with every published DOMPurify advisory fixed (3.4.17).
  it("sanitizes with a patched DOMPurify", () => {
    const [major, minor, patch] = DOMPurify.version.split(".").map(Number);
    expect(major * 1e6 + minor * 1e3 + patch).toBeGreaterThanOrEqual(3_004_017);
  });
});
