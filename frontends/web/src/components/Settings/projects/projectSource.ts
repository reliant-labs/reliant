import type { Project } from "@/store/projectStore";

/**
 * Pure display helpers for Settings → Projects. No React, no stores, so the
 * table's "where does this project come from" column is unit-testable from
 * object literals.
 */

export interface RemoteDisplay {
  /** "github.com" — empty when the remote could not be parsed. */
  host: string;
  /** "owner/repo" — or the raw remote when it could not be parsed. */
  repo: string;
  /** A browsable https URL, only for hosts known to serve one at that path. */
  href?: string;
}

// Hosts whose web UI lives at https://<host>/<owner>/<repo>. Any other host
// (a self-hosted Gitea, a bare SSH server) gets a label but no link, because a
// guessed URL that 404s is worse than no link.
const BROWSABLE_HOSTS = new Set(["github.com", "gitlab.com", "bitbucket.org"]);

function stripRepoPath(path: string): string {
  return path.replace(/^\/+/, "").replace(/\/+$/, "").replace(/\.git$/, "");
}

/** Parse a git remote (scp-style, ssh://, https://) into host + owner/repo. */
export function describeRemote(remoteUrl: string): RemoteDisplay {
  const raw = remoteUrl.trim();

  // scp-like syntax: git@github.com:owner/repo.git
  const scp = /^(?:[^@/\s]+@)?([^:/\s]+):(?!\/\/)(.+)$/.exec(raw);
  if (scp && !/^[A-Za-z]:[\\/]/.test(raw)) {
    const host = scp[1].toLowerCase();
    const repo = stripRepoPath(scp[2]);
    return { host, repo, href: BROWSABLE_HOSTS.has(host) ? `https://${host}/${repo}` : undefined };
  }

  try {
    const url = new URL(raw);
    if (url.protocol === "file:") return { host: "", repo: raw };
    const host = url.hostname.toLowerCase();
    const repo = stripRepoPath(url.pathname);
    if (!host || !repo) return { host: "", repo: raw };
    const browsable = url.protocol === "https:" || url.protocol === "http:" || BROWSABLE_HOSTS.has(host);
    return { host, repo, href: browsable ? `https://${host}/${repo}` : undefined };
  } catch {
    return { host: "", repo: raw };
  }
}

export interface ProjectSource {
  /** The line a reader scans: "owner/repo", "Local repository", "Folder". */
  primary: string;
  /** Quieter context under it: host and default branch. */
  secondary?: string;
  /** True when `primary` is an identifier and should render mono. */
  mono: boolean;
}

export function describeProjectSource(project: Pick<Project, "is_git_repo" | "remote_url" | "default_branch">): ProjectSource {
  if (!project.is_git_repo) {
    return { primary: "Folder", secondary: "Not a git repository", mono: false };
  }
  const branch = project.default_branch || undefined;
  if (project.remote_url) {
    const remote = describeRemote(project.remote_url);
    const secondary = [remote.host, branch].filter(Boolean).join(" · ") || undefined;
    return { primary: remote.repo, secondary, mono: true };
  }
  return { primary: "Local repository", secondary: branch, mono: false };
}

/** Current project first (it is the one the user is in), then most recently active. */
export function sortProjectsForSettings<T extends Pick<Project, "id" | "last_active">>(
  projects: readonly T[],
  currentProjectId: string | undefined,
): T[] {
  return [...projects].sort((a, b) => {
    if (a.id === currentProjectId) return -1;
    if (b.id === currentProjectId) return 1;
    return (Date.parse(b.last_active) || 0) - (Date.parse(a.last_active) || 0);
  });
}

/** Case-insensitive match on name, path and remote. */
export function projectMatchesQuery(
  project: Pick<Project, "name" | "path" | "remote_url">,
  query: string,
): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return [project.name, project.path, project.remote_url ?? ""].some((value) =>
    value.toLowerCase().includes(needle),
  );
}
