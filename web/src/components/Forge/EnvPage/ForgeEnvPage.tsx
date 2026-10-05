// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/env/$env — ONE ENVIRONMENT.
 *
 * ── TABS BY QUESTION, SOURCES BY TAB ────────────────────────────────────────
 *
 * The page used to be two tabs, Live and Preview: the daemon boundary drawn
 * on screen. The tabs now follow what a reader asks (envTabs.ts), and the
 * boundary is held per tab instead:
 *
 *   Overview · Releases · Secrets   the control plane only (design §8.0,
 *                                   O-14). Render with the daemon asleep.
 *   Running                         what `forge env up` runs on the daemon's
 *                                   machine — a LOCAL env's first question.
 *   Changes · Checks                read the user's checkout via the daemon.
 *
 * ── WHEN THIS PAGE ASKS THE DAEMON WITHOUT A TAB ASKING ─────────────────────
 *
 * Only to learn what an environment IS when the control plane cannot say:
 *
 *   - the env's status (forge's `runtime.lifecycle`), when the control plane
 *     has no row or records it as local — that is how dev is known to be a
 *     local env that runs from the working tree rather than one that was
 *     "never built";
 *   - forge's topology, when the control plane has no row — for the release
 *     forge's own ledger has bound, and the Register action.
 *
 * An environment the control plane records as deployed — persistent, preview
 * or self-managed — makes ZERO daemon calls until a daemon tab is opened
 * (ForgeEnvPage.liveNoDaemon.test.tsx).
 *
 * ── HEADER ACTIONS ──────────────────────────────────────────────────────────
 *
 * Promote and Deploy sit in the header on every tab of a deployed env, and
 * are disabled with a reason rather than hidden (EnvPageHeader).
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { cn } from "@/lib/utils";
import {
  useCloudEnvStatus,
  useCloudPromotions,
  useForgeEnvStatus,
  useForgeTopology,
  useLiveConvergences,
  useLiveView,
} from "@/hooks/forge-queries";
import { daemonSideOf, resolveForgeProjectName } from "@/services/forge/environments";
import { isPlacedKind } from "@/services/forge/live";
import { lifecycleOf } from "@/services/forge/roster";
import { isLocalLifecycle } from "@/services/forge/status";
import { environments } from "@/services/forge/topology";
import { useProjectStore, type Project } from "@/store/projectStore";

import { DeployDialog } from "../Deploy/DeployDialog";
import { PromoteDialog } from "../Promote/PromoteDialog";
import { CloudNotice } from "../SourceNotices";
import { EnvLifecycleControls } from "./EnvLifecycleControls";
import { EnvPageHeader } from "./EnvPageHeader";
import {
  ChangesTab,
  ChecksTab,
  OverviewTab,
  ReleasesTab,
  RunningTab,
  SecretsTab,
} from "./EnvTabPanels";
import { resolveTab, tabParam, tabsFor, type EnvTab } from "./envTabs";

export function ForgeEnvPage() {
  const navigate = useNavigate();
  const { env: envName } = useParams({ from: "/_authenticated/_forge/forge/env/$env" });
  const {
    project: projectParam,
    secret: secretParam,
    tab: tabSearch,
  } = useSearch({ from: "/_authenticated/_forge/forge/env/$env" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const persistedName = useProjectStore((state) => persistedForgeProjectName(state, projectParam ?? currentProject?.id));
  const projectId = projectParam ?? currentProject?.id ?? null;

  // ── THE BACKEND: the record of this environment. ──
  // The join key is the forge project name Reliant persisted on the project
  // row, so this needs no daemon.
  const persistedOnly = resolveForgeProjectName(persistedName, undefined);
  const liveFirst = useLiveView(persistedOnly.name);
  const liveEnvFromPersisted = useMemo(
    () => (liveFirst.data?.envs ?? []).find((candidate) => candidate.name === envName) ?? null,
    [liveFirst.data, envName]
  );

  // ── WHAT THIS ENVIRONMENT IS, when the backend cannot say. ──
  const backendSettled = !persistedOnly.name || !liveFirst.isLoading;
  const backendKnowsDeployed =
    !!liveEnvFromPersisted && liveEnvFromPersisted.kind !== "local" && liveEnvFromPersisted.kind !== "unknown";
  const askDaemonForIdentity = backendSettled && !backendKnowsDeployed;

  const [requestedTab, setRequestedTab] = useState<EnvTab | null>(null);
  // The tab the reader asked for — a click (state) wins over the URL, which a
  // replace-navigation updates a beat later.
  const askedTab = requestedTab ?? tabSearch;
  const tabWantsDaemon =
    askedTab === "changes" || askedTab === "checks" || askedTab === "running" || askedTab === "preview";

  // A header action is a request for the daemon too: Promote and Deploy are
  // planned by forge on the user's checkout.
  const [pendingAction, setPendingAction] = useState<"promote" | "deploy" | null>(null);
  const [actionAsked, setActionAsked] = useState(false);
  const daemonWanted = askDaemonForIdentity || tabWantsDaemon || actionAsked;

  const topology = useForgeTopology(projectId, { enabled: daemonWanted });
  // A cached answer counts (the sidebar may have asked); an unasked, uncached
  // daemon is "not asked yet", never "offline".
  const daemonAsked = daemonWanted || topology.data !== undefined || topology.error !== null;
  const daemon = daemonSideOf(topology.data, topology.error);

  // A topology report can name the project when nothing was persisted yet.
  const projectName = resolveForgeProjectName(persistedName, topology.data);
  const live = useLiveView(projectName.name);
  const liveEnv = useMemo(
    () => (live.data?.envs ?? []).find((candidate) => candidate.name === envName) ?? null,
    [live.data, envName]
  );

  const forgeEnv = useMemo(() => {
    const report = topology.data?.kind === "report" ? topology.data.report : null;
    return report ? (environments(report).find((candidate) => candidate.env === envName) ?? null) : null;
  }, [topology.data, envName]);

  const envStatus = useForgeEnvStatus(
    askDaemonForIdentity || tabWantsDaemon ? projectId : null,
    envName
  );
  const envStatusReport = envStatus.data?.kind === "report" ? envStatus.data.report : null;
  const lifecycle = lifecycleOf(liveEnv, forgeEnv, isLocalLifecycle(envStatusReport));

  const tab = requestedTab && tabsFor(lifecycle).some((spec) => spec.id === requestedTab)
    ? requestedTab
    : resolveTab(tabSearch, lifecycle);

  // ── The backend's per-env reads. ──
  const placedId = liveEnv && isPlacedKind(liveEnv.kind) ? liveEnv.id : null;
  const cloudStatus = useCloudEnvStatus(placedId);
  const promotions = useCloudPromotions(liveEnv?.id ?? null);
  const convergences = useLiveConvergences(liveEnv?.id ?? null);

  const [checkoutPath, setCheckoutPath] = useState("");
  const [promoteOpen, setPromoteOpen] = useState(false);
  const [deployOpen, setDeployOpen] = useState(false);

  const selectTab = useCallback(
    (next: EnvTab) => {
      setRequestedTab(next);
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, tab: tabParam(next, lifecycle) }),
        replace: true,
      });
    },
    [navigate, lifecycle]
  );

  const selectSecret = useCallback(
    (name: string | null) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, secret: name ?? undefined }),
        replace: true,
      });
    },
    [navigate]
  );

  // ── Header actions: always present on a deployed env, disabled with a reason. ──
  const latestRelease = topology.data?.kind === "report" ? (topology.data.report.latest_release ?? null) : null;
  const currentRelease = liveEnv?.release || forgeEnv?.release || "";
  // Until something has asked the daemon there is no reason to show: the
  // buttons are enabled, and a click asks (see the effect below).
  const daemonReason = !daemonAsked
    ? null
    : daemon === "ok"
      ? null
      : daemon === "loading"
        ? "Checking your daemon…"
        : "Needs your daemon: forge plans this against your checkout.";
  const promoteReason =
    daemonReason ??
    (!daemonAsked
      ? null
      : !latestRelease
        ? "No release has been cut from this checkout yet. Run forge env build --release."
        : latestRelease === currentRelease
          ? `Already on the latest release, ${latestRelease}.`
          : null);

  const requestAction = useCallback((action: "promote" | "deploy") => {
    setActionAsked(true);
    setPendingAction(action);
  }, []);

  // Open the requested dialog once the daemon has answered — or drop the
  // request, leaving the button disabled with the reason it now knows.
  useEffect(() => {
    if (!pendingAction || daemon === "loading") return;
    if (pendingAction === "promote" && promoteReason === null && latestRelease) setPromoteOpen(true);
    if (pendingAction === "deploy" && daemonReason === null) setDeployOpen(true);
    setPendingAction(null);
  }, [pendingAction, daemon, promoteReason, daemonReason, latestRelease]);

  const tabs = tabsFor(lifecycle);
  const identityLoading = !backendSettled || (askDaemonForIdentity && envStatus.isLoading && !envStatus.data && !liveEnv);

  return (
    <div className="space-y-6" data-testid="forge-env-page" data-tab={tab} data-lifecycle={lifecycle}>
      <EnvPageHeader
        lifecycleControls={
          liveEnv && placedId ? (
            <EnvLifecycleControls
              environmentId={liveEnv.id}
              envName={envName}
              status={cloudStatus.data}
              onDeleted={() => void navigate({ to: "/forge" })}
            />
          ) : null
        }
        envName={envName}
        lifecycle={lifecycle}
        live={liveEnv}
        forgeRelease={forgeEnv?.release ?? null}
        promote={
          lifecycle === "local"
            ? null
            : {
                target: latestRelease && latestRelease !== currentRelease ? latestRelease : null,
                disabledReason: promoteReason,
                pending: pendingAction === "promote",
                onClick: () => requestAction("promote"),
              }
        }
        deploy={
          lifecycle === "local"
            ? null
            : {
                disabledReason: daemonReason,
                pending: pendingAction === "deploy",
                onClick: () => requestAction("deploy"),
              }
        }
      />

      {/* The control plane could not answer at all — a role without deploy
          read access, a build with no control plane, an outage. Said once. */}
      <CloudNotice availability={live.data?.availability} detail={live.data?.detail} />

      <div className="flex items-center gap-1 border-b border-border" role="tablist" aria-label={`${envName} views`}>
        {tabs.map((spec) => (
          <TabButton key={spec.id} active={tab === spec.id} id={spec.id} onSelect={selectTab}>
            {spec.label}
          </TabButton>
        ))}
      </div>

      <div role="tabpanel" id={`env-tab-${tab}`} aria-labelledby={`env-tab-button-${tab}`}>
        {identityLoading && (tab === "overview" || tab === "running") ? (
          <div className="space-y-4" data-testid="live-loading" aria-busy="true">
            <div className="h-28 animate-pulse rounded-lg border border-border bg-card motion-reduce:animate-none" />
            <div className="h-40 animate-pulse rounded-lg border border-border bg-card motion-reduce:animate-none" />
          </div>
        ) : tab === "overview" ? (
          <OverviewTab
            envName={envName}
            live={liveEnv}
            liveLoading={live.isLoading}
            status={cloudStatus.data}
            statusLoading={cloudStatus.isLoading}
            statusError={cloudStatus.error as Error | null}
            promotions={promotions.data}
            forgeEnv={forgeEnv}
            daemon={daemon}
            projectId={projectId}
            forgeProject={projectName.name}
            onOpenReleases={() => selectTab("releases")}
          />
        ) : tab === "running" ? (
          <RunningTab
            envName={envName}
            daemon={daemon}
            envStatus={envStatus.data}
            envStatusLoading={envStatus.isLoading}
            envStatusError={envStatus.error as Error | null}
            onRetry={() => {
              void topology.refetch();
              void envStatus.refetch();
            }}
            retrying={envStatus.isFetching}
            projectName={currentProject?.name}
          />
        ) : tab === "releases" ? (
          <ReleasesTab
            live={liveEnv}
            promotions={promotions.data}
            convergences={convergences.data}
            isLoading={promotions.isLoading}
            error={promotions.error as Error | null}
            forgeEnv={forgeEnv}
          />
        ) : tab === "secrets" ? (
          <SecretsTab
            projectId={projectId}
            live={liveEnv}
            envName={envName}
            forgeProject={projectName.name}
            selectedSecret={secretParam ?? null}
            onSelectSecret={selectSecret}
          />
        ) : tab === "changes" ? (
          <ChangesTab
            projectId={projectId}
            envName={envName}
            daemon={daemon}
            lifecycle={lifecycle}
            checkoutPath={checkoutPath}
            onCheckoutChange={setCheckoutPath}
            onDeploy={lifecycle === "local" ? undefined : () => setDeployOpen(true)}
            onRetryDaemon={() => void topology.refetch()}
          />
        ) : (
          <ChecksTab
            projectId={projectId}
            envName={envName}
            daemon={daemon}
            lifecycle={lifecycle}
            envStatus={envStatus.data}
            envStatusLoading={envStatus.isLoading}
            envStatusError={envStatus.error as Error | null}
            canVerify={(forgeEnv?.images?.length ?? 0) > 0}
            projectName={currentProject?.name}
            onRetryDaemon={() => {
              void topology.refetch();
              void envStatus.refetch();
            }}
          />
        )}
      </div>

      {promoteOpen && latestRelease && (
        <PromoteDialog
          isOpen
          onClose={() => setPromoteOpen(false)}
          projectId={projectId}
          env={envName}
          release={latestRelease}
          projectName={currentProject?.name}
        />
      )}
      {deployOpen && (
        <DeployDialog
          isOpen
          onClose={() => setDeployOpen(false)}
          projectId={projectId}
          env={envName}
          projectName={currentProject?.name}
          checkoutPath={checkoutPath}
        />
      )}
    </div>
  );
}

function TabButton({
  active,
  id,
  onSelect,
  children,
}: {
  active: boolean;
  id: EnvTab;
  onSelect: (tab: EnvTab) => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      id={`env-tab-button-${id}`}
      aria-selected={active}
      aria-controls={`env-tab-${id}`}
      data-testid={`env-tab-${id}`}
      onClick={() => onSelect(id)}
      className={cn(
        "-mb-px rounded-sm border-b-2 px-3 py-2 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        active
          ? "border-primary font-medium text-foreground"
          : "border-transparent text-muted-foreground hover:text-foreground"
      )}
    >
      {children}
    </button>
  );
}

/** The forge project name persisted on `projectId`'s row, from whichever store slot holds it. */
function persistedForgeProjectName(
  state: { projects: Project[]; currentProject: Project | null },
  projectId: string | null | undefined
): string | null {
  if (!projectId) return null;
  const row =
    state.currentProject?.id === projectId
      ? state.currentProject
      : state.projects.find((project) => project.id === projectId);
  return row?.forge_project_name ?? null;
}
