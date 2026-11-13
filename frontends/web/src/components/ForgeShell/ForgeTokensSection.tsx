// Copyright (c) 2025 Reliant Labs

/**
 * Settings › API tokens in the forge shell: personal access tokens for the
 * forge CLI and CI (services/controlPlane/forgeTokens.ts). Reliant's Tokens
 * section makes DAEMON credentials; these are the ones
 * `FORGE_CONTROL_PLANE_TOKEN=… forge env deploy` and a pipeline use. The secret is shown once, with a copy
 * button, and never again.
 *
 * Every scope the server accepts on an unbound token is offered, grouped by
 * product (FORGE_TOKEN_SCOPE_GROUPS). sourcemaps:write is the exception to
 * "a token acts as you": it is a registry-only ORGANIZATION token, so picking
 * it clears every other scope, and it is minted and listed through the org
 * token rpcs, which need the admin role.
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import { formatRelativeTime } from "@/lib/relativeTime";
import {
  createForgeToken,
  DEFAULT_FORGE_TOKEN_SCOPES,
  FORGE_TOKEN_ENV,
  FORGE_TOKEN_SCOPE_GROUPS,
  type ForgeToken,
  isOrgTokenOnlyScope,
  listForgeTokens,
  listOrgForgeTokens,
  revokeForgeToken,
  revokeOrgForgeToken,
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
  const orgKey = ["forge-org-tokens", orgId] as const;
  const tokens = useQuery({ queryKey: key, queryFn: () => listForgeTokens(orgId), enabled: !!orgId, retry: false });
  // Only an admin may list the org's tokens (anyone else gets
  // PermissionDenied), so the section is shown only when the list answers.
  const orgTokens = useQuery({ queryKey: orgKey, queryFn: () => listOrgForgeTokens(orgId), enabled: !!orgId, retry: false });

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(DEFAULT_FORGE_TOKEN_SCOPES);
  const [expiresInDays, setExpiresInDays] = useState<number | null>(90);
  const [secret, setSecret] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => createForgeToken({ orgId, name: name.trim(), scopes, expiresInDays }),
    onSuccess: ({ secret: created, kind }) => {
      setSecret(created);
      setName("");
      void queryClient.invalidateQueries({ queryKey: kind === "org" ? orgKey : key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokeForgeToken(orgId, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: key }),
  });
  const revokeOrg = useMutation({
    mutationFn: (id: string) => revokeOrgForgeToken(orgId, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: orgKey }),
  });

  // An org-token-only scope stands alone: picking it clears the rest, and
  // picking anything else clears it.
  const toggle = (scope: string) =>
    setScopes((prev) => {
      if (prev.includes(scope)) return prev.filter((s) => s !== scope);
      if (isOrgTokenOnlyScope(scope)) return [scope];
      return [...prev.filter((s) => !isOrgTokenOnlyScope(s)), scope];
    });
  const orgTokenSelected = scopes.some(isOrgTokenOnlyScope);

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
        <fieldset className="space-y-3">
          <legend className="text-sm text-muted-foreground">Scopes</legend>
          <p className="text-xs text-muted-foreground">
            Organization permissions work only while you hold them (Settings › Members); the server refuses a scope you
            do not hold.
          </p>
          {FORGE_TOKEN_SCOPE_GROUPS.map((group) => (
            <div key={group.id} role="group" aria-labelledby={`forge-token-group-${group.id}`} className="space-y-1.5" data-testid={`forge-token-group-${group.id}`}>
              <div>
                <h4 id={`forge-token-group-${group.id}`} className="text-sm font-medium">
                  {group.title}
                </h4>
                {group.description && <p className="text-xs text-muted-foreground">{group.description}</p>}
              </div>
              {group.scopes.map(({ scope, label }) => (
                <label key={scope} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" aria-label={scope} checked={scopes.includes(scope)} onChange={() => toggle(scope)} />
                  <code className="text-xs">{scope}</code>
                  <span className="text-muted-foreground">{label}</span>
                </label>
              ))}
            </div>
          ))}
        </fieldset>
        {orgTokenSelected && (
          <p className="text-xs text-muted-foreground" data-testid="forge-token-org-note">
            This creates an organization token: it acts as no one, carries no other scope, and needs the admin role.
          </p>
        )}
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
            <p className="text-xs text-muted-foreground" data-testid="forge-token-usage">
              Set it as <code>{FORGE_TOKEN_ENV}</code> in CI (or the variable your environment&apos;s <code>token_env</code> names), or pass
              it to the forge CLI with <code>--token</code>.
            </p>
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
          <TokenList tokens={tokens.data ?? []} onRevoke={(id) => revoke.mutate(id)} revoking={revoke.isPending} testIdPrefix="forge-token" />
        )}
      </section>

      {orgTokens.isSuccess && (
        <section className="rounded-lg border border-border bg-card" data-testid="forge-org-tokens">
          <div className="border-b border-border px-4 py-3">
            <h3 className="text-sm font-medium">Organization tokens</h3>
            <p className="text-xs text-muted-foreground">Tokens that act as no one, such as source-map tokens. Any admin may revoke them.</p>
          </div>
          {orgTokens.data.length === 0 ? (
            <p className="px-4 py-3 text-sm text-muted-foreground">No organization tokens.</p>
          ) : (
            <TokenList
              tokens={orgTokens.data}
              onRevoke={(id) => revokeOrg.mutate(id)}
              revoking={revokeOrg.isPending}
              testIdPrefix="forge-org-token"
            />
          )}
        </section>
      )}
    </div>
  );
}

function TokenList({
  tokens,
  onRevoke,
  revoking,
  testIdPrefix,
}: {
  tokens: ForgeToken[];
  onRevoke: (id: string) => void;
  revoking: boolean;
  testIdPrefix: string;
}) {
  return (
    <ul className="divide-y divide-border">
      {tokens.map((t) => (
        <li key={t.id} className="flex items-center gap-3 px-4 py-3 text-sm" data-testid={`${testIdPrefix}-${t.id}`}>
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
            onClick={() => onRevoke(t.id)}
            disabled={revoking}
            className="rounded-md px-2 py-1 text-sm text-destructive hover:bg-destructive/10"
          >
            Revoke
          </button>
        </li>
      ))}
    </ul>
  );
}
