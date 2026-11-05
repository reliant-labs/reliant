// Copyright (c) 2025 Reliant Labs

/**
 * Settings › API tokens in the forge shell: personal access tokens for the
 * forge CLI and CI (services/controlPlane/forgeTokens.ts). Reliant's Tokens
 * section makes DAEMON credentials; these are the ones `FORGE_TOKEN=… forge
 * env deploy` and a pipeline use. The secret is shown once, with a copy
 * button, and never again.
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import { formatRelativeTime } from "@/lib/relativeTime";
import {
  createForgeToken,
  DEFAULT_FORGE_TOKEN_SCOPES,
  FORGE_TOKEN_SCOPES,
  listForgeTokens,
  revokeForgeToken,
} from "@/services/controlPlane/forgeTokens";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { SettingsPageHeader } from "../Settings/SettingsPageHeader";
import { CommandLine } from "./RunFromTerminal";

const EXPIRY_OPTIONS = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
  { days: null, label: "Never" },
] as const;

export function ForgeTokensSection() {
  const orgId = useActiveOrgStore((s) => s.effectiveId) ?? "";
  const queryClient = useQueryClient();
  const key = ["forge-tokens", orgId] as const;
  const tokens = useQuery({ queryKey: key, queryFn: () => listForgeTokens(orgId), enabled: !!orgId, retry: false });

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(DEFAULT_FORGE_TOKEN_SCOPES);
  const [expiresInDays, setExpiresInDays] = useState<number | null>(90);
  const [secret, setSecret] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => createForgeToken({ orgId, name: name.trim(), scopes, expiresInDays }),
    onSuccess: ({ secret: created }) => {
      setSecret(created);
      setName("");
      void queryClient.invalidateQueries({ queryKey: key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokeForgeToken(orgId, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: key }),
  });

  const toggle = (scope: string) =>
    setScopes((prev) => (prev.includes(scope) ? prev.filter((s) => s !== scope) : [...prev, scope]));

  return (
    <div className="space-y-6" data-testid="forge-tokens">
      <SettingsPageHeader
        title="API tokens"
        description="Tokens for the forge CLI and CI. A token acts as you, limited to the scopes you pick and to what you may do in this organization."
      />

      <section className="space-y-4 rounded-lg border border-border bg-card p-4">
        <h3 className="text-sm font-medium">Create a token</h3>
        <label className="block space-y-1 text-sm">
          <span className="text-muted-foreground">Name</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ci-deploy"
            className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm"
            data-testid="forge-token-name"
          />
        </label>
        <fieldset className="space-y-1.5">
          <legend className="text-sm text-muted-foreground">Scopes</legend>
          {FORGE_TOKEN_SCOPES.map(({ scope, label }) => (
            <label key={scope} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={scopes.includes(scope)} onChange={() => toggle(scope)} />
              <code className="text-xs">{scope}</code>
              <span className="text-muted-foreground">{label}</span>
            </label>
          ))}
        </fieldset>
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Expires</span>
          <select
            value={expiresInDays ?? "never"}
            onChange={(e) => setExpiresInDays(e.target.value === "never" ? null : Number(e.target.value))}
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
          >
            {EXPIRY_OPTIONS.map((o) => (
              <option key={o.label} value={o.days ?? "never"}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <div className="forge-ui">
          <Button
            variant="primary"
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!orgId || !name.trim() || scopes.length === 0}
          >
            Create token
          </Button>
        </div>
        {create.isError && <p role="alert" className="text-sm text-destructive">Could not create the token. {String((create.error as Error)?.message ?? "")}</p>}
        {secret && (
          <div className="space-y-2 rounded-md border border-border bg-background p-3" data-testid="forge-token-secret">
            <p className="text-sm font-medium">Copy your token now. It is not shown again.</p>
            <CommandLine command={secret} />
            <p className="text-xs text-muted-foreground">Use it as FORGE_TOKEN in CI, or pass it to the forge CLI.</p>
          </div>
        )}
      </section>

      <section className="rounded-lg border border-border bg-card">
        <h3 className="border-b border-border px-4 py-3 text-sm font-medium">Your tokens</h3>
        {tokens.isPending && orgId ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="status">Loading tokens…</p>
        ) : tokens.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">Could not load your tokens.</p>
        ) : (tokens.data ?? []).length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">No tokens yet.</p>
        ) : (
          <ul className="divide-y divide-border">
            {(tokens.data ?? []).map((t) => (
              <li key={t.id} className="flex items-center gap-3 px-4 py-3 text-sm" data-testid={`forge-token-${t.id}`}>
                <div className="min-w-0 flex-1">
                  <div className="font-medium">
                    {t.name} <code className="ml-1 text-xs text-muted-foreground">{t.prefix}…</code>
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {t.allScopes ? "All scopes" : t.scopes.join(", ")}
                    {t.lastUsedAt ? ` · used ${formatRelativeTime(t.lastUsedAt.toISOString())}` : " · never used"}
                    {t.expiresAt ? ` · expires ${t.expiresAt.toLocaleDateString()}` : " · no expiry"}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => revoke.mutate(t.id)}
                  disabled={revoke.isPending}
                  className="rounded-md px-2 py-1 text-sm text-destructive hover:bg-destructive/10"
                >
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
