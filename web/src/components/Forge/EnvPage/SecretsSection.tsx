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
  selectedSecret: string | null;
  onSelectSecret: (name: string | null) => void;
}

export function SecretsSection({
  projectId,
  summary,
  daemonAvailable,
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

  const setMutation = useSetManagedSecret(projectId, env, environmentId);
  const deleteMutation = useDeleteManagedSecret(projectId, env, environmentId);
  const undeleteMutation = useUndeleteManagedSecret(projectId, env, environmentId);
  const destroyMutation = useDestroyManagedSecret(projectId, env, environmentId);

  const [modal, setModal] = useState<{ existing: { name: string; currentVersion: number } | null } | null>(
    null
  );

  const report = declarations.data?.kind === "report" ? declarations.data.report : null;
  const storeAvailable = managed.data?.availability === "available";
  // The mode decides whether this surface can write. A control plane env row
  // IS the managed store's provider, whatever an older forge calls it.
  const mode = summary.cloud ? (storeAvailable ? "managed" : "managed-remote") : surfaceMode(report, storeAvailable);

  const closeModal = useCallback(() => {
    setModal(null);
    setMutation.reset();
  }, [setMutation]);

  const handleSubmit = useCallback(
    async (args: { name: string; value: string; cas?: number }) => {
      await setMutation.mutateAsync(args).then(
        () => closeModal(),
        // Swallow: the modal renders the mutation's error itself, and an
        // unhandled rejection here would surface the value-carrying request in
        // a console trace.
        () => undefined
      );
    },
    [setMutation, closeModal]
  );

  const pendingVersion =
    (deleteMutation.isPending && deleteMutation.variables?.versions?.[0]) ||
    (undeleteMutation.isPending && undeleteMutation.variables?.versions?.[0]) ||
    (destroyMutation.isPending && destroyMutation.variables?.versions?.[0]) ||
    null;

  // Not in the managed store at all: say so in one line, with no file-store UI.
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
      {isCloudLocal(summary) && (
        <p data-testid="secrets-local-pull" className="text-xs text-muted-foreground">
          <code className="font-mono text-foreground">forge env up</code> pulls these into the
          processes it starts, in memory — they are never written to disk.
        </p>
      )}

      <ManagedSecretsView
        env={env}
        mode={mode}
        availability={managed.data?.availability ?? (target.kind === "none" ? target.availability : "unreachable")}
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
        isSubmitting={setMutation.isPending}
        error={(setMutation.error as Error | null) ?? null}
      />
    </div>
  );
}
