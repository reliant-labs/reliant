// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/data — the data browser, as a console page.
 *
 * WHY A PAGE AND NOT AN ENVIRONMENT TAB. The environment page is the
 * environment as a configured object (EnvPage/envTabs.ts); everything an
 * environment HAS — releases, secrets, telemetry — is a project page scoped
 * by the top bar's environment control, the console's one environment
 * selector (ConsoleShell). An environment's rows are the same kind of thing
 * as its logs, so Data is a page beside Observability, read through the same
 * scope, and switching prod → staging in the top bar keeps the table open.
 *
 * The browser itself (DataBrowser) is free of the console; this page only
 * resolves the scope to one recorded environment and binds the URL.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Database, Layers } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { CloudNotice } from "../SourceNotices";
import { DataBrowser } from "./DataBrowser";
import { SaveBrowseView } from "./SaveBrowseView";
import { DataNote } from "./DataStates";
import { DATA_SEARCH_KEYS, type DataSearch } from "./dataUrl-model";

function pickData(search: Record<string, unknown>): DataSearch {
  const out: Record<string, unknown> = {};
  for (const key of DATA_SEARCH_KEYS) if (search[key] !== undefined) out[key] = search[key];
  return out as DataSearch;
}

function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="space-y-4" data-testid="console-data">
      <PageHeader density="compact" title="Data" className="mb-0" />
      {children}
    </div>
  );
}

export function DataPage() {
  const { env, roster, setEnv, href } = useForgeConsole();
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const search = useMemo(() => pickData(raw), [raw]);
  const navigate = useNavigate();

  const onNavigate = useCallback(
    (next: DataSearch, options: { replace?: boolean } = {}) =>
      void navigate({
        to: ".",
        // Only the scope survives from the old search: every data key is the new view's.
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: options.replace ?? false,
      }),
    [navigate]
  );
  const dataHref = useCallback((next: DataSearch) => href({ kind: "data" }, { ...next }), [href]);

  if (roster.isLoading || roster.resolvingName) {
    return (
      <Frame>
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      </Frame>
    );
  }

  const recorded = roster.envs.filter((candidate) => !!candidate.live);
  const availability = roster.live.data?.availability;

  if (env === ENV_ALL) {
    return (
      <Frame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <DataNote
          testId="data-choose-env"
          icon={<Layers className="h-4 w-4" />}
          title="Choose an environment"
          actions={recorded.map((candidate) => (
            <Button key={candidate.name} variant="outline" size="sm" onClick={() => setEnv(candidate.name)} data-testid={`data-env-${candidate.name}`}>
              {candidate.name}
            </Button>
          ))}
        >
          <p>Each environment has databases of its own. Pick one here or in the top bar; every link you share keeps it.</p>
        </DataNote>
      </Frame>
    );
  }

  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;
  if (!scoped?.live) {
    const envLink = (
      <Link href={href({ kind: "environment", env })} className="font-medium text-ink underline-offset-2 hover:underline">
        {env}
      </Link>
    );
    return (
      <Frame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <DataNote testId="data-env-unrecorded" icon={<Database className="h-4 w-4" />} title={`Reliant has no record of ${env}`}>
          <p>
            {!scoped ? (
              <>There is no environment named {env} in this project.</>
            ) : scoped.lifecycle === "local" ? (
              <>
                {envLink} runs on your machine (forge env up), and its database is there too: open it with{" "}
                <code className="font-mono text-ink">forge db query {env}</code>.
              </>
            ) : (
              <>The browser reads an environment&apos;s databases through its record here. Deploy {envLink} through Reliant to register it.</>
            )}
          </p>
        </DataNote>
      </Frame>
    );
  }

  return (
    <Frame>
      <SaveBrowseView key={`save/${scoped.live.id}`} search={search} environmentId={scoped.live.id} />
      <DataBrowser key={scoped.live.id} env={{ id: scoped.live.id, name: scoped.name }} search={search} onNavigate={onNavigate} href={dataHref} />
    </Frame>
  );
}
