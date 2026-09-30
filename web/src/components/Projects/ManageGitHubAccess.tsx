/**
 * ManageGitHubAccess — the "I can't see my repo" affordance.
 *
 * A repo missing from the picker means one of two things, and NEITHER is
 * fixed by reconnecting:
 *
 *   - the Reliant GitHub App is not installed on the account that owns it, or
 *   - it is installed with "selected repositories" and that repo isn't one.
 *
 * Reconnecting re-authorizes the USER. It does not change which accounts the
 * App is installed on, nor which repositories a selected-repositories
 * installation can see. Only GitHub's installation flow does that, which is
 * what this component links to. Conflating the two is what left the reporting
 * user clicking "Reconnect GitHub" and getting the same three repos back.
 *
 * Rendered in the clone dialog and in Settings → GitHub so the two places a
 * user looks tell the same story.
 *
 * ONE RULE ABOUT THE EMPTY STATE. "No GitHub accounts connected" is a claim
 * this component is usually not entitled to make. An empty installations list
 * is not proof of zero installations — the control plane only enumerates them
 * for a GitHub App user token, and returns an empty list for an OAuth-App
 * token, a PAT, or a failed probe. In prod that rendered "No GitHub accounts
 * connected" directly beneath the user's own repositories. So callers pass
 * installationsKnown=false whenever the list cannot be trusted (most
 * importantly: whenever repos are visible, which proves access exists), and
 * the component degrades to the neutral CTA, which is both actionable and
 * true. Never claim the negative without evidence for it.
 */
import { ExternalLink, Github } from "lucide-react";
import { cn } from "@/lib/utils";
import type { GitAppInstallation } from "@/services/controlPlane/git";

/**
 * index.css carries a global `a:not(.no-color) { color: hsl(var(--primary)) }`.
 * Its specificity (0,1,1) beats a utility class (0,1,0), so an anchor styled as
 * a filled button loses `text-primary-foreground` and is repainted in the
 * BACKGROUND colour it already sits on — white-on-white in dark mode. Opting
 * out by name is the established fix here (CopilotDevicePanel,
 * OnboardingChecklist do the same).
 */
const ANCHOR_KEEPS_ITS_OWN_COLOR = "no-color";

/** GitHub's own wording for the two installation scopes. */
function describeSelection(repositorySelection: string): string {
  return repositorySelection === "all" ? "All repositories" : "Selected repositories";
}

interface ManageGitHubAccessProps {
  /** GitHub's installation flow. Absent when the control plane has no App
   *  slug configured — the component then renders nothing rather than a
   *  link to github.com/apps//installations/new, which is a 404. */
  installUrl?: string;
  /** Installations the credential can reach. Empty is the loud case: it is
   *  why no private repo is visible — but see installationsKnown, because
   *  empty does NOT by itself mean "none installed". */
  installations: GitAppInstallation[];
  /**
   * Whether an empty `installations` can be believed as "nothing is
   * installed".
   *
   * It often cannot. The control plane only enumerates installations for a
   * GitHub App user token (`ghu_`); for a classic OAuth App token, a PAT, or
   * any failed /user/installations probe it returns an empty list, which is
   * indistinguishable from a genuine zero. Reported in prod: a user saw "No
   * GitHub accounts connected" directly above a working list of their repos.
   *
   * So the negative claim requires positive evidence. Pass false whenever
   * repos ARE visible (they prove access exists) or the credential is not an
   * App token; the component then shows the neutral "missing a repository?"
   * CTA, which is actionable and true either way.
   */
  installationsKnown?: boolean;
  /** "prominent" leads with the CTA (empty state, zero installations);
   *  "footer" is the quieter always-present form under a populated list. */
  variant?: "prominent" | "footer";
  /** Fired when the user leaves for GitHub, so the caller can arrange to
   *  re-fetch when they come back. */
  onNavigate?: () => void;
  className?: string;
}

export function ManageGitHubAccess({
  installUrl,
  installations,
  installationsKnown = true,
  variant = "footer",
  onNavigate,
  className,
}: ManageGitHubAccessProps) {
  if (!installUrl) return null;

  const hasInstallations = installations.length > 0;
  // "Nothing is installed" is a claim about the world, and an empty list is
  // only evidence for it when the list is trustworthy. Otherwise fall back to
  // the neutral wording: the CTA is the same either way, so the only thing a
  // wrong guess changes is whether we tell the user something false.
  const showNoAccounts = !hasInstallations && installationsKnown;
  // With nothing installed, no private repo is reachable at all — so the
  // install action IS the primary thing to do, whatever the caller asked for.
  const prominent = variant === "prominent" || showNoAccounts;

  return (
    <div
      className={cn(
        "rounded-lg border p-3 space-y-2.5",
        prominent
          ? "border-primary/30 bg-primary/5"
          : "border-border/40 bg-background",
        className,
      )}
    >
      <div className="space-y-1">
        <p className="text-sm font-semibold text-foreground">
          {showNoAccounts ? "No GitHub accounts connected" : "Missing a repository?"}
        </p>
        <p className="text-xs text-muted-foreground">
          {showNoAccounts
            ? "The Reliant GitHub App isn't installed on any account yet, so Reliant can't see your repositories."
            : "Reliant only sees repositories you've granted the GitHub App access to. Add another account, or grant access to more repos."}
        </p>
      </div>

      <a
        href={installUrl}
        target="_blank"
        rel="noopener noreferrer"
        onClick={onNavigate}
        className={cn(
          ANCHOR_KEEPS_ITS_OWN_COLOR,
          "inline-flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-semibold transition-colors",
          prominent
            ? "bg-primary text-primary-foreground hover:bg-primary/90"
            : "border border-border/60 text-foreground hover:bg-muted/50",
        )}
      >
        <Github className="h-4 w-4" />
        {showNoAccounts
          ? "Install the GitHub App"
          : "Add account or choose repositories"}
        <ExternalLink className="h-3 w-3 opacity-70" />
      </a>

      {hasInstallations && (
        <ul className="space-y-1 pt-0.5">
          {installations.map((installation) => (
            <li
              key={`${installation.accountType}:${installation.accountLogin}`}
              className="flex items-center justify-between gap-3 text-xs"
            >
              <span className="min-w-0 truncate">
                <span className="font-medium text-foreground">
                  {installation.accountLogin}
                </span>
                <span className="text-muted-foreground">
                  {" · "}
                  {describeSelection(installation.repositorySelection)}
                </span>
              </span>
              <a
                href={installation.configureUrl}
                target="_blank"
                rel="noopener noreferrer"
                onClick={onNavigate}
                className={cn(
                  ANCHOR_KEEPS_ITS_OWN_COLOR,
                  "flex-shrink-0 underline text-muted-foreground hover:text-foreground",
                )}
              >
                Configure
              </a>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
