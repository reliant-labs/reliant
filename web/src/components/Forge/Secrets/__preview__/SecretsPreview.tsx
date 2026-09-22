// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the secrets surface. NOT a product surface.
 *
 * It exists for the same reason WorkloadInventoryPreview does: the complaint
 * this screen fixes was VISUAL — "secrets tables are fucking ugly" — so "the
 * tests pass" is not evidence that it is fixed. This route renders the REAL
 * components with a scheme and light/dark switcher, so the thing the user will
 * actually see can be looked at in every theme without standing up a daemon, a
 * control plane and an OpenBao.
 *
 * The cases below are the states that are hard to reach on demand against a
 * live stack but easy to get wrong: a destroyed version, a soft-deleted one, a
 * declared-but-never-set secret, an external provider that must offer no create
 * button, and a store that is simply not configured.
 *
 * `.forge-ui` wraps the render, exactly as ForgeLayout does in the product —
 * without it forge's `accent` resolves to reliant's muted hover tint and every
 * accent-colored forge component renders as a barely-visible grey.
 */

import { useState } from "react";

import type { ForgeSecretsReport } from "@/services/forge/secrets";
import type { ManagedSecretSummary, ManagedSecretVersion } from "@/services/forge/secretStore";
import type { SecretSurfaceMode } from "@/services/forge/secretSurface";

import { ForgeShell } from "../../ForgeShell";
import { ManagedSecretsView } from "../ManagedSecretsView";
import { SetSecretModal } from "../SetSecretModal";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

function summary(
  overrides: Partial<ManagedSecretSummary> & { name: string }
): ManagedSecretSummary {
  return {
    currentVersion: 3,
    oldestVersion: 1,
    maxVersions: 10,
    createdAt: "2026-01-14T09:12:00Z",
    updatedAt: "2026-03-02T16:40:00Z",
    currentVersionDeleted: false,
    currentVersionDestroyed: false,
    ...overrides,
  };
}

function declared(provider: string, names: string[]): ForgeSecretsReport {
  return {
    env: "prod",
    provider,
    secrets: names.map((name) => ({
      name,
      present: false,
      declared_by: [
        { workload: "admin-server", kind: "service", secret_name: "cp-secrets", secret_key: name },
      ],
    })),
  };
}

/** A populated managed store, including the two states people get wrong. */
const POPULATED: ManagedSecretSummary[] = [
  summary({ name: "ANTHROPIC_API_KEY", currentVersion: 7 }),
  summary({ name: "DATABASE_URL", currentVersion: 2 }),
  summary({ name: "OLD_WEBHOOK_TOKEN", currentVersion: 1, currentVersionDeleted: true }),
  summary({ name: "ROTATED_LEGACY_KEY", currentVersion: 4, currentVersionDestroyed: true }),
  summary({ name: "STRIPE_SECRET_KEY" }),
];

const VERSIONS: ManagedSecretVersion[] = [
  { version: 7, createdAt: "2026-03-02T16:40:00Z", destroyed: false },
  { version: 6, createdAt: "2026-02-11T11:05:00Z", deletedAt: "2026-02-20T08:00:00Z", destroyed: false },
  { version: 5, createdAt: "2026-01-30T14:22:00Z", destroyed: true },
  { version: 4, createdAt: "2026-01-14T09:12:00Z", destroyed: true },
];

interface Case {
  id: string;
  label: string;
  mode: SecretSurfaceMode;
  report: ForgeSecretsReport | null;
  managed: ManagedSecretSummary[];
  availability: "available" | "not-configured" | "no-control-plane" | "unreachable";
}

const CASES: Case[] = [
  {
    id: "managed",
    label: "managed — populated, with a deleted and a destroyed row",
    mode: "managed",
    report: declared("none", ["ANTHROPIC_API_KEY", "DATABASE_URL", "STRIPE_SECRET_KEY", "NEVER_SET"]),
    managed: POPULATED,
    availability: "available",
  },
  {
    id: "managed-empty",
    label: "managed — empty store",
    mode: "managed",
    report: declared("none", []),
    managed: [],
    availability: "available",
  },
  {
    id: "unset",
    label: "managed — everything declared, nothing set (the blocker)",
    mode: "managed",
    report: declared("none", ["ANTHROPIC_API_KEY", "DATABASE_URL", "STRIPE_SECRET_KEY"]),
    managed: [],
    availability: "available",
  },
  {
    id: "external",
    label: "external — must offer NO create button",
    mode: "external",
    report: declared("external", ["ANTHROPIC_API_KEY", "DATABASE_URL"]),
    managed: [],
    availability: "not-configured",
  },
  {
    id: "external-empty",
    label: "external — nothing declared",
    mode: "external",
    report: declared("external", []),
    managed: [],
    availability: "not-configured",
  },
  {
    id: "file",
    label: "file — local dev, read-only here",
    mode: "file",
    report: declared("file", []),
    managed: [],
    availability: "no-control-plane",
  },
  {
    id: "unreachable",
    label: "managed store unreachable",
    mode: "managed",
    report: declared("none", ["DATABASE_URL"]),
    managed: [],
    availability: "unreachable",
  },
  {
    id: "loading",
    label: "loading",
    mode: "managed",
    report: null,
    managed: [],
    availability: "available",
  },
];

export default function SecretsPreview() {
  const [scheme, setScheme] = useState<string>(
    () => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue"
  );
  const [dark, setDark] = useState<boolean>(() =>
    document.documentElement.classList.contains("dark")
  );
  const [caseId, setCaseId] = useState<string>(CASES[0].id);
  const [selected, setSelected] = useState<string | null>(null);
  const [modalOpen, setModalOpen] = useState(false);

  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  /*
   * The preview renders inside the REAL shell (ForgeShell — the same component
   * ForgeLayout renders), not bare.
   *
   * This is not decoration. A harness that drew the screen without the sidebar
   * looked exactly like the product to anyone reading a screenshot, and was
   * not — which cost two review cycles here, both spent asking where the left
   * nav had gone when it was present in the app all along. Rendering the real
   * chrome makes that class of mistake impossible rather than merely
   * discouraged.
   */
  return (
    <ForgeShell
      activePath="/forge/secrets"
      headerContent={
        <div className="flex flex-wrap items-center gap-3 px-4">
        <select
          data-testid="preview-case"
          value={caseId}
          onChange={(e) => {
            setCaseId(e.target.value);
            setSelected(null);
          }}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {CASES.map((c) => (
            <option key={c.id} value={c.id}>
              {c.label}
            </option>
          ))}
        </select>
        <select
          data-testid="preview-scheme"
          value={scheme}
          onChange={(e) => applyScheme(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {SCHEMES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <button
          type="button"
          data-testid="preview-dark"
          onClick={() => applyDark(!dark)}
          className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
        >
          {dark ? "Dark" : "Light"}
        </button>
        <button
          type="button"
          data-testid="preview-modal"
          onClick={() => setModalOpen(true)}
          className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
        >
          Open set-secret modal
        </button>
        </div>
      }
    >
      <div className="h-full overflow-y-auto p-6">
        <ManagedSecretsView
          env="prod"
          mode={active.mode}
          availability={active.availability}
          report={active.report}
          managed={active.managed}
          isLoading={active.id === "loading"}
          selectedName={selected}
          onSelect={setSelected}
          versions={VERSIONS}
          versionsLoading={false}
          onAdd={() => setModalOpen(true)}
          onSet={() => setModalOpen(true)}
          onDelete={() => undefined}
          onUndelete={() => undefined}
          onDestroy={() => undefined}
          pendingVersion={null}
        />

        <SetSecretModal
          open={modalOpen}
          onClose={() => setModalOpen(false)}
          env="prod"
          existing={null}
          takenNames={active.managed.map((s) => s.name)}
          onSubmit={async () => undefined}
          isSubmitting={false}
          error={null}
        />
      </div>
    </ForgeShell>
  );
}
