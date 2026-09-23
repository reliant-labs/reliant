// Copyright (c) 2025 Reliant Labs

/**
 * The visual and behavioural contract of the managed secrets surface.
 *
 * Three things are pinned here, in descending order of how bad it would be to
 * lose them:
 *
 *   1. NO VALUE, EVER. There is no reveal, no value column, and no input that
 *      renders a stored value back. The store structurally refuses reads, so a
 *      UI that appeared to show one would be lying.
 *   2. EXTERNAL OFFERS NO CREATE. forge never sees those values; a create
 *      button there teaches a falsehood about who owns the secret.
 *   3. DELETE AND DESTROY ARE DISTINCT. Destroy is confirmed and irreversible;
 *      delete has an undo sitting next to it.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ForgeSecretsReport } from "@/services/forge/secrets";
import type { ManagedSecretSummary, ManagedSecretVersion } from "@/services/forge/secretStore";

import { ManagedSecretsView } from "../ManagedSecretsView";
import type { ManagedSecretsViewProps } from "../ManagedSecretsView";

function summary(overrides: Partial<ManagedSecretSummary> & { name: string }): ManagedSecretSummary {
  return {
    currentVersion: 2,
    oldestVersion: 1,
    maxVersions: 10,
    createdAt: "2026-01-02T03:04:05Z",
    updatedAt: "2026-02-03T04:05:06Z",
    currentVersionDeleted: false,
    currentVersionDestroyed: false,
    ...overrides,
  };
}

function declaredReport(provider: string, names: string[]): ForgeSecretsReport {
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

function props(overrides: Partial<ManagedSecretsViewProps> = {}): ManagedSecretsViewProps {
  return {
    env: "dev",
    mode: "managed",
    availability: "available",
    report: declaredReport("none", ["DATABASE_URL"]),
    managed: [summary({ name: "DATABASE_URL" })],
    isLoading: false,
    selectedName: null,
    onSelect: vi.fn(),
    versions: [],
    versionsLoading: false,
    onAdd: vi.fn(),
    onSet: vi.fn(),
    onDelete: vi.fn(),
    onUndelete: vi.fn(),
    onDestroy: vi.fn(),
    pendingVersion: null,
    ...overrides,
  };
}

describe("no value reaches the screen", () => {
  it("renders no column, cell, or control that could carry a secret value", () => {
    const { container } = render(<ManagedSecretsView {...props()} />);

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent?.toLowerCase());
    expect(headers).not.toContain("value");
    // The vocabulary a reveal affordance would use, in any casing.
    expect(container.textContent).not.toMatch(/reveal|unmask|show value|copy value/i);
    // A list view has nothing to type into; an input here would be the seam a
    // value could enter or leave through.
    expect(container.querySelectorAll("input")).toHaveLength(0);
  });

  it("shows version metadata instead — the thing that makes history answerable", () => {
    render(<ManagedSecretsView {...props()} />);
    const row = screen.getByTestId("secret-row-DATABASE_URL");
    expect(within(row).getByText("v2")).toBeInTheDocument();
    expect(within(row).getByText("Set")).toBeInTheDocument();
  });
});

describe("provider awareness", () => {
  it("offers no create affordance for an external secret manager", () => {
    // The dangerous falsehood: forge never sees these values, so a create
    // button would imply reliant is the system of record for a secret it has
    // never held.
    render(
      <ManagedSecretsView
        {...props({ mode: "external", report: declaredReport("external", []), managed: [] })}
      />
    );
    expect(screen.queryByTestId("add-secret")).not.toBeInTheDocument();
    expect(screen.queryByTestId("add-secret-empty")).not.toBeInTheDocument();
    expect(screen.getByTestId("secrets-empty").textContent).toMatch(
      /external secret manager holds the values/i
    );
  });

  it("offers no create affordance for the local file provider", () => {
    // The write path there is `forge secret set` against a file on the user's
    // own machine, which the browser cannot reach. A form would be a button
    // that lies.
    render(
      <ManagedSecretsView
        {...props({ mode: "file", report: declaredReport("file", []), managed: [] })}
      />
    );
    expect(screen.queryByTestId("add-secret")).not.toBeInTheDocument();
    expect(screen.getByTestId("secrets-empty").textContent).toMatch(/forge secret set/i);
  });

  it("offers create on the managed store", () => {
    render(<ManagedSecretsView {...props()} />);
    expect(screen.getByTestId("add-secret")).toBeInTheDocument();
  });

  it("says the store is unreachable without claiming anything about the secrets", () => {
    render(<ManagedSecretsView {...props({ availability: "unreachable", managed: [] })} />);
    // A connection failure must not read as "you have no secrets".
    expect(screen.getByTestId("managed-secrets").textContent).toMatch(
      /could not be reached.*not a statement about your secrets/i
    );
  });
});

describe("the declared-but-unset row", () => {
  it("names a declared secret the store has never held, and marks it as the blocker", () => {
    render(
      <ManagedSecretsView
        {...props({ report: declaredReport("none", ["MISSING_KEY"]), managed: [] })}
      />
    );
    const row = screen.getByTestId("secret-row-MISSING_KEY");
    expect(within(row).getByText("Not set")).toBeInTheDocument();
    // It is the only count that takes colour, and only when non-zero.
    expect(screen.getByTestId("managed-secrets").textContent).toMatch(/1 not set/);
  });
});

describe("version history: delete versus destroy", () => {
  const versions: ManagedSecretVersion[] = [
    { version: 3, createdAt: "2026-03-01T00:00:00Z", destroyed: false },
    { version: 2, createdAt: "2026-02-01T00:00:00Z", deletedAt: "2026-02-15T00:00:00Z", destroyed: false },
    { version: 1, createdAt: "2026-01-01T00:00:00Z", destroyed: true },
  ];

  function detail(overrides: Partial<ManagedSecretsViewProps> = {}) {
    return props({ selectedName: "DATABASE_URL", versions, ...overrides });
  }

  it("gives a soft-deleted version an undo, and a live one a delete", () => {
    render(<ManagedSecretsView {...detail()} />);
    expect(within(screen.getByTestId("version-row-3")).getByText("Delete")).toBeInTheDocument();
    // The undo IS the explanation that the delete was soft.
    expect(within(screen.getByTestId("version-row-2")).getByText("Undelete")).toBeInTheDocument();
  });

  it("leaves a destroyed version in the history with no action on it", () => {
    render(<ManagedSecretsView {...detail()} />);
    const destroyed = screen.getByTestId("version-row-1");
    // The row stays: removing it would make the history lie by omission.
    expect(destroyed).toHaveAttribute("data-state", "destroyed");
    expect(within(destroyed).queryByText("Delete")).not.toBeInTheDocument();
    expect(within(destroyed).queryByText("Undelete")).not.toBeInTheDocument();
    expect(within(destroyed).queryByText("Destroy")).not.toBeInTheDocument();
  });

  it("soft-deletes immediately — it is recoverable, so it needs no confirmation", async () => {
    const onDelete = vi.fn();
    render(<ManagedSecretsView {...detail({ onDelete })} />);
    await userEvent.click(within(screen.getByTestId("version-row-3")).getByText("Delete"));
    expect(onDelete).toHaveBeenCalledWith("DATABASE_URL", 3);
  });

  it("requires confirmation before destroying, and says the loss is permanent", async () => {
    const onDestroy = vi.fn();
    render(<ManagedSecretsView {...detail({ onDestroy })} />);

    await userEvent.click(within(screen.getByTestId("version-row-3")).getByText("Destroy"));
    // Not destroyed yet — the dialog is the gate.
    expect(onDestroy).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toMatch(/cannot be undone/i);
    // It must also rule out the recovery the user might assume exists.
    expect(dialog.textContent).toMatch(/undelete cannot bring it back/i);

    await userEvent.click(within(dialog).getByText("Destroy permanently"));
    expect(onDestroy).toHaveBeenCalledWith("DATABASE_URL", 3);
  });

  it("does not destroy when the confirmation is dismissed", async () => {
    const onDestroy = vi.fn();
    render(<ManagedSecretsView {...detail({ onDestroy })} />);
    await userEvent.click(within(screen.getByTestId("version-row-3")).getByText("Destroy"));
    await userEvent.click(within(await screen.findByRole("dialog")).getByText("Cancel"));
    expect(onDestroy).not.toHaveBeenCalled();
  });

  it("exposes no mutating action at all when the surface cannot write", async () => {
    render(<ManagedSecretsView {...detail({ mode: "external" })} />);
    const history = screen.getByTestId("version-history");
    expect(within(history).queryByText("Delete")).not.toBeInTheDocument();
    expect(within(history).queryByText("Destroy")).not.toBeInTheDocument();
  });
});

describe("elevation rules", () => {
  it("builds structure without bg-muted, which inverts between light and dark", () => {
    // --muted moves in opposite directions across the schemes, so a muted
    // surface recesses in one mode and lifts in the other. Interaction state
    // only, never structure.
    const { container } = render(<ManagedSecretsView {...props()} />);
    expect(container.querySelector('[class*="bg-muted"]')).toBeNull();
  });
});
