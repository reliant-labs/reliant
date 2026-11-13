// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../lib/telemetry", () => ({ reportErrorToTelemetry: vi.fn() }));
vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => ({ crashReportingEnabled: false, analyticsEnabled: false }),
}));

import { ErrorFallbackUI } from "../ErrorBoundary";
import { StaleBuildError } from "../../lib/lazyRoute";

// Import errors carry the failing URL, which can hold credentials or tokens:
// neither the visible message nor the copied report may repeat them.
const SECRET_URL = "https://alice:hunter2@preview.test/assets/route.js?token=s3cr3t-query#s3cr3t-hash";
const LEAKS = /alice|hunter2|s3cr3t|token=/;

describe("ErrorFallbackUI redaction", () => {
  it("redacts the URL in a top-level error message, on screen and in the report", () => {
    const { container } = render(
      <ErrorFallbackUI error={new TypeError(`Failed to fetch dynamically imported module: ${SECRET_URL}`)} onReload={() => {}} />,
    );
    expect(screen.getAllByText(/preview\.test\/assets\/route\.js/).length).toBeGreaterThan(0);
    expect(container.textContent).not.toMatch(LEAKS);
  });

  it("redacts the URL in a nested import error", () => {
    const cause = new TypeError(`Failed to fetch dynamically imported module: ${SECRET_URL}`);
    const { container } = render(<ErrorFallbackUI error={new StaleBuildError(cause)} onReload={() => {}} />);
    expect(container.textContent).toContain("https://preview.test/assets/route.js");
    expect(container.textContent).not.toMatch(LEAKS);
  });
});
