// Copyright (c) 2025 Reliant Labs

/**
 * Settings › API tokens in the forge shell: personal access tokens for the
 * forge CLI and CI (services/controlPlane/forgeTokens.ts). Reliant's Tokens
 * section makes DAEMON credentials; these are the ones
 * `FORGE_CONTROL_PLANE_TOKEN=… forge env deploy` and a pipeline use. The secret is shown once, with a copy
 * button, and never again.
 *
 * A token is PERSONAL (acts as you, CreateMyToken) or an ORGANIZATION token
 * (acts as no one, CreateToken, needs the admin role). Its access is a scope
 * BUNDLE or Custom. A personal token starts with the developer bundle, an
 * organization token with ci, and an organization token is never offered
 * developer, which acts as a person. A chosen bundle shows what it resolves to
 * today (scopeCatalog.ts, pinned to forge's catalog), and says that no bundle
 * carries a sensitive scope: those are picked one by one under Custom.
 *
 * Under Custom a personal token offers every scope the server accepts on an
 * unbound token, grouped by product (FORGE_TOKEN_SCOPE_GROUPS), except the
 * upload-only ones (ORG_TOKEN_ONLY_SCOPES: sourcemaps:write, tests:write),
 * which an organization token's Custom offers instead, one at a time.
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import { formatRelativeTime } from "@/lib/relativeTime";
import {
  createForgeToken,
  DEFAULT_FORGE_TOKEN_BUNDLE,
  DEFAULT_FORGE_TOKEN_SCOPES,
  FORGE_TOKEN_BUNDLES,
  FORGE_TOKEN_ENV,
  FORGE_TOKEN_SCOPE_GROUPS,
  type ForgeToken,
  type ForgeTokenKind,
  forgeTokenBundlesFor,
  isOrgTokenOnlyScope,
  listForgeTokens,
  listOrgForgeTokens,
  revokeForgeToken,
  revokeOrgForgeToken,
} from "@/services/controlPlane/forgeTokens";
import { expandBundle, type ForgeBundle, isForgeBundle, SENSITIVE_SCOPES } from "@/services/controlPlane/scopeCatalog";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { SettingsPageHeader } from "../Settings/SettingsPageHeader";
import { CommandLine } from "./RunFromTerminal";

const EXPIRY_OPTIONS = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
  { days: null, label: "Never" },
] as const;

const KIND_OPTIONS: readonly { kind: ForgeTokenKind; label: string; description: string }[] = [
  { kind: "personal", label: "Personal", description: "Acts as you, limited to what you hold in this organization." },
  { kind: "org", label: "Organization", description: "Acts as no one. Needs the admin role; any admin may revoke it." },
];

/** A bundle, or "custom": the scopes picked one by one. */
type Access = ForgeBundle | "custom";

/** The scope groups Custom offers a token of this kind, each narrowed to the scopes it may carry. */
function customGroupsFor(kind: ForgeTokenKind) {
  return FORGE_TOKEN_SCOPE_GROUPS.map((g) => ({
    ...g,
    scopes: g.scopes.filter(({ scope }) => isOrgTokenOnlyScope(scope) === (kind === "org")),
  })).filter((g) => g.scopes.length > 0);
}

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
  const [kind, setKind] = useState<ForgeTokenKind>("personal");
  const [access, setAccess] = useState<Access>(DEFAULT_FORGE_TOKEN_BUNDLE.personal);
  const [scopes, setScopes] = useState<string[]>(DEFAULT_FORGE_TOKEN_SCOPES);
  const [expiresInDays, setExpiresInDays] = useState<number | null>(90);
  const [secret, setSecret] = useState<string | null>(null);

  const custom = access === "custom";
  const create = useMutation({
    mutationFn: () =>
      createForgeToken({
        orgId,
        name: name.trim(),
        kind,
        scopes: custom ? scopes : [],
        bundles: custom ? [] : [access],
        expiresInDays,
      }),
    onSuccess: ({ secret: created, kind: made }) => {
      setSecret(created);
      setName("");
      void queryClient.invalidateQueries({ queryKey: made === "org" ? orgKey : key });
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

  // A new kind starts over: its default bundle, and Custom's default scopes
  // (an organization token's Custom has no default; its scopes stand alone).
  const chooseKind = (next: ForgeTokenKind) => {
    setKind(next);
    setAccess(DEFAULT_FORGE_TOKEN_BUNDLE[next]);
    setScopes(next === "personal" ? DEFAULT_FORGE_TOKEN_SCOPES : []);
  };

  // An org-token-only scope stands alone: picking it clears the rest, and
  // picking anything else clears it.
  const toggle = (scope: string) =>
    setScopes((prev) => {
      if (prev.includes(scope)) return prev.filter((s) => s !== scope);
      if (isOrgTokenOnlyScope(scope)) return [scope];
      return [...prev.filter((s) => !isOrgTokenOnlyScope(s)), scope];
    });

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

        <fieldset className="space-y-1.5" data-testid="forge-token-kind">
          <legend className="text-ui text-muted-foreground">Acts as</legend>
          {KIND_OPTIONS.map((o) => (
            <label key={o.kind} className="flex items-baseline gap-2 text-ui">
              <input type="radio" name="forge-token-kind" aria-label={o.label} checked={kind === o.kind} onChange={() => chooseKind(o.kind)} />
              <span className="font-medium">{o.label}</span>
              <span className="text-meta text-muted-foreground">{o.description}</span>
            </label>
          ))}
        </fieldset>

        <fieldset className="space-y-1.5" data-testid="forge-token-access">
          <legend className="text-ui text-muted-foreground">Access</legend>
          {forgeTokenBundlesFor(kind).map((b) => (
            <label key={b} className="flex items-baseline gap-2 text-ui">
              <input
                type="radio"
                name="forge-token-access"
                aria-label={`${FORGE_TOKEN_BUNDLES[b].label} bundle`}
                checked={access === b}
                onChange={() => setAccess(b)}
              />
              <span className="font-medium">{FORGE_TOKEN_BUNDLES[b].label} bundle</span>
              <span className="text-meta text-muted-foreground">{FORGE_TOKEN_BUNDLES[b].description}</span>
            </label>
          ))}
          <label className="flex items-baseline gap-2 text-ui">
            <input type="radio" name="forge-token-access" aria-label="Custom" checked={custom} onChange={() => setAccess("custom")} />
            <span className="font-medium">Custom</span>
            <span className="text-meta text-muted-foreground">
              {kind === "personal" ? "Pick each scope, sensitive ones included." : "An upload-only token: one scope and nothing else."}
            </span>
          </label>
        </fieldset>

        {custom ? (
          <fieldset className="space-y-3">
            <legend className="text-sm text-muted-foreground">Scopes</legend>
            <p className="text-xs text-muted-foreground">
              Organization permissions work only while you hold them (Settings › Members); the server refuses a scope you
              do not hold.
            </p>
            {customGroupsFor(kind).map((group) => (
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
        ) : (
          <BundlePreview bundle={access} kind={kind} />
        )}
        {kind === "org" && (
          <p className="text-xs text-muted-foreground" data-testid="forge-token-org-note">
            This creates an organization token: it acts as no one, carries only what you pick here, and needs the admin
            role.
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
            disabled={!orgId || !name.trim() || (custom && scopes.length === 0)}
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
            <p className="text-xs text-muted-foreground">Tokens that act as no one, such as CI and source-map tokens. Any admin may revoke them.</p>
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

/**
 * What the chosen bundle resolves to today, from the catalog this repo pins
 * (the server expands it the same way at every use), and the sensitive scopes
 * it never carries.
 */
function BundlePreview({ bundle, kind }: { bundle: ForgeBundle; kind: ForgeTokenKind }) {
  const resolved = expandBundle(bundle);
  return (
    <div className="space-y-2 rounded-md border border-border/60 bg-background p-3" data-testid="forge-token-bundle-preview">
      <p className="text-ui">
        The {FORGE_TOKEN_BUNDLES[bundle].label} bundle resolves to {resolved.length} scopes today:
      </p>
      <ul className="flex flex-wrap gap-x-3 gap-y-1" data-testid="forge-token-bundle-scopes">
        {resolved.map((s) => (
          <li key={s}>
            <code className="text-xs">{s}</code>
          </li>
        ))}
      </ul>
      <p className="text-meta text-muted-foreground">
        {kind === "personal"
          ? "You must hold each of these yourself to create the token, and it works only while you still do. "
          : "You must hold each of these organization-wide to create the token. "}
        The bundle is expanded at every use, so a scope added to it later reaches this token too.
      </p>
      <p className="text-meta text-muted-foreground" data-testid="forge-token-sensitive-note">
        Sensitive scopes are never part of a bundle: {SENSITIVE_SCOPES.join(", ")}.{" "}
        {kind === "personal"
          ? "To give a token one, choose Custom and pick it explicitly."
          : "An organization token made here never carries one."}
      </p>
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
              {t.bundles.length > 0 && (
                <span className="text-foreground" data-testid={`${testIdPrefix}-${t.id}-bundles`}>
                  {t.bundles.map((b) => `${isForgeBundle(b) ? FORGE_TOKEN_BUNDLES[b].label : b} bundle`).join(", ")}
                  {" · "}
                </span>
              )}
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
