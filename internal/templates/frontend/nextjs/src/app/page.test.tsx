// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Smoke test for the dashboard page. Rendered through
// renderWithTransport (src/lib/test-utils) — NOT a bare render() —
// because the dashboard's generated entity tiles (dashboard_gen.tsx)
// call the generated React Query list hooks the moment the project has
// one CRUD entity, and any hook in the tree needs a QueryClientProvider
// and a Connect transport. The empty mockTransport rejects unmatched
// RPCs with Unimplemented; tiles render their "—" fallback and the
// heading still mounts. Register handlers (see test-utils.tsx) to
// assert on live tile counts.

import { screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";

import { mockTransport, renderWithTransport } from "@/lib/test-utils";

import Page from "./page";

describe("DashboardPage", () => {
  it("renders the dashboard heading", () => {
    renderWithTransport(<Page />, { transport: mockTransport() });
    expect(screen.getByRole("heading", { name: "Dashboard" })).toBeTruthy();
  });
});
