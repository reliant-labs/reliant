// Copyright (c) 2025 Reliant Labs

/**
 * THE CONSTRAINT THAT OUTRANKS EVERY OTHER TEST IN THIS DIRECTORY.
 *
 * A secret value must never reach the browser, and the UI must have no way to
 * show one even if a value somehow arrived. That is enforced in three places on
 * the way here — forge's report type graph is pinned by an allow-list reflection
 * test, the daemon withholds forge's stderr on the secret path rather than risk
 * echoing a value, and TestListForgeSecretsResponseCannotCarrySecretValues pins
 * the RPC response — and this file is the fourth: the LAST layer, where a
 * well-meaning "just show a masked preview" would land.
 *
 * The canary is a string that appears nowhere in the report's legitimate fields.
 * It is planted in every unexpected position a value could plausibly occupy —
 * an extra field on an entry, an extra field on a declaration, and the top level
 * of the document — and the assertion is that it appears nowhere in the rendered
 * DOM, not in text, not in an attribute, not in a title.
 *
 * If this test fails, do not adjust the canary. Remove whatever renders it.
 */

import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

import { SecretsView } from "../SecretsView";
import type { ForgeSecretsReport } from "@/services/forge/secrets";

import { reportOutcome, topologyOutcome } from "./fixtures";

const CANARY = "hunter2-SUPERSECRET-canary-value";

function renderWithCanary(report: ForgeSecretsReport) {
  return render(
    <SecretsView
      outcome={reportOutcome(report)}
      isLoading={false}
      topology={topologyOutcome(["dev"])}
      selectedEnv="dev"
      onSelectEnv={vi.fn()}
      projectName="control-plane"
    />
  );
}

describe("secret values cannot reach the rendered output", () => {
  it("renders no canary planted anywhere in the report document", () => {
    // Every position a value could arrive in, all at once. None of these fields
    // exist in the typed contract, which is the point: the view reads only the
    // declared fields, so an undeclared one has nowhere to go.
    const report = {
      env: "dev",
      provider: "file",
      store_path: "/tmp/dev.yaml",
      store_exists: true,
      // Top level.
      value: CANARY,
      values: { GITHUB_CLIENT_SECRET: CANARY },
      secrets: [
        {
          name: "GITHUB_CLIENT_SECRET",
          present: true,
          // On the entry.
          value: CANARY,
          masked: CANARY,
          preview: CANARY,
          declared_by: [
            {
              workload: "admin-server",
              kind: "service",
              secret_name: "control-plane-secrets",
              secret_key: "github_client_secret",
              // On the declaration.
              value: CANARY,
            },
          ],
        },
      ],
      inert: ["OLD_KEY"],
      missing: [],
      missing_count: 0,
      ok: true,
    } as unknown as ForgeSecretsReport;

    const { container } = renderWithCanary(report);

    // Not in visible text.
    expect(container.textContent).not.toContain(CANARY);
    // Not in any attribute either — a title, aria-label, data-* or value prop
    // would all be recoverable by a user and by a screenshot.
    expect(container.innerHTML).not.toContain(CANARY);
  });

  it("offers no control that could reveal, copy or unmask a value", () => {
    const report = {
      env: "dev",
      provider: "file",
      store_path: "/tmp/dev.yaml",
      store_exists: true,
      secrets: [
        {
          name: "GITHUB_CLIENT_SECRET",
          present: true,
          declared_by: [{ workload: "admin-server", kind: "service" }],
        },
      ],
      inert: [],
      missing: [],
      missing_count: 0,
      ok: true,
    } as ForgeSecretsReport;

    const { container } = renderWithCanary(report);
    const html = container.innerHTML.toLowerCase();

    // No INTERACTIVE affordance implying a value exists to be looked at. Scoped
    // to controls rather than to all text on purpose: the header prose says
    // there is "no value here to reveal", which is the opposite of a reveal
    // button and must not trip this.
    const controls = [
      ...container.querySelectorAll("button, a, input, select, [role='button'], [role='switch']"),
    ];
    for (const control of controls) {
      const label = `${control.textContent ?? ""} ${control.getAttribute("aria-label") ?? ""} ${
        control.getAttribute("title") ?? ""
      }`.toLowerCase();
      for (const forbidden of ["reveal", "unmask", "copy", "show value", "value"]) {
        expect(label).not.toContain(forbidden);
      }
    }

    // No masked placeholder standing in for a value — a row of dots teaches the
    // reader that a value is present in this page, which is false.
    expect(html).not.toContain("••");
    expect(html).not.toContain("＊＊");
    // No password or text input, the other way a value ends up in the DOM.
    expect(container.querySelector("input")).toBeNull();

    // And no column promising one. The header row names presence, never value.
    const headers = [...container.querySelectorAll("thead th")].map((th) =>
      (th.textContent ?? "").toLowerCase()
    );
    expect(headers).toContain("presence");
    for (const header of headers) {
      expect(header).not.toContain("value");
    }
  });

  it("keeps the response body out of the error path", () => {
    // A transport failure renders the transport error only. The report is not
    // stringified into a message, which is the most common way a body leaks.
    const { container } = render(
      <SecretsView
        outcome={undefined}
        isLoading={false}
        error={new Error("daemon unreachable")}
        topology={topologyOutcome(["dev"])}
        selectedEnv="dev"
        onSelectEnv={vi.fn()}
      />
    );
    expect(container.textContent).toContain("daemon unreachable");
    expect(container.innerHTML).not.toContain(CANARY);
    expect(container.innerHTML).not.toContain("report_json");
  });
});
