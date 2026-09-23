// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/secrets.
 *
 * It owns fetching and mutation state; ManagedSecretsView owns presentation and
 * takes pure props, the same split TopologyView uses — which is what lets the
 * visual contract be tested by handing it report objects instead of standing up
 * a query client and two transports.
 *
 * ── TWO BACKENDS, ONE SCREEN ────────────────────────────────────────────────
 *
 * This page joins two independent sources, and they fail independently:
 *
 *   useForgeSecrets      the DAEMON, via forge. Which secrets this environment
 *                        DECLARES, and which provider it uses.
 *   useManagedSecrets    CONTROL-PLANE, via Connect. What the managed store
 *                        actually holds.
 *
 * Neither is waited on for the other. A daemon that is down still leaves the
 * store's rows renderable, and a control plane without a managed store still
 * leaves forge's declarations renderable — see joinSecretRows, which is total
 * over both being absent. The alternative, gating the screen on both, means one
 * backend's outage blanks a page that could have shown half the truth.
 *
 * The project-level outcomes (not-a-forge-project, forge-too-old) still short
 * circuit, because those are facts about the project rather than about one
 * store, and they are rendered by the SAME components the other forge screens
 * use so the wording cannot drift.
 *
 * ── WHAT THIS PAGE DOES NOT DO ──────────────────────────────────────────────
 *
 * It never stringifies a report, never puts a response body in an error
 * message, and holds no secret value in state — the value lives inside
 * SetSecretModal's form state for the duration of one submit and is cleared on
 * close. There is no reveal because there is no RPC that could serve one.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";
import {
  useDeleteManagedSecret,
  useDestroyManagedSecret,
  useForgeSecrets,
  useForgeTopology,
  useManagedSecretVersions,
  useManagedSecrets,
  useSetManagedSecret,
  useUndeleteManagedSecret,
} from "@/hooks/forge-queries";
import { environments } from "@/services/forge/topology";
import { surfaceMode, type SecretSurfaceRow } from "@/services/forge/secretSurface";
import PageHeader from "@/components/forge-ui/page_header";

import { EnvTabs } from "../EnvTabs";
import { ForgeMalformed, ForgeUnreachable, ForgeUnsupported, NotForgeProject } from "../ForgeStates";
import { ManagedSecretsView } from "./ManagedSecretsView";
import { SetSecretModal } from "./SetSecretModal";

export function ForgeSecretsPage() {
  const navigate = useNavigate();
  const {
    project: projectParam,
    env: envParam,
    secret: secretParam,
  } = useSearch({ from: "/_authenticated/_forge/forge/secrets" });
  const currentProject = useProjectStore((state) => state.currentProject);

  const projectId = projectParam ?? currentProject?.id ?? null;
  const topology = useForgeTopology(projectId);
  const selectedEnv = envParam ?? null;

  const setSearch = useCallback(
    (patch: Record<string, unknown>) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }),
        replace: true,
      });
    },
    [navigate]
  );

  const selectEnv = useCallback(
    (env: string) => {
      // Changing environment clears the selected secret: the same NAME in a
      // different env is a different secret with a different history, and
      // carrying the param across would silently show one env's detail under
      // another env's heading.
      setSearch({ env, secret: undefined });
    },
    [setSearch]
  );

  const selectSecret = useCallback(
    (name: string | null) => setSearch({ secret: name ?? undefined }),
    [setSearch]
  );

  const envNames = useMemo(
    () =>
      topology.data?.kind === "report"
        ? environments(topology.data.report).map((env) => env.env)
        : [],
    [topology.data]
  );

  // Settle on the first environment once the list is known, and re-settle if
  // the selection disappears. Nothing is guessed before the list arrives.
  useEffect(() => {
    if (envNames.length === 0) return;
    if (selectedEnv && envNames.includes(selectedEnv)) return;
    selectEnv(envNames[0]);
  }, [envNames, selectedEnv, selectEnv]);

  const secrets = useForgeSecrets(projectId, selectedEnv);
  const managed = useManagedSecrets(projectId, selectedEnv);
  const versions = useManagedSecretVersions(projectId, selectedEnv, secretParam ?? null);

  const setMutation = useSetManagedSecret(projectId, selectedEnv);
  const deleteMutation = useDeleteManagedSecret(projectId, selectedEnv);
  const undeleteMutation = useUndeleteManagedSecret(projectId, selectedEnv);
  const destroyMutation = useDestroyManagedSecret(projectId, selectedEnv);

  /**
   * Modal state. `null` closed; `{ existing: null }` create; `{ existing }` set
   * a new version. The discriminator is what drives cas — see SetSecretModal.
   */
  const [modal, setModal] = useState<{
    existing: { name: string; currentVersion: number } | null;
  } | null>(null);

  const report = secrets.data?.kind === "report" ? secrets.data.report : null;
  const mode = surfaceMode(report, managed.data?.availability === "available");

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

  /** Which version is mid-mutation, so its row can show pending state. */
  const pendingVersion =
    (deleteMutation.isPending && deleteMutation.variables?.versions?.[0]) ||
    (undeleteMutation.isPending && undeleteMutation.variables?.versions?.[0]) ||
    (destroyMutation.isPending && destroyMutation.variables?.versions?.[0]) ||
    null;

  // Project-level outcomes: facts about the project, not about one store.
  const projectLevel = secrets.data ?? topology.data;
  const header = (
    <PageHeader
      title="Secrets"
      subtitle="What each environment declares, what the store holds, and every version of it. Values are write-only — nothing here can read one back."
    />
  );

  if (projectLevel?.kind === "not-forge-project") {
    return (
      <div className="space-y-6">
        {header}
        <NotForgeProject projectName={currentProject?.name} />
      </div>
    );
  }
  if (projectLevel?.kind === "unsupported") {
    return (
      <div className="space-y-6">
        {header}
        <ForgeUnsupported meta={projectLevel.meta} />
      </div>
    );
  }

  let body: React.ReactNode;

  if (!selectedEnv) {
    body =
      topology.isLoading || envNames.length > 0 ? (
        <Panel testId="secrets-no-env">
          Select an environment to see which secrets it declares.
        </Panel>
      ) : (
        <Panel testId="secrets-no-envs">This forge project declares no environments yet.</Panel>
      );
  } else if (secrets.data?.kind === "unreachable") {
    // forge's side is unreachable. The managed store may still be fine, but
    // without the declaration report there is no way to tell a declared secret
    // from an orphan, and inventing that distinction would be worse than
    // saying the daemon is down.
    body = <ForgeUnreachable meta={secrets.data.meta} />;
  } else if (secrets.data?.kind === "malformed") {
    body = <ForgeMalformed meta={secrets.data.meta} />;
  } else {
    body = (
      <ManagedSecretsView
        env={selectedEnv}
        mode={mode}
        availability={managed.data?.availability ?? "unreachable"}
        report={report}
        managed={managed.data?.secrets ?? []}
        isLoading={(secrets.isLoading || managed.isLoading) && !secrets.data}
        selectedName={secretParam ?? null}
        onSelect={selectSecret}
        versions={versions.data?.versions ?? []}
        versionsLoading={versions.isLoading}
        onAdd={() => setModal({ existing: null })}
        onSet={(row: SecretSurfaceRow) =>
          setModal({
            existing: { name: row.name, currentVersion: row.summary?.currentVersion ?? 0 },
          })
        }
        onDelete={(name, version) => deleteMutation.mutate({ name, versions: [version] })}
        onUndelete={(name, version) => undeleteMutation.mutate({ name, versions: [version] })}
        onDestroy={(name, version) => destroyMutation.mutate({ name, versions: [version] })}
        pendingVersion={typeof pendingVersion === "number" ? pendingVersion : null}
      />
    );
  }

  return (
    <div className="space-y-6">
      {header}

      <EnvTabs
        envs={envNames}
        selected={selectedEnv}
        onSelect={selectEnv}
        isLoading={topology.isLoading}
      />

      {body}

      {selectedEnv && (
        <SetSecretModal
          open={modal !== null}
          onClose={closeModal}
          env={selectedEnv}
          existing={modal?.existing ?? null}
          takenNames={(managed.data?.secrets ?? []).map((s) => s.name)}
          onSubmit={handleSubmit}
          isSubmitting={setMutation.isPending}
          error={(setMutation.error as Error | null) ?? null}
        />
      )}
    </div>
  );
}

function Panel({ testId, children }: { testId: string; children: React.ReactNode }) {
  return (
    <div
      data-testid={testId}
      className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
    >
      {children}
    </div>
  );
}
