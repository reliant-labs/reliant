// Copyright (c) 2025 Reliant Labs

/**
 * The frame the console's own pages share (ConsolePages.tsx, SecretsPage.tsx,
 * ActivityPage.tsx): the page template (PageHeader with one meta line, then
 * the body), and the scope notes a list page shows. Each page is its own
 * chunk, so this module holds only what every one of them needs.
 *
 * Every list page honours the top-bar scope (useForgeConsole().env): one
 * environment, or all of them.
 */

import { useMemo, type ReactNode } from "react";
import { Rocket } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import Link from "@/components/forge-ui/link";
import type { PageHeaderAction } from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import type { LiveEnv } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";

import { DeployWithAgentButton } from "../Observability/AskReliant/AskReliant";
import { useForgeConsole } from "./consoleContext";
import { ENV_ALL, envsInScope } from "./envScope";
import { ConsolePageHeader } from "./severalSectionContext";

/**
 * A console page's header: one compact row (forge-ui PageHeader, "compact").
 * It never repeats the environment scope: the top bar's scope control is the
 * one place that says it, and a second line saying "All environments" under
 * every title was empty space above every page.
 */
export function Header({ title, subtitle, meta, actions }: { title: string; subtitle?: ReactNode; meta?: ReactNode; actions?: PageHeaderAction[] }) {
  // One project's section of a page across several (severalProjectSections.tsx) keeps only its facts and actions.
  return <ConsolePageHeader density="compact" title={title} subtitle={subtitle} meta={meta} actions={actions} className="mb-0" />;
}

export function Page({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div className="space-y-4" data-testid={testId}>
      {children}
    </div>
  );
}

export function RosterLoading() {
  return (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );
}

/**
 * The recorded, deployed environments in scope — what has releases and
 * activity. All leaves previews out: there can be dozens, and the scope menu
 * picks one.
 */
export function useDeployedInScope(): { envs: (RosterEnv & { live: LiveEnv })[]; scoped: RosterEnv | null } {
  const { env, roster } = useForgeConsole();
  return useMemo(() => {
    const deployed = roster.envs.filter(
      (candidate): candidate is RosterEnv & { live: LiveEnv } =>
        !!candidate.live && candidate.live.kind !== "local" && (env !== ENV_ALL || candidate.live.kind !== "preview")
    );
    return {
      envs: envsInScope(env, deployed),
      scoped: env === ENV_ALL ? null : (roster.envs.find((candidate) => candidate.name === env) ?? null),
    };
  }, [env, roster.envs]);
}

/** Why a single-environment scope has nothing to list here, or null when it does. */
export function ScopedEnvNote({ scoped, what }: { scoped: RosterEnv | null; what: string }) {
  const { env, href } = useForgeConsole();
  if (env === ENV_ALL) return null;
  const envLink = (
    <Link href={href({ kind: "environment", env })} className="font-medium text-ink underline-offset-2 hover:underline">
      {env}
    </Link>
  );
  const sentence = !scoped ? (
    <>There is no environment named {env} in this project.</>
  ) : scoped.lifecycle === "local" ? (
    <>{envLink} runs locally from your working tree, so it has no {what}.</>
  ) : !scoped.live ? (
    <>Reliant has no record of {envLink} yet, so it has no {what} here. Register it from its environment page.</>
  ) : null;
  if (!sentence) return null;
  return (
    <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="scoped-env-note">
      {sentence}
    </p>
  );
}

export function NothingDeployed({ what, about }: { what: string; about: string }) {
  return (
    <GettingStarted
      testId="console-nothing-deployed"
      icon={<Rocket className="h-4 w-4" />}
      title="Nothing deployed yet"
      about={about}
      agent={<DeployWithAgentButton env={null} />}
      steps={[
        {
          text: "Deploy an environment through Reliant from your checkout. It builds, cuts a release and deploys it.",
          command: "reliant forge env deploy <env>",
        },
      ]}
      docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
    >
      <p>
        An environment has {what} once a release is promoted to it. A local environment (reliant forge env up) runs your working tree and
        records none.
      </p>
    </GettingStarted>
  );
}
