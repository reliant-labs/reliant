// Copyright (c) 2025 Reliant Labs

/**
 * The join and the mode gate — the two pure decisions the screen rests on.
 *
 * These are unit tests over plain functions rather than render tests, because
 * what is being pinned is a POLICY, not a layout: which rows exist, and whether
 * the surface may write. A render test would couple those to markup that is
 * expected to keep changing.
 */

import { describe, expect, it } from "vitest";

import type { ForgeSecretsReport } from "@/services/forge/secrets";
import type { ManagedSecretSummary } from "@/services/forge/secretStore";
import {
  joinSecretRows,
  modeSupportsWrite,
  rowStatusVariant,
  surfaceMode,
  tallyRows,
} from "@/services/forge/secretSurface";

function report(provider: string, names: string[]): ForgeSecretsReport {
  return {
    env: "dev",
    provider,
    secrets: names.map((name) => ({
      name,
      present: false,
      declared_by: [{ workload: "admin-server", kind: "service" }],
    })),
  };
}

function summary(overrides: Partial<ManagedSecretSummary> & { name: string }): ManagedSecretSummary {
  return {
    currentVersion: 1,
    oldestVersion: 1,
    maxVersions: 10,
    currentVersionDeleted: false,
    currentVersionDestroyed: false,
    ...overrides,
  };
}

describe("surfaceMode", () => {
  it("treats an external provider as external even when a managed store is reachable", () => {
    // The precedence that matters. forge's declaration decides where the value
    // is read at deploy time, so a reachable managed store must not make an
    // external environment look writable — that would offer a write into a
    // store nothing reads.
    expect(surfaceMode(report("external", ["A"]), true)).toBe("external");
  });

  it("never offers a write for external, file, or unconfigured environments", () => {
    for (const mode of ["external", "file", "none"] as const) {
      expect(modeSupportsWrite(mode)).toBe(false);
    }
    expect(modeSupportsWrite("managed")).toBe(true);
  });

  it("resolves to managed only when forge has no provider AND the store answers", () => {
    expect(surfaceMode(report("none", []), true)).toBe("managed");
    expect(surfaceMode(report("none", []), false)).toBe("none");
  });

  it("keeps a file provider on the file surface, which is read-only from the browser", () => {
    expect(surfaceMode(report("file", ["A"]), true)).toBe("file");
  });
});

describe("joinSecretRows", () => {
  it("marks a declared secret the store has never held as declared-unset", () => {
    // The row the whole module exists to produce: visible from neither source
    // alone, and the one that actually breaks a deploy.
    const rows = joinSecretRows(report("none", ["DATABASE_URL"]), []);
    expect(rows).toHaveLength(1);
    expect(rows[0].origin).toBe("declared-unset");
    expect(rows[0].summary).toBeNull();
  });

  it("marks a stored secret nothing declares as an orphan", () => {
    const rows = joinSecretRows(report("none", []), [summary({ name: "OLD_KEY" })]);
    expect(rows[0].origin).toBe("orphan");
  });

  it("joins a declared secret to its stored record", () => {
    const rows = joinSecretRows(
      report("none", ["API_KEY"]),
      [summary({ name: "API_KEY", currentVersion: 4 })]
    );
    expect(rows[0].origin).toBe("declared");
    expect(rows[0].state).toBe("set");
    expect(rows[0].summary?.currentVersion).toBe(4);
    expect(rows[0].declaredBy[0].workload).toBe("admin-server");
  });

  it("is total over either side being absent", () => {
    // Each backend fails independently; one being down must degrade the
    // screen, not blank it.
    expect(joinSecretRows(null, [summary({ name: "A" })])).toHaveLength(1);
    expect(joinSecretRows(report("none", ["B"]), null)).toHaveLength(1);
    expect(joinSecretRows(null, null)).toEqual([]);
  });

  it("distinguishes a destroyed-to-zero secret from one that was never set", () => {
    // currentVersion 0 is a tombstone — a real row holding nothing — not the
    // same as a secret nobody has created.
    const rows = joinSecretRows(report("none", []), [
      summary({ name: "GONE", currentVersion: 0, currentVersionDestroyed: false }),
    ]);
    expect(rows[0].state).toBe("tombstoned");
  });
});

describe("rowStatusVariant", () => {
  it("reserves the error variant for declared-unset, the only deploy-blocking state", () => {
    const [unset] = joinSecretRows(report("none", ["NEEDED"]), []);
    expect(rowStatusVariant(unset)).toBe("error");

    // A soft delete is a deliberate act with an undo. Painting it the same red
    // as a genuine blocker would flatten the one distinction that matters.
    const [deleted] = joinSecretRows(report("none", []), [
      summary({ name: "X", currentVersionDeleted: true }),
    ]);
    expect(rowStatusVariant(deleted)).not.toBe("error");
  });
});

describe("tallyRows", () => {
  it("derives every count from the rows the reader can see", () => {
    const rows = joinSecretRows(report("none", ["SET", "UNSET"]), [
      summary({ name: "SET" }),
      summary({ name: "ORPHAN" }),
      summary({ name: "DELETED", currentVersionDeleted: true }),
    ]);
    const tally = tallyRows(rows);
    expect(tally.total).toBe(4);
    expect(tally.set).toBe(2); // SET and ORPHAN both hold a live version
    expect(tally.unset).toBe(1);
    expect(tally.inactive).toBe(1);
    // ORPHAN and DELETED are both stored-but-undeclared. `orphans` counts
    // origin, not liveness, so a soft-deleted orphan is still an orphan.
    expect(tally.orphans).toBe(2);
  });
});
