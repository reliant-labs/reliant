// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/users — who can sign in to an environment, as a console
 * page.
 *
 * A page beside Data and Storage, not only an environment tab, for the reason
 * those give: what an environment HAS is a project page scoped by the top
 * bar's environment control, the console's one environment selector. "Who
 * are my users?" was otherwise three clicks deep (Environments › prod ›
 * Identity), where nobody looked for it.
 *
 * The content is the environment page's Identity tab (EnvPage/IdentityTab.tsx)
 * in its page layout: the people first — search, state, roles, the verbs —
 * and the sign-in service that holds them (its state, issuer and
 * applications) beside them. The tab stays, and links here; `?tab=identity`
 * links keep opening it. This page reads `user` and `q` the way the tab does,
 * so "Open in Users" from a Data row opens the same person.
 *
 * Above it all is the USER ACTIVITY slot: new, returning and active people
 * over time, which another workstream fills (UserActivitySlot below).
 */

import { useCallback, type ReactNode } from "react";
import { Layers, Users } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { Frame, IdentityTab } from "../EnvPage/IdentityTab";
import { CloudNotice } from "../SourceNotices";

function PageFrame({ children }: { children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-4" data-testid="console-users">
      <PageHeader
        density="compact"
        title="Users"
        subtitle="Who can sign in to this environment's apps, through the sign-in service Reliant runs for it."
        className="mb-0"
      />
      {children}
    </div>
  );
}

/**
 * ── USER ACTIVITY SLOT ──────────────────────────────────────────────────────
 * Reserved for user activity — new, returning and active people in the scoped
 * environment — which another workstream is building. It renders nothing
 * until then. Fill it here, not elsewhere on the page: it is the first thing
 * under the header, above the list, for one environment at a time.
 */
function UserActivitySlot(_props: { env: string; environmentId: string | null }) {
  return null;
}

export function UsersPage() {
  const { env, roster, setEnv, href } = useForgeConsole();
  const envNameById = useCallback(
    (id: string) => roster.envs.find((candidate) => candidate.live?.id === id)?.name,
    [roster.envs]
  );

  if (roster.isLoading || roster.resolvingName) {
    return (
      <PageFrame>
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      </PageFrame>
    );
  }

  const availability = roster.live.data?.availability;

  if (env === ENV_ALL) {
    const choices = roster.envs.filter((candidate) => !!candidate.live || candidate.lifecycle === "local");
    return (
      <PageFrame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <Frame icon={<Layers className="h-4 w-4" />} title="Choose an environment" testId="users-choose-env">
          <p>Each environment signs people in on its own, with users of its own. Pick one here or in the top bar; every link you share keeps it.</p>
          {choices.length > 0 && (
            <div className="flex flex-wrap gap-2 pt-1">
              {choices.map((candidate) => (
                <Button
                  key={candidate.name}
                  variant="outline"
                  size="sm"
                  className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0"
                  onClick={() => setEnv(candidate.name)}
                  data-testid={`users-env-${candidate.name}`}
                >
                  {candidate.name}
                </Button>
              ))}
            </div>
          )}
        </Frame>
      </PageFrame>
    );
  }

  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;
  if (!scoped) {
    return (
      <PageFrame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <Frame icon={<Users className="h-4 w-4" />} title={`There is no environment named ${env}`} testId="users-env-unknown">
          <p>
            This project has no environment by that name.{" "}
            <Link href={href({ kind: "environments" })} className="font-medium text-ink underline-offset-2 hover:underline">
              See its environments
            </Link>
            .
          </p>
        </Frame>
      </PageFrame>
    );
  }

  return (
    <PageFrame>
      <UserActivitySlot env={scoped.name} environmentId={scoped.live?.id ?? null} />
      <CloudNotice availability={availability} detail={roster.live.data?.detail} />
      <IdentityTab
        key={scoped.live?.id ?? scoped.name}
        envName={scoped.name}
        lifecycle={scoped.lifecycle}
        live={scoped.live}
        liveLoading={roster.live.isLoading}
        envNameById={envNameById}
        layout="page"
      />
    </PageFrame>
  );
}
