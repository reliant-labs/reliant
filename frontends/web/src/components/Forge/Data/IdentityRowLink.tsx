// Copyright (c) 2025 Reliant Labs

import { useContext } from "react";
import Link from "@/components/forge-ui/link";
import { useIdentityOrg, useIdentityStatus } from "@/hooks/identity-queries";
import { ForgeConsoleContext } from "../Console/consoleContext";
import type { IdentityTarget } from "./identityLinks-model";

export function IdentityRowLink({ env, target }: { env: { id: string; name: string }; target: IdentityTarget }) {
  const console = useContext(ForgeConsoleContext);
  return console ? <AvailableIdentityLink env={env} target={target} href={(search) => console.href({ kind: "environment", env: env.name }, { tab: "identity", ...search })} /> : null;
}
function AvailableIdentityLink({ env, target, href }: { env: { id: string; name: string }; target: IdentityTarget; href: (target: IdentityTarget) => string }) {
  const org = useIdentityOrg();
  const status = useIdentityStatus(org.orgId, env.id);
  // Status is scoped to the actual environment, not its identity's owner or a
  // previously selected environment. Errors/refusals never produce a link.
  if (status.error || !status.data?.hasInstance) return null;
  return <Link href={href(target)} className="inline-flex text-xs text-primary-ink hover:underline" data-testid="data-open-identity">Open in Identity</Link>;
}
