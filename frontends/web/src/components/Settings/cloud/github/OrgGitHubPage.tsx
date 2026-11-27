// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Connections › Organization GitHub (`/settings/org-github`): the
 * Reliant GitHub App installations linked to the active org
 * (control-plane docs/design/org-sharing.md §7.1). A link is what lets the
 * org's automations push as the App's bot, and what maps a repository to the
 * org when its GitHub Actions sign in to Reliant with GitHub's OIDC token
 * (controlplane.org_github_installations). It grants nobody new access: every
 * use still needs the acting member's own access to the repository.
 *
 *   Linked installations   every member reads them; owners and admins unlink
 *   Link an installation   owners and admins:
 *     1. connect their own GitHub account (Reliant links only an installation
 *        that account can see: GitHub's installation id is spoofable);
 *     2. link one the App already has (ListLinkableGitHubAppInstallations,
 *        then StartGitHubAppInstall's state to LinkGitHubAppInstallation), or
 *        install the App on another account: StartGitHubAppInstall's URL, and
 *        GitHub's setup redirect comes back through /auth/github/callback to
 *        this page with `installation_id` and `link_state`, which it links.
 *
 * Keyed by org: a switch remounts the page, so nothing of one org can be acted
 * on in the next.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { CheckCircle2, Github } from "lucide-react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import type { GitHubAppInstallation, LinkableGitHubAppInstallation } from "@/gen/controlplane/services/organization/v1/organization_pb";
import {
  useLinkGitHubAppInstallation,
  useListGitHubAppInstallations,
  useListLinkableGitHubAppInstallations,
  useStartGitHubAppInstall,
  useUnlinkGitHubAppInstallation,
} from "@/hooks/controlplane/organization-service-hooks_gen";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import { useActiveOrg } from "@/hooks/useOrgs";
import { isElectron } from "@/lib/constants";
import { connectGitHub } from "@/lib/github-connect";
import { openExternalLink } from "@/lib/open-link";
import { cn } from "@/lib/utils";
import { canAdministerMembers } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { SettingsPageHeader } from "../../SettingsPageHeader";
import { githubLinkErrorMessage, needsGitHubConnection } from "./githubLinkErrors";

const TITLE = "Organization GitHub";

/** Errors render inline on this page, never as the app-wide toast too. */
const SILENT = { meta: { silenceErrorToast: true } } as const;

type Notice = { kind: "ok" | "info" | "error"; text: string };

export function OrgGitHubPage() {
  const { active, isLoading } = useActiveOrg();
  if (!active) {
    return (
      <div className="space-y-6">
        <SettingsPageHeader title={TITLE} description="The Reliant GitHub App installations linked to your organization." />
        <p className="text-sm text-muted-foreground" role="status">
          {isLoading ? "Loading your organization…" : "You're not in an organization yet."}
        </p>
      </div>
    );
  }
  return <OrgGitHub key={active.id} org={active} />;
}

function OrgGitHub({ org }: { org: MyOrg }) {
  const { orgs } = useActiveOrg();
  const isAdmin = canAdministerMembers(org.role);
  const linked = useListGitHubAppInstallations({ orgId: org.id }, { enabled: !!org.id });
  const start = useStartGitHubAppInstall(SILENT);
  const link = useLinkGitHubAppInstallation(SILENT);
  const unlink = useUnlinkGitHubAppInstallation(SILENT);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [unlinking, setUnlinking] = useState<GitHubAppInstallation | null>(null);
  const [linkingId, setLinkingId] = useState<bigint | null>(null);

  const linkInstallation = useCallback(
    async (installationId: bigint, state: string) => {
      setNotice(null);
      setLinkingId(installationId);
      try {
        const res = await link.mutateAsync({ installationId, state });
        const account = res.installation?.accountLogin ? `@${res.installation.accountLogin}` : "The installation";
        const target = res.installation?.orgId;
        const into = !target || target === org.id ? org.name : (orgs.find((o) => o.id === target)?.name ?? "another of your organizations");
        setNotice({ kind: "ok", text: `${account} is linked to ${into}.` });
      } catch (err) {
        setNotice({ kind: "error", text: githubLinkErrorMessage(err, org.name, "The installation could not be linked. Try again.") });
      } finally {
        setLinkingId(null);
      }
    },
    [link, org.id, org.name, orgs],
  );

  // Back from GitHub's install page (GitHubOAuthCallback forwards the setup
  // redirect here): link what it named, once, and take both values off the
  // URL so a refresh does not try again. The server trusts neither.
  const search = useSearch({ strict: false }) as { installation_id?: string; link_state?: string };
  const navigate = useNavigate();
  const returned = useRef(false);
  useEffect(() => {
    if (returned.current || !search.link_state) return;
    returned.current = true;
    const { installation_id: rawId, link_state: state } = search;
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, installation_id: undefined, link_state: undefined }),
      replace: true,
    });
    let installationId: bigint | null = null;
    try {
      installationId = rawId && /^\d+$/.test(rawId) ? BigInt(rawId) : null;
    } catch {
      installationId = null;
    }
    if (installationId === null || installationId <= 0n) {
      setNotice({
        kind: "info",
        text: "GitHub didn't name an installation. If the App is waiting for an owner of that GitHub account to approve it, link it here once they have.",
      });
      return;
    }
    void linkInstallation(installationId, state);
  }, [search, navigate, linkInstallation]);

  const linkExisting = async (installationId: bigint) => {
    setNotice(null);
    setLinkingId(installationId);
    try {
      const { state } = await start.mutateAsync({ orgId: org.id });
      await linkInstallation(installationId, state);
    } catch (err) {
      setLinkingId(null);
      setNotice({ kind: "error", text: githubLinkErrorMessage(err, org.name, "The installation could not be linked. Try again.") });
    }
  };

  const install = async () => {
    setNotice(null);
    try {
      const { installUrl } = await start.mutateAsync({ orgId: org.id });
      if (isElectron()) {
        // The desktop app sends GitHub to the system browser; the new
        // installation is in the list below when the person comes back.
        await openExternalLink(installUrl);
        setNotice({ kind: "info", text: "Finish installing the App in your browser. The new installation appears below when you come back." });
        return;
      }
      window.location.assign(installUrl);
    } catch (err) {
      setNotice({ kind: "error", text: githubLinkErrorMessage(err, org.name, "GitHub's install page could not be opened. Try again.") });
    }
  };

  const confirmUnlink = async () => {
    const target = unlinking;
    setUnlinking(null);
    if (!target) return;
    setNotice(null);
    try {
      await unlink.mutateAsync({ orgId: org.id, installationId: target.installationId });
      setNotice({ kind: "ok", text: `@${target.accountLogin} is unlinked from ${org.name}.` });
    } catch (err) {
      setNotice({ kind: "error", text: githubLinkErrorMessage(err, org.name, "The installation could not be unlinked. Try again.") });
    }
  };

  return (
    <div className="forge-ui space-y-6" data-testid="org-github-page">
      <SettingsPageHeader
        title={TITLE}
        description={`Link the Reliant GitHub App's installations to ${org.name}. Its automations can then push as the App, and GitHub Actions in those repositories can sign in to ${org.name} without a stored token. Linking gives nobody new access to a repository.`}
      />

      {notice && <NoticeLine notice={notice} />}

      <section aria-labelledby="org-github-linked-heading" className="rounded-lg border border-border bg-card p-4" data-testid="org-github-linked">
        <h2 id="org-github-linked-heading" className="text-sm font-semibold text-foreground">
          Linked installations
        </h2>
        {linked.isLoading ? (
          <p className="mt-3 text-sm text-muted-foreground" role="status">
            Loading…
          </p>
        ) : linked.error ? (
          <div role="alert" className="mt-3 flex flex-wrap items-center justify-between gap-3 text-sm" data-testid="org-github-linked-error">
            <span className="text-foreground">{githubLinkErrorMessage(linked.error, org.name, "The linked installations could not be loaded.")}</span>
            <Button variant="outline" size="sm" onClick={() => void linked.refetch()}>
              Try again
            </Button>
          </div>
        ) : (linked.data?.installations.length ?? 0) === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground" data-testid="org-github-linked-empty">
            {isAdmin ? `No installation is linked to ${org.name} yet. Link one below.` : `No installation is linked to ${org.name} yet.`}
          </p>
        ) : (
          <ul className="mt-3 divide-y divide-border/60 rounded-md border border-border/60 bg-background">
            {linked.data?.installations.map((inst) => (
              <li key={String(inst.installationId)} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5" data-testid="org-github-linked-row">
                <Github className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-baseline gap-x-2">
                    <span className="truncate text-sm font-medium text-foreground">{inst.accountLogin || `Installation ${inst.installationId}`}</span>
                    {inst.suspendedAt && <span className="text-xs font-medium text-warning-ink">Suspended on GitHub</span>}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {accountKind(inst.accountType)}
                    {inst.linkedAt && ` · Linked ${formatDay(inst.linkedAt)}`}
                  </p>
                </div>
                {isAdmin && (
                  <Button variant="ghost" size="sm" onClick={() => setUnlinking(inst)} aria-label={`Unlink ${inst.accountLogin}`} data-testid="org-github-unlink">
                    Unlink
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      {isAdmin ? (
        <LinkSection org={org} linkingId={linkingId} onLink={(id) => void linkExisting(id)} onInstall={() => void install()} installing={start.isPending && linkingId === null} />
      ) : (
        <p role="note" className="text-sm text-muted-foreground" data-testid="org-github-member-note">
          Only owners and admins of {org.name} can link or unlink GitHub App installations.
        </p>
      )}

      <ConfirmationDialog
        open={unlinking !== null}
        title={unlinking ? `Unlink @${unlinking.accountLogin}?` : "Unlink installation?"}
        description={`${org.name}'s automations can no longer push as the App to its repositories, and their GitHub Actions can no longer sign in to ${org.name}. The App stays installed on GitHub.`}
        confirmLabel="Unlink"
        variant="danger"
        loading={unlink.isPending}
        onCancel={() => setUnlinking(null)}
        onConfirm={() => void confirmUnlink()}
      />
    </div>
  );
}

interface LinkSectionProps {
  org: MyOrg;
  linkingId: bigint | null;
  installing: boolean;
  onLink: (installationId: bigint) => void;
  onInstall: () => void;
}

/** Step 1, the admin's own GitHub account; step 2, link one the App has or install it anew. */
function LinkSection({ org, linkingId, installing, onLink, onInstall }: LinkSectionProps) {
  const credential = useGitHubCredential();
  const connected = credential.hasToken;
  const linkable = useListLinkableGitHubAppInstallations({ orgId: org.id }, { enabled: !!org.id && connected, refetchOnWindowFocus: true, retry: false });
  const [connecting, setConnecting] = useState(false);
  const [connectError, setConnectError] = useState<string | null>(null);

  const connect = async () => {
    setConnecting(true);
    setConnectError(null);
    const outcome = await connectGitHub();
    if (outcome.kind === "redirecting") return;
    if (outcome.kind === "error") setConnectError(outcome.message);
    if (outcome.kind === "connected") await credential.refresh();
    setConnecting(false);
  };

  const connectButton = (label: string) => (
    <Button variant="primary" leftIcon={<Github />} onClick={() => void connect()} loading={connecting} data-testid="org-github-connect">
      {label}
    </Button>
  );

  const installations = linkable.data?.installations ?? [];
  const reconnect = linkable.error && needsGitHubConnection(linkable.error);

  return (
    <section aria-labelledby="org-github-link-heading" className="rounded-lg border border-border bg-card p-4" data-testid="org-github-link">
      <h2 id="org-github-link-heading" className="text-sm font-semibold text-foreground">
        Link an installation
      </h2>
      {credential.isLoading ? (
        <p className="mt-3 text-sm text-muted-foreground" role="status">
          Checking your GitHub connection…
        </p>
      ) : !connected || reconnect ? (
        <div className="mt-3 space-y-3" data-testid="org-github-connect-step">
          <p className="text-sm text-muted-foreground">
            {reconnect
              ? "GitHub rejected your saved sign-in. Reconnect GitHub to see the installations your account can reach."
              : "First connect your own GitHub account. Reliant links only an installation your GitHub account can see."}
          </p>
          {connectButton(reconnect ? "Reconnect GitHub" : "Connect GitHub")}
          {connectError && (
            <p role="alert" className="text-sm text-destructive-ink">
              {connectError}
            </p>
          )}
        </div>
      ) : (
        <div className="mt-1 space-y-4">
          <p className="text-sm text-muted-foreground">
            {credential.accountLogin ? `The Reliant GitHub App's installations that @${credential.accountLogin} can see.` : "The Reliant GitHub App's installations your GitHub account can see."}
          </p>
          {linkable.isLoading ? (
            <p className="text-sm text-muted-foreground" role="status">
              Asking GitHub…
            </p>
          ) : linkable.error ? (
            <div role="alert" className="flex flex-wrap items-center justify-between gap-3 text-sm" data-testid="org-github-linkable-error">
              <span className="text-foreground">{githubLinkErrorMessage(linkable.error, org.name, "Your installations could not be listed.")}</span>
              <Button variant="outline" size="sm" onClick={() => void linkable.refetch()}>
                Try again
              </Button>
            </div>
          ) : installations.length === 0 ? (
            <p className="text-sm text-muted-foreground" data-testid="org-github-linkable-empty">
              The App isn't installed on any GitHub account or organization you can see.
            </p>
          ) : (
            <ul className="divide-y divide-border/60 rounded-md border border-border/60 bg-background">
              {installations.map((inst) => (
                <LinkableRow key={String(inst.installationId)} inst={inst} busy={linkingId === inst.installationId} disabled={linkingId !== null} onLink={() => onLink(inst.installationId)} />
              ))}
            </ul>
          )}
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border/60 pt-4">
            <p className="text-sm text-muted-foreground">
              {installations.length === 0 ? "Install it on the GitHub account or organization that owns your repositories." : "Not listed? Install the App on another GitHub account or organization."}
            </p>
            <Button variant={installations.length === 0 && !linkable.isLoading ? "primary" : "outline"} onClick={onInstall} loading={installing} data-testid="org-github-install">
              Install the GitHub App
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}

function LinkableRow({ inst, busy, disabled, onLink }: { inst: LinkableGitHubAppInstallation; busy: boolean; disabled: boolean; onLink: () => void }) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5" data-testid="org-github-linkable-row">
      {inst.avatarUrl ? (
        <img src={inst.avatarUrl} alt="" className="h-6 w-6 shrink-0 rounded-full border border-border" />
      ) : (
        <Github className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="truncate text-sm font-medium text-foreground">{inst.accountLogin}</span>
          {inst.suspendedAt && <span className="text-xs font-medium text-warning-ink">Suspended on GitHub</span>}
        </div>
        <p className="text-xs text-muted-foreground">
          {accountKind(inst.accountType)}
          {repositoryScope(inst.repositorySelection) && ` · ${repositoryScope(inst.repositorySelection)}`}
        </p>
      </div>
      {inst.linkedHere ? (
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground" data-testid="org-github-linkable-state">
          <CheckCircle2 className="h-3.5 w-3.5 text-success-ink" aria-hidden="true" />
          Linked here
        </span>
      ) : inst.linkedElsewhere ? (
        <span className="text-xs text-muted-foreground" data-testid="org-github-linkable-state">
          Linked to another organization
        </span>
      ) : (
        <Button variant="outline" size="sm" onClick={onLink} loading={busy} disabled={disabled && !busy} aria-label={`Link ${inst.accountLogin}`} data-testid="org-github-link-button">
          Link
        </Button>
      )}
    </li>
  );
}

function NoticeLine({ notice }: { notice: Notice }) {
  return (
    <p
      role={notice.kind === "error" ? "alert" : "status"}
      className={cn(
        "flex items-start gap-2 rounded-md border bg-background px-3 py-2 text-sm",
        notice.kind === "error" ? "border-destructive/40 text-destructive-ink" : "border-border/60 text-foreground",
      )}
      data-testid="org-github-notice"
    >
      {notice.kind === "ok" && <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-success-ink" aria-hidden="true" />}
      {notice.text}
    </p>
  );
}

/** GitHub's account type, in words. */
function accountKind(type: string): string {
  if (type === "Organization") return "GitHub organization";
  if (type === "User") return "Personal account";
  return "GitHub account";
}

/** The installation's repository scope, in words, or "" when GitHub did not say. */
function repositoryScope(selection: string): string {
  if (selection === "all") return "All repositories";
  if (selection === "selected") return "Selected repositories";
  return "";
}

function formatDay(ts: Timestamp): string {
  return timestampDate(ts).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" });
}
