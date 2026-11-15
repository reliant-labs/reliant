// Copyright (c) 2025 Reliant Labs

/** The console's Activity page: a chunk of its own (forgeConsolePages.ts), in the frame of consolePageFrame.tsx. */

import { useMemo } from "react";

import { useObservabilityOrg } from "@/hooks/observability-queries";

import { ActivityFeed } from "../Activity/ActivityFeed";
import { Panel } from "../EnvPage/EnvTabPanels";
import { CloudNotice } from "../SourceNotices";
import { useForgeConsole } from "./consoleContext";
import { Header, NothingDeployed, Page, RosterLoading, ScopedEnvNote, useDeployedInScope } from "./consolePageFrame";
import { DeployStatusUnread, deployStatusUnread } from "./DeployStatusUnread";
import { ENV_ALL } from "./envScope";

/**
 * The AUDIT LOG: the typed activity stream for the project — who changed what,
 * through what. Promotions are not repeated here as their own timelines: a
 * release's promotions, rollbacks and deploys are the Releases page's.
 */
export function ActivityPage() {
  const { roster, forgeProject, env } = useForgeConsole();
  const { envs, scoped } = useDeployedInScope();
  const org = useObservabilityOrg();
  // A scoped environment with no live record has no id to ask for: its feed is the org-wide one, not everything.
  const scopedId = scoped?.live?.id;
  const feedScope = useMemo(
    () => (org.orgId && (env === ENV_ALL || scopedId) ? { orgId: org.orgId, projectId: forgeProject, envIds: scopedId ? [scopedId] : [] } : null),
    [org.orgId, forgeProject, env, scopedId]
  );
  return (
    <Page testId="console-activity">
      <Header title="Activity" />
      {roster.isLoading || roster.resolvingName ? (
        <RosterLoading />
      ) : (
        <>
          <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
          <ScopedEnvNote scoped={scoped} what="activity" />
          {feedScope && (
            <Panel title="Everything that changed" description="Who did what, and through what: promotions, triage, database queries, issues Reliant opened." testId="activity-stream" flush>
              <ActivityFeed scope={feedScope} subject={forgeProject} empty="Nothing recorded yet." testId="activity-stream-feed" />
            </Panel>
          )}
          {envs.length === 0 && !scoped && deployStatusUnread(roster.live) && (
            <DeployStatusUnread state={deployStatusUnread(roster.live)!} testId="activity-unread" />
          )}
          {envs.length === 0 && !scoped && !deployStatusUnread(roster.live) && (
            <NothingDeployed
              what="activity"
              about="Activity is everything that changed in this project: each promotion and rollback, what the platform observed after it, and what people and agents did."
            />
          )}
        </>
      )}
    </Page>
  );
}
