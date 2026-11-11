// Copyright (c) 2025 Reliant Labs

import { useContext } from "react";
import { useSearch } from "@tanstack/react-router";
import Link from "@/components/forge-ui/link";
import { useIdentityUser, useIdentityUsers } from "@/hooks/identity-queries";
import type { IdentityUser } from "@/services/controlPlane/identity/api";
import { ForgeConsoleContext } from "../Console/consoleContext";
import { identityDataLinksEnabled, uniqueExactEmail, validIdentityEmail, validIdentityId } from "../Data/identityLinks-model";

type SelectionProps = { orgId: string; envId: string; serving: boolean };
export function IdentitySelection(props: SelectionProps) {
  return identityDataLinksEnabled() ? <EnabledIdentitySelection {...props} /> : null;
}
function EnabledIdentitySelection({ orgId, envId, serving }: SelectionProps) {
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const userId = validIdentityId(raw.user) ? raw.user : null;
  const email = !userId && validIdentityEmail(raw.q) ? raw.q : null;
  const user = useIdentityUser(orgId, envId, userId, serving);
  const matches = useIdentityUsers(orgId, envId, { query: email ?? "" }, serving && !!email);
  const rows = matches.data?.pages.flatMap((page) => page.users) ?? [];
  const selected = userId ? (user.data?.userId === userId ? user.data : null) : email ? uniqueExactEmail(rows, email, !!matches.data && !matches.hasNextPage) : null;
  if (!userId && !email) return null;
  const failed = userId ? user.error : matches.error;
  const pending = userId ? user.isLoading : matches.isLoading;
  return <section className="space-y-2 rounded-md border border-border p-4" aria-label="Selected identity">
    <h3 className="text-sm font-medium text-ink">Selected identity</h3>
    {!serving ? <p className="text-xs text-ink-muted">Identity is not ready.</p> : failed ? <p role="alert" className="text-xs text-danger-ink">Could not open this identity in this environment. The server refused the lookup or the user is unavailable.</p> : pending ? <p className="text-xs text-ink-muted">Loading identity…</p> : selected ? <><p className="text-sm text-ink">{selected.email}</p><p className="font-mono text-xs text-ink-muted">{selected.userId}</p><IdentityAppDataLink envId={envId} user={selected} /></> : <p className="text-xs text-ink-muted">No unique exact identity found. Use the filtered list below to select a user; substring matches are not identities.</p>}
  </section>;
}
export function IdentityUserLink({ envId, user }: { envId: string; user: IdentityUser }) {
  const console = useContext(ForgeConsoleContext);
  const env = console?.roster.envs.find((e) => e.live?.id === envId);
  if (!identityDataLinksEnabled() || !console || !env || !validIdentityId(user.userId)) return null;
  return <Link href={console.href({ kind: "environment", env: env.name }, { tab: "identity", user: user.userId })} className="text-xs text-primary-ink hover:underline">Open identity</Link>;
}
export function IdentityAppDataLink({ envId, user }: { envId: string; user: IdentityUser }) {
  const console = useContext(ForgeConsoleContext);
  const env = console?.roster.envs.find((e) => e.live?.id === envId);
  if (!identityDataLinksEnabled() || !console || !env || !validIdentityId(user.userId)) return null;
  const subject = JSON.stringify({ environmentId: envId, userId: user.userId, email: validIdentityEmail(user.email) ? user.email : "" });
  return <Link href={console.href({ kind: "data" }, { env: env.name, subject })} className="text-xs text-primary-ink hover:underline">App data</Link>;
}
