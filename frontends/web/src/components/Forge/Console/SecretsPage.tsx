// Copyright (c) 2025 Reliant Labs

/** The console's Secrets page: a chunk of its own (forgeConsolePages.ts), in the frame of consolePageFrame.tsx. */

import { useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { SecretsTab } from "../EnvPage/EnvTabPanels";
import { SecretsMatrix } from "../Secrets/SecretsMatrix";
import { useForgeConsole } from "./consoleContext";
import { Header, Page, RosterLoading } from "./consolePageFrame";
import { ENV_ALL } from "./envScope";

/** All scope compares metadata; a scoped environment owns the editing flow. */
export function SecretsPage() {
  const { env, roster, projectId, forgeProject, href } = useForgeConsole();
  const navigate = useNavigate();
  const { secret } = useSearch({ strict: false }) as { secret?: string };
  const selectSecret = useCallback(
    (name: string | null) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, secret: name ?? undefined }),
        replace: true,
      }),
    [navigate]
  );
  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;

  return (
    <Page testId="console-secrets">
      <Header title="Secrets" />
      {roster.isLoading || roster.resolvingName ? (
        <RosterLoading />
      ) : env === ENV_ALL ? (
        <SecretsMatrix
          envs={roster.envs}
          secretHref={(name, secretName) => href({ kind: "secrets" }, { env: name, secret: secretName })}
        />
      ) : (
        <SecretsTab
          projectId={projectId}
          live={scoped?.live ?? null}
          envName={env}
          forgeProject={forgeProject}
          selectedSecret={secret ?? null}
          onSelectSecret={selectSecret}
        />
      )}
    </Page>
  );
}
