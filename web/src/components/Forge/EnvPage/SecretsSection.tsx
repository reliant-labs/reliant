// Copyright (c) 2025 Reliant Labs

/**
 * One environment's SECRETS — the managed store, and nothing else.
 *
 * There used to be a whole Secrets page that also rendered forge's file store
 * and external providers. That UX is gone: a web form cannot reach a
 * gitignored file on someone's laptop, and an external secret manager is
 * provisioned out of band. The managed store is the one place Reliant holds
 * secrets, so it is the one this section manages.
 *
 * ── WHERE THE ANSWER COMES FROM ─────────────────────────────────────────────
 *
 * The store is keyed on the environment's CONTROL-PLANE id
 * (managedTargetFor). The control plane's own env row supplies it directly —
 * for a Reliant cloud env AND for a LOCAL env, whose secrets `forge env up`
 * pulls — so this section works with no daemon at all. Only an env the
 * control plane has no row for falls back to forge's report, which can name
 * an id for a hosted env forge has ensured.
 *
 * forge's DECLARATION report is an enrichment, not a gate: when the daemon
 * answers, it adds the rows for secrets a workload asks for that the store
 * has never held ("declared but never set" — the row that breaks a deploy).
 * When it does not, the store's rows still render.
 *
 * Nothing here holds a value. The value lives inside SetSecretModal's form
 * state for one submit and is cleared on close; there is no reveal because no
 * RPC this app calls could serve one.
 */

import { useCallback, useMemo, useState } from "react";

import {
  useDeleteManagedSecret,
  useDestroyManagedSecret,
  useForgeSecrets,
  useManagedSecretVersions,
  useManagedSecrets,
  useSetManagedSecret,
  useSetManagedSecretEnsuringEnvironment,
  useUndeleteManagedSecret,
} from "@/hooks/forge-queries";
import { isCloudLocal, managedTargetFor, type ForgeEnvSummary } from "@/services/forge/environments";
import { surfaceMode, type SecretSurfaceRow } from "@/services/forge/secretSurface";

import { ManagedSecretsView } from "../Secrets/ManagedSecretsView";
import { SetSecretModal } from "../Secrets/SetSecretModal";

export interface SecretsSectionProps {
  projectId: string | null;
  summary: ForgeEnvSummary;
  /** Whether the daemon can be asked for forge's declarations. */
  daemonAvailable: boolean;
  /**
   * The FORGE project name (forge.yaml `name`) — the project half of a
   * control-plane environment's (org, project, name) identity. Needed to
   * create the environment row for an env that has never been deployed.
   */
  forgeProject: string | null;
  selectedSecret: string | null;
  onSelectSecret: (name: string | null) => void;
}

export function SecretsSection({
  projectId,
  summary,
  daemonAvailable,
  forgeProject,
  selectedSecret,
  onSelectSecret,
}: SecretsSectionProps) {
  const env = summary.name;
  const target = useMemo(() => managedTargetFor(summary), [summary]);
  const environmentId = target.kind === "lookup" ? target.environmentId : null;

  const managed = useManagedSecrets(projectId, env, target);
  // Declarations are forge's to report, so only asked for when a daemon can
  // answer — and never waited on.
  const declarations = useForgeSecrets(daemonAvailable ? projectId : null, env);
  const versions = useManagedSecretVersions(projectId, env, environmentId, selectedSecret);

  // forge could not say where this env's secrets live (no report, or no
  // destination it could resolve) and the control plane holds no row.
  const providerUnknown = target.kind === "none" && target.availability === "provider-unknown";

  // What it would take to CREATE this environment's row: forge's project name
  // and the kind forge derived for it. The row is addressed by (org, project,
  // name) and its kind is immutable, so neither is ever guessed.
  //
  // When forge could not report at all, the kind is left empty and the SET
  // FORM asks the user for it (SetSecretModal askEnvironmentKind) — that is
  // what keeps a value settable before the first deploy without a daemon.
  // The project name has no such fallback: without it the write would land
  // in a different environment, so the write is not offered.
  const ensureFacts = useMemo(() => {
    const project = (forgeProject ?? "").trim();
    const kind = (summary.forge?.control_plane_kind ?? "").trim();
    if (project === "") return null;
    if (kind === "" && !providerUnknown) return null;
    return { project, name: env, controlPlaneKind: kind };
  }, [forgeProject, summary.forge?.control_plane_kind, env, providerUnknown]);
  const askEnvironmentKind = providerUnknown && ensureFacts?.controlPlaneKind === "";

  const setMutation = useSetManagedSecret(projectId, env, environmentId);
  const ensureSetMutation = useSetManagedSecretEnsuringEnvironment(
    projectId,
    env,
    environmentId,
    ensureFacts
  );
  const deleteMutation = useDeleteManagedSecret(projectId, env, environmentId);
  const undeleteMutation = useUndeleteManagedSecret(projectId, env, environmentId);
  const destroyMutation = useDestroyManagedSecret(projectId, env, environmentId);

  const [modal, setModal] = useState<{ existing: { name: string; currentVersion: number } | null } | null>(
    null
  );

  const report = declarations.data?.kind === "report" ? declarations.data.report : null;
  const storeAvailable = managed.data?.availability === "available";
  const availability =
    managed.data?.availability ?? (target.kind === "none" ? target.availability : "unreachable");

  // A hosted env the control plane has never seen is WRITABLE even though it
  // is not readable: the only thing missing is its deploy_environments row,
  // and the write path creates that first (setSecretEnsuringEnvironment).
  // Writing is what a user comes to this screen to do before their first
  // deploy, and gating it on having already deployed is the chicken-and-egg
  // this surface used to hand them.
  //
  // The same holds when forge could not report the env at all: the values go
  // to the managed store ahead of the first deploy, which reads them from
  // there when the env's config declares HostedSecrets.
  const canEnsure = (availability === "not-ensured" || providerUnknown) && ensureFacts !== null;

  // The mode decides whether this surface can write. A control plane env row
  // IS the managed store's provider, whatever an older forge calls it.
  const mode = canEnsure
    ? "managed"
    : summary.cloud
      ? storeAvailable
        ? "managed"
        : "managed-remote"
      : surfaceMode(report, storeAvailable);

  // The write path: ensure-then-set when the environment has no row yet,
  // the plain set when it does. One `activeSet` from here down, so the modal
  // renders one pending state and one error whichever path is in play.
  const activeSet = canEnsure ? ensureSetMutation : setMutation;

  const closeModal = useCallback(() => {
    setModal(null);
    activeSet.reset();
  }, [activeSet]);

  const handleSubmit = useCallback(
    async (args: { name: string; value: string; cas?: number; controlPlaneKind?: string }) => {
      await activeSet.mutateAsync(args).then(
        () => closeModal(),
        // Swallow: the modal renders the mutation's error itself, and an
        // unhandled rejection here would surface the value-carrying request in
        // a console trace.
        () => undefined
      );
    },
    [activeSet, closeModal]
  );

  const pendingVersion =
    (deleteMutation.isPending && deleteMutation.variables?.versions?.[0]) ||
    (undeleteMutation.isPending && undeleteMutation.variables?.versions?.[0]) ||
    (destroyMutation.isPending && destroyMutation.variables?.versions?.[0]) ||
    null;

  // Not in the managed store at all: say so in one line, with no file-store UI.
  // ONLY when forge actually named a non-hosted destination — "forge could
  // not say" is provider-unknown, rendered below, and never this sentence.
  if (target.kind === "none" && target.availability === "not-hosted") {
    return (
      <p
        data-testid="secrets-not-managed"
        className="rounded-lg border border-dashed border-border px-4 py-3 text-sm text-muted-foreground"
      >
        This environment&apos;s secrets are not in the managed store — its forge config names a
        different secret provider, so Reliant does not hold them.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      {providerUnknown && ensureFacts === null && (
        // The one fact the write cannot do without: which forge project the
        // env belongs to. Said plainly instead of offering a button that
        // would fail on submit.
        <p data-testid="secrets-provider-unknown-no-project" className="text-xs text-muted-foreground">
          Reliant does not yet know this project&apos;s forge name, so it cannot create this
          environment to hold values. Open the project once with your daemon running and it will be
          recorded.
        </p>
      )}
      {isCloudLocal(summary) && (
        <p data-testid="secrets-local-pull" className="text-xs text-muted-foreground">
          <code className="font-mono text-foreground">forge env up</code> pulls these into the
          processes it starts, in memory — they are never written to disk.
        </p>
      )}

      <ManagedSecretsView
        env={env}
        mode={mode}
        availability={availability}
        report={report}
        managed={managed.data?.secrets ?? []}
        isLoading={managed.isLoading && !managed.data}
        selectedName={selectedSecret}
        onSelect={onSelectSecret}
        versions={versions.data?.versions ?? []}
        versionsLoading={versions.isLoading}
        onAdd={() => setModal({ existing: null })}
        onSet={(row: SecretSurfaceRow) =>
          setModal({ existing: { name: row.name, currentVersion: row.summary?.currentVersion ?? 0 } })
        }
        onDelete={(name, version) => deleteMutation.mutate({ name, versions: [version] })}
        onUndelete={(name, version) => undeleteMutation.mutate({ name, versions: [version] })}
        onDestroy={(name, version) => destroyMutation.mutate({ name, versions: [version] })}
        pendingVersion={typeof pendingVersion === "number" ? pendingVersion : null}
      />

      <SetSecretModal
        open={modal !== null}
        onClose={closeModal}
        env={env}
        existing={modal?.existing ?? null}
        takenNames={(managed.data?.secrets ?? []).map((s) => s.name)}
        onSubmit={handleSubmit}
        isSubmitting={activeSet.isPending}
        error={(activeSet.error as Error | null) ?? null}
        askEnvironmentKind={canEnsure && askEnvironmentKind}
      />
    </div>
  );
}
