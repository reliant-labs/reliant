// Copyright (c) 2025 Reliant Labs

/**
 * ONE ENVIRONMENT'S SECRETS, WITH NO DAEMON ANYWHERE.
 *
 * ── WHAT CHANGED, AND WHY IT MATTERS MORE HERE THAN ANYWHERE ────────────────
 *
 * The "declared but never set" row — a workload asks for a secret and nobody
 * has ever written one — is the row a user comes to this screen to find: it is
 * the one that stops a deploy. It used to come from `forge.secret_list` on the
 * daemon, which made the single most valuable row on the page disappear
 * whenever a laptop slept.
 *
 * It now comes from `declared_shape.secrets` on the environment's
 * control-plane row (design §10 state 1). `forge env build`, `forge env
 * deploy` and Register all record that shape, so the names are on the control
 * plane from the moment anything has touched the environment — and since the
 * managed store is also the control plane, BOTH halves of the join are one
 * round trip away with the user's session. The shape carries secret names and
 * providers only (F-13); there is no field in it that could hold a value.
 *
 * ── THE THREE STATES OF §10 ─────────────────────────────────────────────────
 *
 *   1. a row exists      answered entirely from the row: its kind, and
 *                        declared_shape.secrets[].provider. Needs no daemon,
 *                        and this is the overwhelmingly common case because a
 *                        row exists as soon as anything has been built or
 *                        registered. Values are settable.
 *   2. no row, Preview   the Preview tab registers the environment from
 *                        forge's render, which produces state 1. Not this
 *                        component's job.
 *   3. no row at all     the form is DISABLED with the remedy. Note the
 *                        asymmetry and keep it: the form is disabled because
 *                        the VALUE IS UNKNOWN (an environment's kind is
 *                        immutable and nothing has stated it), not because the
 *                        daemon is offline. An environment that has been built
 *                        once never returns to state 3, however often the
 *                        daemon comes and goes.
 *
 * Nothing here holds a value. The value lives inside SetSecretModal's form
 * state for one submit and is cleared on close; there is no reveal, because no
 * RPC this app calls could serve one.
 */

import { useCallback, useMemo, useState } from "react";

import {
  useDeleteManagedSecret,
  useDestroyManagedSecret,
  useManagedSecretVersions,
  useManagedSecrets,
  useSetManagedSecret,
  useUndeleteManagedSecret,
} from "@/hooks/forge-queries";
import type { LiveEnv } from "@/services/forge/live";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { normalizeEndpoint, type ManagedStoreTarget } from "@/services/forge/secretStore";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";
import type { SecretSurfaceRow } from "@/services/forge/secretSurface";

import { ManagedSecretsView } from "../Secrets/ManagedSecretsView";
import { SetSecretModal } from "../Secrets/SetSecretModal";

export interface LiveSecretsSectionProps {
  projectId: string | null;
  /** Null means state 3: no control-plane row for this environment. */
  env: LiveEnv | null;
  /** The environment name from the route — needed for state 3's copy. */
  envName?: string;
  forgeProject: string | null;
  selectedSecret: string | null;
  onSelectSecret: (name: string | null) => void;
}

export function LiveSecretsSection({
  projectId,
  env,
  envName,
  selectedSecret,
  onSelectSecret,
}: LiveSecretsSectionProps) {
  const name = env?.name ?? envName ?? "";

  // The store is keyed on the environment's control-plane id, which the row
  // supplies directly. No forge report, no endpoint comparison, no guessed id:
  // the id was issued by THIS console's control plane because the row came
  // from it.
  const target: ManagedStoreTarget = useMemo(() => {
    if (!CONTROL_PLANE_API_URL) return { kind: "none", availability: "no-control-plane" };
    if (!env) {
      // State 3. `provider-unknown` is the honest availability: nothing has
      // stated where this environment's secrets live, which is a different
      // claim from "they are not in the managed store".
      return { kind: "none", availability: "provider-unknown" };
    }
    return {
      kind: "lookup",
      environmentId: env.id,
      endpoint: normalizeEndpoint(CONTROL_PLANE_API_URL),
    };
  }, [env]);

  const environmentId = target.kind === "lookup" ? target.environmentId : null;

  const managed = useManagedSecrets(projectId, name, target);
  const versions = useManagedSecretVersions(projectId, name, environmentId, selectedSecret);

  const setMutation = useSetManagedSecret(projectId, name, environmentId);
  const deleteMutation = useDeleteManagedSecret(projectId, name, environmentId);
  const undeleteMutation = useUndeleteManagedSecret(projectId, name, environmentId);
  const destroyMutation = useDestroyManagedSecret(projectId, name, environmentId);

  const [modal, setModal] = useState<{ existing: { name: string; currentVersion: number } | null } | null>(
    null
  );

  /**
   * The declared names, in the shape ManagedSecretsView's join already reads.
   *
   * Adapting the control plane's declared shape into forge's report type
   * rather than reimplementing the join: the join IS the screen's most
   * valuable logic (declared-unset versus orphan versus declared-unread, in
   * services/forge/secretSurface.ts), it is already tested, and a second
   * implementation reading a different source would be free to disagree with
   * it about which row is a blocker.
   *
   * `provider` comes from the shape's own secrets. forge's providers and the
   * shape's are the same vocabulary, so `hosted` means the managed store here
   * exactly as it does in forge's report.
   */
  const declared: ForgeSecretsReport | null = useMemo(() => {
    const shape = env?.declaredShape;
    if (!shape) return null;
    return {
      env: env?.name,
      provider: shape.secrets[0]?.provider || "hosted",
      // Presence is NOT claimed from the shape. A declaration says a workload
      // asks for the secret; it says nothing about whether a value exists, and
      // the managed store is the only thing that can answer that. Leaving
      // `verifiable` unset keeps the join reading the store rather than
      // inventing a presence.
      secrets: shape.secrets.map((secret) => ({
        name: secret.name,
        declared_by: secret.declaredBy.map((workload) => ({ workload })),
      })),
    };
  }, [env?.declaredShape, env?.name]);

  const availability =
    managed.data?.availability ?? (target.kind === "none" ? target.availability : "unreachable");
  const storeAvailable = availability === "available";

  const closeModal = useCallback(() => {
    setModal(null);
    setMutation.reset();
  }, [setMutation]);

  const handleSubmit = useCallback(
    async (args: { name: string; value: string; cas?: number }) => {
      await setMutation.mutateAsync(args).then(
        () => closeModal(),
        // Swallowed: the modal renders the mutation's error itself, and an
        // unhandled rejection here would surface the value-carrying request
        // in a console trace.
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

  // ── STATE 3: no row. Disabled, with the remedy, and no guess. ──
  //
  // The form does not ask the user for the kind and does not offer to create
  // the environment from a value the user typed. An immutable field chosen by
  // a human under a form is exactly what #353 had to guard against afterwards
  // with a FailedPrecondition; removing the question is safer than keeping it
  // as a fallback.
  if (!env) {
    return (
      <p
        data-testid="live-secrets-not-built"
        className="rounded-lg border border-dashed border-border px-4 py-3 text-sm text-muted-foreground"
      >
        <span className="font-mono text-foreground">{name}</span> hasn&apos;t been built yet. Run{" "}
        <code className="font-mono text-foreground">forge env build {name}</code>, or open Preview
        with your daemon online.
      </p>
    );
  }

  return (
    <div className="space-y-3" data-testid="live-secrets">
      {env.kind === "local" && (
        <p data-testid="secrets-local-pull" className="text-xs text-muted-foreground">
          <code className="font-mono text-foreground">forge env up</code> pulls these into the
          processes it starts, in memory — they are never written to disk.
        </p>
      )}

      <ManagedSecretsView
        env={env.name}
        // A control-plane env row IS the managed store's provider: the row's
        // existence is what makes the store addressable, so the surface is
        // `managed` when the store answered and read-only when it did not.
        mode={storeAvailable ? "managed" : "managed-remote"}
        availability={availability}
        report={declared}
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
        onDelete={(secret, version) => deleteMutation.mutate({ name: secret, versions: [version] })}
        onUndelete={(secret, version) => undeleteMutation.mutate({ name: secret, versions: [version] })}
        onDestroy={(secret, version) => destroyMutation.mutate({ name: secret, versions: [version] })}
        pendingVersion={typeof pendingVersion === "number" ? pendingVersion : null}
      />

      <SetSecretModal
        open={modal !== null}
        onClose={closeModal}
        env={env.name}
        existing={modal?.existing ?? null}
        takenNames={(managed.data?.secrets ?? []).map((secret) => secret.name)}
        onSubmit={handleSubmit}
        isSubmitting={setMutation.isPending}
        error={(setMutation.error as Error | null) ?? null}
        // The kind question is never asked from Live: a row exists, so the
        // kind is already recorded and immutable (§10 state 1). R4 removes the
        // prop and its fieldset outright (#353).
        askEnvironmentKind={false}
      />
    </div>
  );
}
