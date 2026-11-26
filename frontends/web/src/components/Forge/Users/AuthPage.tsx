// Copyright (c) 2026 Reliant Labs

/**
 * AUTH (U6d, owner): who can sign in to the app, and how — beside Data and
 * Storage in the nav. "Sign-in users" moved here from Configure.
 *
 *   /forge/p/:app/auth            Users: the scoped environment's people (the old
 *                                 Users page; `user` and `q` as it read them)
 *   /forge/p/:app/auth/sign-in    Sign-in: the sign-in service Reliant runs for the
 *                                 environment (state, issuer, applications, export),
 *                                 and how the app declares it
 *   /forge/p/:app/users           → /auth, its search kept (forgeConsoleRoutes.tsx)
 *
 * Both tabs are the managed identity's own panels (EnvPage/IdentityTab.tsx,
 * layouts "users" and "service"), scoped by the top bar's environment.
 */

import type { ReactNode } from "react";
import { ExternalLink } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { ConsolePageHeader } from "../Console/severalSectionContext";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { AUTH_TAB_LABELS, AUTH_TABS, type AuthTab } from "../Console/paths";
import { ScopedIdentity } from "./UsersPage";
import { learnUrl } from "@/lib/learnLinks";

const SUBTITLE: Record<AuthTab, string> = {
  users: "Who can sign in to this environment's app, through the sign-in service Reliant runs for it",
  "sign-in": "How people sign in to this environment's app: the sign-in service, its issuer and the applications it serves",
};

export function AuthPage() {
  const { page, href } = useForgeConsole();
  const tab: AuthTab = page.kind === "auth" ? (page.tab ?? "users") : "users";
  return (
    <div className="min-w-0 space-y-4" data-testid="console-auth" data-tab={tab}>
      <ConsolePageHeader
        density="compact"
        className="mb-0"
        title="Auth"
        subtitle={SUBTITLE[tab]}
      />
      <nav aria-label="Auth" className="-mt-1 flex gap-1 border-b border-border" data-testid="auth-tabs">
        {AUTH_TABS.map((candidate) => (
          <Link
            key={candidate}
            // The scope and nothing else: a selected user belongs to the Users tab.
            href={href({ kind: "auth", tab: candidate })}
            aria-current={candidate === tab ? "page" : undefined}
            data-testid={`auth-tab-${candidate}`}
            className={cn(
              "no-color -mb-px inline-flex h-9 items-center border-b-2 px-3 text-ui transition-colors",
              candidate === tab ? "border-primary font-medium text-ink" : "border-transparent text-ink-muted hover:text-ink"
            )}
          >
            {AUTH_TAB_LABELS[candidate]}
          </Link>
        ))}
      </nav>
      {tab === "users" ? (
        <ScopedIdentity layout="users" />
      ) : (
        <div className="grid grid-cols-1 items-start gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
          <div className="min-w-0">
            <ScopedIdentity layout="service" />
          </div>
          <HowSignInWorks />
        </div>
      )}
    </div>
  );
}

function Fact({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <h3 className="text-ui font-medium text-ink">{title}</h3>
      <div className="text-meta text-ink-muted">{children}</div>
    </div>
  );
}

/** How the app declares its sign-in, from forge's auth model: in code and KCL, never in this page. */
function HowSignInWorks() {
  return (
    <aside className="space-y-4 rounded-xl border border-border bg-surface px-4 py-4" data-testid="auth-how">
      <h2 className="text-section text-ink">Configure sign-in</h2>
      <Fact title="Declared in the repo">
        A forge app declares its users once, in <code className="font-mono text-code">deploy/kcl/identity.k</code> (
        <code className="font-mono text-code">forge.ManagedIdentity</code>: roles, machine users, seed users), and each environment imports it. Changing sign-in is a
        reviewed change and a deploy, not a setting here.
      </Fact>
      <Fact title="Where it runs">
        In dev, the identity provider <code className="font-mono text-code">forge env up</code> starts on your machine; in staging and prod, the sign-in service Reliant
        runs per environment, shown beside this.
      </Fact>
      <Fact title="Wired for you">
        forge fills the API&apos;s issuer, audience, client id and redirect from that declaration, and every RPC requires a signed-in caller unless its proto says{" "}
        <code className="font-mono text-code">auth_required: false</code>.
      </Fact>
      <Fact title="Your own provider">Auth0, Okta, Keycloak or another OIDC issuer: declare no identity for that environment and set its issuer in the environment&apos;s config.</Fact>
      <Link href={learnUrl("auth")} className="no-color inline-flex items-center gap-1 text-ui text-primary-ink hover:underline">
        Learn about sign-in <ExternalLink className="size-3.5" aria-hidden="true" />
      </Link>
    </aside>
  );
}
