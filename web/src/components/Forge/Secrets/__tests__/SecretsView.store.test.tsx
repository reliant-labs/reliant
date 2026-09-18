// Copyright (c) 2025 Reliant Labs

/**
 * store_exists FALSE vs "a store that exists and is empty", plus the inert set.
 *
 * The two store states produce identical per-secret rows — everything missing —
 * and mean different things. "No store file has been created" needs the store
 * created. "A store exists with nothing in it" means someone created it and the
 * values never landed: a half-finished setup, where the next question is who
 * created it and what happened to the values. A UI that says "13 missing" for
 * both has thrown that distinction away.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { SecretsView } from "../SecretsView";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { storeKeyCount, storeState } from "@/services/forge/secrets";

import { controlPlaneDevReport, reportOutcome, topologyOutcome } from "./fixtures";

function renderReport(report: ForgeSecretsReport) {
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

describe("store_exists: false", () => {
  it("says NO STORE FILE YET, distinctly from an empty one", () => {
    const report = controlPlaneDevReport(); // store_exists false
    expect(storeState(report)).toBe("absent");

    renderReport(report);

    const store = screen.getByTestId("secrets-store");
    expect(store.dataset.storeState).toBe("absent");
    expect(store.textContent).toContain("No store file yet");
    // Names the path that does not exist, and the action.
    expect(store.textContent).toContain("dev.yaml");
    expect(store.textContent?.toLowerCase()).toContain("has not been created");
    // Explicitly not the empty-store wording.
    expect(store.textContent).not.toContain("Store exists");
  });
});

describe("a store that exists and is empty", () => {
  it("says so as its own state — a half-finished setup, not an untouched one", () => {
    const report = controlPlaneDevReport();
    report.store_exists = true; // created, but holds nothing
    expect(storeKeyCount(report)).toBe(0);
    expect(storeState(report)).toBe("empty");

    renderReport(report);

    const store = screen.getByTestId("secrets-store");
    expect(store.dataset.storeState).toBe("empty");
    expect(store.textContent).toContain("Store exists, but it is empty");
    expect(store.textContent?.toLowerCase()).toContain("values never landed");
    // Not confusable with the absent case.
    expect(store.textContent).not.toContain("No store file yet");
  });

  it("is a DIFFERENT rendering from store_exists false, despite identical rows", () => {
    const absent = controlPlaneDevReport();
    const empty = { ...controlPlaneDevReport(), store_exists: true };

    const a = renderReport(absent);
    const absentText = a.getByTestId("secrets-store").textContent;
    const absentState = a.getByTestId("secrets-store").dataset.storeState;
    a.unmount();

    const b = renderReport(empty);
    const emptyText = b.getByTestId("secrets-store").textContent;
    const emptyState = b.getByTestId("secrets-store").dataset.storeState;

    expect(absentState).not.toBe(emptyState);
    expect(absentText).not.toBe(emptyText);
    // Both still report the same blocking verdict, because both do block.
    expect(b.getByTestId("secrets-verdict").dataset.blocked).toBe("true");
  });
});

describe("a populated store", () => {
  it("counts the keys it holds, summed from present secrets plus inert keys", () => {
    const report = controlPlaneDevReport();
    report.store_exists = true;
    report.secrets = report.secrets!.map((entry, index) =>
      index < 4 ? { ...entry, present: true } : entry
    );
    report.inert = ["LEFTOVER_KEY", "TYPOED_KEY"];

    // 4 present + 2 inert = 6 keys in the store, derived rather than read from
    // a count field so it cannot disagree with the rows.
    expect(storeKeyCount(report)).toBe(6);
    expect(storeState(report)).toBe("populated");

    renderReport(report);
    const store = screen.getByTestId("secrets-store");
    expect(store.dataset.storeState).toBe("populated");
    expect(store.textContent).toContain("6 keys");
  });
});

describe("the inert set", () => {
  it("explains what inert MEANS rather than just listing names", () => {
    const report = controlPlaneDevReport();
    report.store_exists = true;
    report.inert = ["GITHUB_CLIENT_SECRETT", "LOG_LEVEL"];

    renderReport(report);

    const inert = screen.getByTestId("secrets-inert");
    expect(inert.textContent).toContain("2 inert keys");
    // The explanation, not just the list: nothing injects them.
    expect(inert.textContent?.toLowerCase()).toContain("no workload declares them");
    expect(inert.textContent?.toLowerCase()).toContain("injects them nowhere");
    // Names the two real causes, including where config actually belongs.
    expect(inert.textContent).toContain("typo");
    expect(inert.textContent).toContain("deploy/kcl/<env>/config.k");
    // The names are still there.
    expect(inert.textContent).toContain("GITHUB_CLIENT_SECRETT");
    expect(inert.textContent).toContain("LOG_LEVEL");
    // Informational, not a failure — an inert key breaks nothing today.
    expect(inert.className).not.toMatch(/destructive/);
    expect(inert.className).toContain("border-dashed");
  });

  it("is absent entirely when there are no inert keys", () => {
    // Not a reassuring "0 inert keys" nobody asked about.
    renderReport(controlPlaneDevReport());
    expect(screen.queryByTestId("secrets-inert")).toBeNull();
  });
});

describe("an environment that declares no secrets", () => {
  it("says nothing needs setting rather than rendering an empty table", () => {
    const report: ForgeSecretsReport = {
      env: "dev",
      provider: "file",
      store_path: "/tmp/dev.yaml",
      store_exists: true,
      secrets: [],
      inert: [],
      missing: [],
      missing_count: 0,
      ok: true,
    };
    renderReport(report);
    expect(screen.getByTestId("secrets-none-declared").textContent).toContain("forge env up");
  });
});
