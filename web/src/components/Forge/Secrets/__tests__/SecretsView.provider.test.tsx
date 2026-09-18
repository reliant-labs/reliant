// Copyright (c) 2025 Reliant Labs

/**
 * PROVIDER KIND IS LOAD-BEARING, and these tests are the reason the data layer
 * gates presence on it before reading the entry's own flag.
 *
 * Under an `external` provider forge still populates `missing` and
 * `missing_count`, because from forge's point of view it has no value for those
 * keys. Rendering that as "13 missing secrets" would manufacture an outage in a
 * correctly-configured environment — the values are in an external manager and
 * nobody asked it. So external presence is UNKNOWN: not a pass, not a failure.
 *
 * `none` is a third case. No store is configured, so there was nothing that
 * could have been checked, and the declared list is a reference rather than a
 * verdict.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { SecretsView } from "../SecretsView";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { missingNames, presenceOf, providerKind, storeState } from "@/services/forge/secrets";

import { controlPlaneDevReport, reportOutcome, topologyOutcome } from "./fixtures";

function renderReport(report: ForgeSecretsReport) {
  return render(
    <SecretsView
      outcome={reportOutcome(report)}
      isLoading={false}
      topology={topologyOutcome(["dev", "prod"])}
      selectedEnv="dev"
      onSelectEnv={vi.fn()}
      projectName="control-plane"
    />
  );
}

describe("provider: file", () => {
  it("makes the missing count the headline and says it blocks env up", () => {
    // The real control-plane dev shape: 13 declared, 13 missing.
    renderReport(controlPlaneDevReport());

    const verdict = screen.getByTestId("secrets-verdict");
    expect(verdict.dataset.blocked).toBe("true");
    expect(screen.getByTestId("missing-count").textContent).toBe("13");
    // The actionable consequence, not just a number.
    expect(verdict.textContent).toContain("forge secret ensure");
    expect(verdict.textContent?.toLowerCase()).toContain("refuse to start");
    // Painted as a real problem, because under a file provider it is one.
    expect(verdict.className).toMatch(/destructive/);
  });

  it("marks each declared secret missing and names who declares it", () => {
    renderReport(controlPlaneDevReport());

    const badge = screen.getByTestId("presence-GITHUB_CLIENT_SECRET");
    expect(badge.dataset.presence).toBe("missing");
    expect(badge.dataset.certainty).toBe("known-bad");
    expect(badge.textContent).toContain("Missing");

    // The coordinates: who breaks, and which Secret key it is fetched by.
    const declaredBy = screen.getByTestId("declared-by-GITHUB_CLIENT_SECRET");
    expect(declaredBy.textContent).toContain("admin-server");
    expect(declaredBy.textContent).toContain("service");
    expect(declaredBy.textContent).toContain("control-plane-secrets/github_client_secret");
  });

  it("passes cleanly when every declared secret has a value", () => {
    const report = controlPlaneDevReport();
    report.store_exists = true;
    report.secrets = report.secrets!.map((entry) => ({ ...entry, present: true }));
    report.missing = [];
    report.missing_count = 0;
    report.ok = true;

    renderReport(report);

    const verdict = screen.getByTestId("secrets-verdict");
    expect(verdict.dataset.blocked).toBe("false");
    expect(verdict.textContent).toContain("Every declared secret has a value");
    expect(screen.getByTestId("presence-GITHUB_CLIENT_SECRET").dataset.certainty).toBe("known-good");
  });
});

describe("provider: external", () => {
  function externalReport(): ForgeSecretsReport {
    // Forge's own missing array is POPULATED here — it has no values — and the
    // correct rendering is still zero missing.
    const report = controlPlaneDevReport();
    report.provider = "external";
    report.store_path = "";
    report.store_exists = false;
    return report;
  }

  it("renders presence as unknown rather than as a pile of missing secrets", () => {
    const report = externalReport();
    // The data layer refuses to derive "missing" from forge's own array.
    expect(providerKind(report)).toBe("external");
    expect(report.missing_count).toBe(13);
    expect(missingNames(report)).toHaveLength(0);

    renderReport(report);

    // No verdict at all — there is nothing forge is in a position to verdict on.
    expect(screen.queryByTestId("secrets-verdict")).toBeNull();
    const panel = screen.getByTestId("secrets-no-verdict");
    expect(panel.textContent?.toLowerCase()).toContain("unknown");
    // Not painted as a failure, and not as a pass.
    expect(panel.className).not.toMatch(/destructive/);
    expect(panel.className).not.toMatch(/bg-success/);
    expect(panel.className).toContain("border-dashed");
    // The number 13 must not appear as a missing count anywhere.
    expect(screen.queryByTestId("missing-count")).toBeNull();
  });

  it("gives every row the unknown treatment, distinct from both certain ones", () => {
    renderReport(externalReport());

    const badge = screen.getByTestId("presence-GITHUB_CLIENT_SECRET");
    expect(badge.dataset.presence).toBe("not-held");
    expect(badge.dataset.certainty).toBe("unknown");
    expect(badge.dataset.certainty).not.toBe("known-bad");
    expect(badge.dataset.certainty).not.toBe("known-good");
    expect(badge.className).toContain("border-dashed");
    expect(badge.className).not.toMatch(/bg-destructive/);
    expect(badge.className).not.toMatch(/bg-success/);
    // Says WHY it is unknown rather than showing a bare question mark.
    expect(badge.textContent).toContain("Held externally");
  });

  it("explains that forge does not hold the values", () => {
    renderReport(externalReport());
    const provider = screen.getByTestId("secrets-provider");
    expect(provider.textContent).toContain("External secret manager");
    expect(provider.textContent?.toLowerCase()).toContain("did not look");
    expect(screen.getByTestId("secrets-store").dataset.storeState).toBe("external");
  });
});

describe("provider: none", () => {
  it("says no store is configured instead of reporting failures", () => {
    const report = controlPlaneDevReport();
    report.provider = "none";
    report.store_path = "";
    report.store_exists = false;

    expect(storeState(report)).toBe("unconfigured");
    expect(presenceOf(report, report.secrets![0])).toBe("undetermined");

    renderReport(report);

    expect(screen.queryByTestId("secrets-verdict")).toBeNull();
    expect(screen.getByTestId("secrets-provider").textContent).toContain("No store configured");
    const store = screen.getByTestId("secrets-store");
    expect(store.dataset.storeState).toBe("unconfigured");
    expect(store.className).toContain("border-dashed");
    expect(store.className).not.toMatch(/destructive/);

    const badge = screen.getByTestId("presence-GITHUB_CLIENT_SECRET");
    expect(badge.dataset.certainty).toBe("unknown");
    expect(badge.textContent).toContain("Not known");
  });
});

describe("an unrecognised provider", () => {
  it("falls back to unknown rather than assuming file", () => {
    const report = controlPlaneDevReport();
    report.provider = "vault-direct-v2";

    // The fallback DIRECTION is the load-bearing part: guessing `file` would
    // render a newer forge's secrets as a wall of false failures.
    expect(providerKind(report)).toBe("unknown");
    expect(missingNames(report)).toHaveLength(0);

    renderReport(report);
    expect(screen.queryByTestId("secrets-verdict")).toBeNull();
    expect(screen.getByTestId("presence-GITHUB_CLIENT_SECRET").dataset.certainty).toBe("unknown");
  });
});
