// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/experiments — the project's Forge Changes: remote config,
 * gradual rollouts and A/B tests, one row per change per environment.
 *
 * ONE PAGE PER PROJECT (owner decision, 2026-11-20), filtered by the
 * console's own scopes: the app switcher picks the app (in this console one
 * Reliant project declares one forge app), the top bar's environment control
 * picks the environment ("All" lists every environment's changes).
 *
 * The primary action is "New change". A row opens the change
 * (/experiments/:changeId, ExperimentPage), where "Who gets it?" is decided.
 *
 * People and agents act on the same records through the same ChangesService,
 * with one audit trail: an agent's change shows up here like anyone's.
 */

import { useMemo, useState, type ReactNode } from "react";
import { useParams } from "@tanstack/react-router";
import { FlaskConical, Plus } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import { ConsolePageHeader } from "../Console/severalSectionContext";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import { useChanges, useChangesOrg } from "@/hooks/changes-queries";
import type { Change } from "@/services/controlPlane/changes/changesTypes";
import { changeErrorKind, describeChangeError } from "@/services/controlPlane/changes/errors";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import { BetaTag } from "./BetaTag";
import { ChangeDetail } from "./ChangeDetail";
import { ageOf, deliveryPhrase, MODE_LABELS } from "./changeModel";
import { NewChangeDialog, type NewChangeEnv } from "./NewChangeDialog";

function PageFrame({ children, onNew, canCreate }: { children: ReactNode; onNew?: () => void; canCreate: boolean }) {
  return (
    <div className="min-w-0 space-y-4" data-testid="console-experiments">
      <ConsolePageHeader
        density="compact"
        title="Experiments"
        titleAccessory={<BetaTag />}
        subtitle="Change what your app does for some or all of your users without a deploy, and compare versions to see which works better."
        className="mb-0"
        actions={
          onNew
            ? [{ label: "New change", onClick: onNew, variant: "primary", disabled: !canCreate, icon: <Plus className="h-4 w-4" aria-hidden="true" /> }]
            : undefined
        }
      />
      {children}
    </div>
  );
}

/** The scoped environments that live on the control plane: only those can hold a change. */
export function useLiveEnvs(): NewChangeEnv[] {
  const { roster } = useForgeConsole();
  return useMemo(
    () => roster.envs.filter((candidate) => !!candidate.live?.id).map((candidate) => ({ id: candidate.live!.id, name: candidate.name })),
    [roster.envs]
  );
}

export function ExperimentsPage() {
  const { forgeProject, env, roster, href } = useForgeConsole();
  // The demo console: no writes, so no new change (obsReadOnly.tsx).
  const lock = useObsReadOnly();
  const org = useChangesOrg();
  const envs = useLiveEnvs();
  const scoped = env === ENV_ALL ? null : (envs.find((candidate) => candidate.name === env) ?? null);
  const envNameById = useMemo(() => new Map(envs.map((candidate) => [candidate.id, candidate.name])), [envs]);
  // A named scope with no live environment lists nothing rather than everything.
  const unknownScope = env !== ENV_ALL && !scoped && !roster.isLoading;
  const list = useChanges(unknownScope ? null : org.orgId, forgeProject, scoped?.id ?? null);
  const [creating, setCreating] = useState(false);
  const changes = useMemo(() => list.data?.changes ?? [], [list.data]);
  const declaredKeys = useMemo(() => [...new Set(changes.filter((c) => c.declaration).map((c) => c.key))].sort(), [changes]);

  const frame = (body: ReactNode) => (
    <PageFrame onNew={() => setCreating(true)} canCreate={envs.length > 0 && !!org.orgId && !lock.readOnly}>
      {body}
      <NewChangeDialog
        open={creating}
        onClose={() => setCreating(false)}
        envs={envs}
        defaultEnvId={scoped?.id ?? envs[0]?.id ?? ""}
        declaredKeys={declaredKeys}
        changeHref={(changeId) => href({ kind: "experiment", changeId })}
      />
    </PageFrame>
  );

  if (org.isLoading || roster.isLoading || list.isLoading) {
    return frame(
      <Card padding="none" aria-label="Loading" aria-busy="true">
        <SkeletonLoader variant="table-row" count={4} />
      </Card>
    );
  }
  if (!org.orgId) {
    return frame(<p className="text-sm text-ink-muted">Experiments need an organization. Yours is still being set up; try again in a moment.</p>);
  }
  if (list.error) {
    const kind = changeErrorKind(list.error);
    return frame(
      <EmptyState
        icon={<FlaskConical className="h-7 w-7" aria-hidden="true" />}
        title={kind === "not-available" ? "Experiments aren't available here yet" : "Couldn't load experiments"}
        description={
          kind === "not-available"
            ? "This control plane doesn't run Forge Changes yet. Once it does, your changes appear here."
            : describeChangeError(list.error)
        }
      />
    );
  }
  if (unknownScope) {
    return frame(<p className="text-sm text-ink-muted">{`There is no deployed environment named ${env} in this project.`}</p>);
  }
  if (changes.length === 0) {
    return frame(
      <EmptyState
        icon={<FlaskConical className="h-7 w-7" aria-hidden="true" />}
        title="No changes yet"
        description={
          envs.length === 0
            ? "Deploy an environment first: a change lives in one environment and reaches the apps running there."
            : "A change lets you turn something on for everyone, for a share of your users, or compare two versions — without a deploy. Declare it in changes.yaml, then decide who gets it here."
        }
        actionLabel={envs.length > 0 && !lock.readOnly ? "New change" : undefined}
        onAction={() => setCreating(true)}
      />
    );
  }

  return frame(
    <>
      <ChangeTable changes={changes} showEnv={!scoped} envNameById={envNameById} href={(c) => href({ kind: "experiment", changeId: c.id })} />
      {list.data?.truncated && (
        <p className="mt-3 text-meta text-ink-muted" data-testid="changes-truncated">
          {`Showing the first ${changes.length} changes. Pick an environment in the top bar to narrow the list.`}
        </p>
      )}
    </>
  );
}

function ChangeTable({ changes, showEnv, envNameById, href }: { changes: Change[]; showEnv: boolean; envNameById: Map<string, string>; href: (c: Change) => string }) {
  const rows = [...changes].sort((a, b) => a.key.localeCompare(b.key) || (envNameById.get(a.environmentId) ?? "").localeCompare(envNameById.get(b.environmentId) ?? ""));
  return (
    <Card padding="none">
      <table className="w-full text-left text-ui" aria-label="Changes">
        <thead className="text-meta text-ink-muted">
          <tr className="border-b border-border">
            <th scope="col" className="px-4 py-2 font-medium">Change</th>
            {showEnv && <th scope="col" className="px-4 py-2 font-medium">Environment</th>}
            <th scope="col" className="px-4 py-2 font-medium">Mode</th>
            <th scope="col" className="px-4 py-2 font-medium">Who gets it</th>
            <th scope="col" className="px-4 py-2 font-medium">Running for</th>
            <th scope="col" className="px-4 py-2 font-medium">Release</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((change) => (
            <tr key={change.id} className="border-b border-border-subtle last:border-0 hover:bg-ink/5" data-testid={`change-row-${change.key}`}>
              <td className="px-4 py-2.5">
                <Link href={href(change)} className="font-mono text-ink underline-offset-2 hover:underline">
                  {change.key}
                </Link>
                {change.description && <div className="truncate text-meta text-ink-muted">{change.description}</div>}
              </td>
              {showEnv && <td className="px-4 py-2.5 text-ink-muted">{envNameById.get(change.environmentId) ?? change.environmentId}</td>}
              <td className="px-4 py-2.5">
                <Tag>{MODE_LABELS[change.mode]}</Tag>
              </td>
              <td className="px-4 py-2.5 text-ink">{deliveryPhrase(change)}</td>
              <td className="px-4 py-2.5 tabular-nums text-ink-muted">{change.currentRun ? ageOf(change.currentRun.startedAt) : "—"}</td>
              <td className="px-4 py-2.5">
                {change.declaration ? <span className="text-ink-muted">Declared</span> : <Tag title="The release running in this environment doesn't declare this key yet">Not declared yet</Tag>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

/** /forge/p/:project/experiments/:changeId — one change. */
export function ExperimentPage() {
  const { changeId } = useParams({ strict: false }) as { changeId?: string };
  const { href } = useForgeConsole();
  const envs = useLiveEnvs();
  return <ChangeDetail changeId={changeId ?? ""} envs={envs} backHref={href({ kind: "experiments" })} />;
}
