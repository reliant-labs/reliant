// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/env/$env — ONE ENVIRONMENT, IN TWO TABS.
 *
 * ── THE SPLIT IS THE DAEMON BOUNDARY (design §8.0, owner decision O-14) ─────
 *
 *   LIVE (the default)  the control plane ONLY: the declared shape, the bound
 *                       release and its provenance, the promotion history,
 *                       what is deployed, and secrets. ONE GetLiveView round
 *                       trip, from the browser, with the user's session. The
 *                       daemon is never called.
 *   PREVIEW             everything that needs the user's CHECKOUT: forge's
 *                       render, the cluster inventory, the dev stack, the
 *                       audit, the Deploy/Promote plans, and Register. The
 *                       daemon is required.
 *
 * Two rules follow, and they are the whole decision:
 *
 *   1. LIVE IS NEVER DEGRADED BY DAEMON STATE and carries NO BANNER. A daemon
 *      that is offline, slow, or has never existed changes nothing about what
 *      Live shows. A test renders this page's Live tab with the daemon
 *      transport mocked to throw and asserts zero daemon calls
 *      (__tests__/ForgeEnvPage.liveNoDaemon.test.tsx).
 *   2. PREVIEW ALONE says the daemon is offline, in one short line.
 *
 * Why this page was rebuilt rather than patched: it used to mix the two
 * sources per section, asking the daemon for `forge.env_status` — and forge
 * then called control-plane ListEnvironments with the DAEMON's token, which
 * 403'd a page the user was entitled to see. A daemon-sourced row sitting
 * beside a hosted one is why an asleep laptop degraded a page describing a
 * production environment the control plane was watching the whole time.
 *
 * ── A NEVER-BUILT ENVIRONMENT IS A NORMAL STATE ─────────────────────────────
 *
 * An env in the KCL that the control plane has no row for is not an error and
 * is not "unknown". Live says it has not been built yet and points at Preview
 * or `forge env build`; Preview shows it as "would be created" with a Register
 * button. No error styling anywhere on that path.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  useCloudEnvStatus,
  useCloudPromotions,
  useForgeTopology,
  useLiveView,
} from "@/hooks/forge-queries";
import {
  daemonSideOf,
  resolveForgeProjectName,
  type ForgeEnvSummary,
} from "@/services/forge/environments";
import { isPlacedKind } from "@/services/forge/live";
import { environments, type ForgeTopologyEnv } from "@/services/forge/topology";
import { useProjectStore, type Project } from "@/store/projectStore";

import { PreviewSection } from "../Preview/PreviewSection";
import { CloudNotice } from "../SourceNotices";
import { LiveSection } from "./LiveSection";

type EnvTab = "live" | "preview";

export function ForgeEnvPage() {
  const navigate = useNavigate();
  const { env: envName } = useParams({ from: "/_authenticated/_forge/forge/env/$env" });
  const {
    project: projectParam,
    secret: secretParam,
    tab: tabParam,
  } = useSearch({ from: "/_authenticated/_forge/forge/env/$env" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const persistedName = useProjectStore((state) => persistedForgeProjectName(state, projectParam ?? currentProject?.id));
  const projectId = projectParam ?? currentProject?.id ?? null;

  const tab: EnvTab = tabParam === "preview" ? "preview" : "live";

  // ── PREVIEW's source, AND IT IS ENABLED ONLY ON THE PREVIEW TAB. ──
  //
  // The gate is the rule, not an optimisation. An always-enabled topology
  // query would put a daemon call behind the Live tab — exactly what O-14
  // forbids — and it would do so invisibly, because Live's own rendering would
  // not change. So the query is off until the user opens Preview, and the
  // zero-daemon-call test holds by construction rather than by inspection.
  const topology = useForgeTopology(tab === "preview" ? projectId : null);

  // ── LIVE's join key, and why it needs no daemon. ──
  //
  // The forge project name is the project half of an environment's
  // (org, project, name) identity. Reliant persists it on the project row
  // (forge.yaml `name`, recorded the first time a daemon read it), so Live has
  // it with the daemon down and in any browser. forge's live report wins when
  // there is one — it is forge's answer this second — which only ever happens
  // on the Preview tab, where the topology query runs.
  //
  // Nothing is guessed from the Reliant project's DISPLAY name: a guess that
  // matched another project's `prod` would put the wrong environment on screen
  // beside this one's buttons.
  const projectName = resolveForgeProjectName(persistedName, topology.data);

  // ── LIVE. One call, and the only source the Live tab reads. ──
  const live = useLiveView(projectName.name);
  const liveState = live;

  const liveEnv = useMemo(
    () => (live.data?.envs ?? []).find((candidate) => candidate.name === envName) ?? null,
    [live.data, envName]
  );

  // GetStatus is an OBSERVATION and only the platform makes one. Asked for a
  // placed env alone — a self-managed env has no server-side observer, and
  // asking would answer for a cluster the platform has never connected to.
  const placedId = liveEnv && isPlacedKind(liveEnv.kind) ? liveEnv.id : null;
  const cloudStatus = useCloudEnvStatus(placedId);
  const promotions = useCloudPromotions(liveEnv?.id ?? null);

  const daemon = daemonSideOf(topology.data, topology.error);
  const topologyReport = topology.data?.kind === "report" ? topology.data.report : null;
  const summary: ForgeEnvSummary | null = useMemo(() => {
    const forgeEnv: ForgeTopologyEnv | null = topologyReport
      ? (environments(topologyReport).find((candidate) => candidate.env === envName) ?? null)
      : null;
    if (!forgeEnv) return null;
    return { name: envName, where: "unknown", forge: forgeEnv, cloud: null };
  }, [topologyReport, envName]);

  const selectTab = useCallback(
    (next: EnvTab) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          tab: next === "live" ? undefined : next,
        }),
        replace: true,
      });
    },
    [navigate]
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

  const backToOverview = (
    <button
      type="button"
      onClick={() => void navigate({ to: "/forge", search: { project: projectId ?? undefined } })}
      className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
    >
      <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
      All environments
    </button>
  );

  return (
    <div className="space-y-6" data-testid="forge-env-page" data-tab={tab}>
      {backToOverview}

      {/* The control plane could not answer at all — a role without deploy
          read access, a build with no control plane, an outage. Said once
          here, because with no control plane Live has no source. Never shown
          for `available` or `no-control-plane`. */}
      <CloudNotice availability={liveState.data?.availability} detail={liveState.data?.detail} />

      <div
        className="flex items-center gap-1 border-b border-border"
        role="tablist"
        aria-label={`${envName} views`}
      >
        <Tab current={tab} value="live" onSelect={selectTab}>
          Live
        </Tab>
        <Tab current={tab} value="preview" onSelect={selectTab}>
          Preview
        </Tab>
      </div>

      {tab === "live" ? (
        <div role="tabpanel" aria-label="Live" id="env-tab-live">
          {liveState.isLoading && !liveState.data ? (
            <p data-testid="live-loading" className="text-sm text-muted-foreground">
              Reading <span className="font-mono">{envName}</span>…
            </p>
          ) : (
            <LiveSection
              envName={envName}
              env={liveEnv}
              forgeProject={projectName.name}
              projectId={projectId}
              status={cloudStatus.data}
              statusLoading={cloudStatus.isLoading}
              statusError={cloudStatus.error as Error | null}
              promotions={promotions.data}
              promotionsLoading={promotions.isLoading}
              promotionsError={promotions.error as Error | null}
              selectedSecret={secretParam ?? null}
              onSelectSecret={selectSecret}
              onOpenPreview={() => selectTab("preview")}
            />
          )}
        </div>
      ) : (
        <div role="tabpanel" aria-label="Preview" id="env-tab-preview">
          <PreviewSection
            projectId={projectId}
            envName={envName}
            projectName={currentProject?.name}
            forgeProject={projectName.name}
            summary={summary}
            liveEnv={liveEnv}
            daemonState={daemon}
            daemonError={topology.error as Error | null}
            topologyOutcome={topology.data}
            latestRelease={topologyReport?.latest_release ?? null}
          />
        </div>
      )}
    </div>
  );
}

function Tab({
  current,
  value,
  onSelect,
  children,
}: {
  current: EnvTab;
  value: EnvTab;
  onSelect: (tab: EnvTab) => void;
  children: React.ReactNode;
}) {
  const active = current === value;
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      aria-controls={`env-tab-${value}`}
      data-testid={`env-tab-${value}`}
      onClick={() => onSelect(value)}
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

