import { useState, useMemo, useCallback, useRef, useEffect } from "react";
import {
  Search,
  Lock,
  Check,
  Loader2,
  Github,
  AlertCircle,
} from "lucide-react";
import { cn } from "../lib/utils";

export interface GitRepo {
  fullName: string;
  cloneUrl: string;
  defaultBranch: string;
  description?: string;
  isPrivate: boolean;
  language?: string;
  updatedAt?: string;
}

export interface RepoPickerProps {
  repos: GitRepo[];
  loading: boolean;
  error?: string | null;
  hasMore: boolean;
  selectedRepo?: GitRepo | null;
  onSelect: (repo: GitRepo) => void;
  onLoadMore: () => void;
  onSearch?: (query: string) => void;
  onConnectGitHub?: () => void;
  emptyMessage?: string;
}

function relativeTime(iso: string): string {
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  const months = Math.floor(days / 30);
  return `${months}mo ago`;
}

const LANGUAGE_COLORS: Record<string, string> = {
  TypeScript: "text-blue-500",
  JavaScript: "text-yellow-500",
  Python: "text-green-500",
  Go: "text-cyan-500",
  Rust: "text-orange-500",
  Java: "text-red-500",
  Ruby: "text-red-400",
  C: "text-gray-500",
  "C++": "text-pink-500",
  "C#": "text-purple-500",
  Swift: "text-orange-400",
  Kotlin: "text-violet-500",
  PHP: "text-indigo-400",
  Shell: "text-green-400",
  HTML: "text-orange-600",
  CSS: "text-blue-400",
};

export function RepoPicker({
  repos,
  loading,
  error,
  hasMore,
  selectedRepo,
  onSelect,
  onLoadMore,
  onSearch,
  onConnectGitHub,
  emptyMessage,
}: RepoPickerProps) {
  const [query, setQuery] = useState("");
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const handleQueryChange = useCallback(
    (value: string) => {
      setQuery(value);
      if (onSearch) {
        clearTimeout(debounceRef.current);
        debounceRef.current = setTimeout(() => onSearch(value), 300);
      }
    },
    [onSearch]
  );

  useEffect(() => {
    return () => clearTimeout(debounceRef.current);
  }, []);

  const filteredRepos = useMemo(() => {
    if (onSearch || !query.trim()) return repos;
    const q = query.toLowerCase();
    return repos.filter(
      (r) =>
        r.fullName.toLowerCase().includes(q) ||
        r.description?.toLowerCase().includes(q)
    );
  }, [repos, query, onSearch]);

  const isEmpty = filteredRepos.length === 0 && !loading;

  return (
    <div className="space-y-3">
      {/* Search bar */}
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground pointer-events-none" />
        <input
          type="text"
          value={query}
          onChange={(e) => handleQueryChange(e.target.value)}
          placeholder="Search repositories…"
          className={cn(
            "w-full pl-9 pr-3 py-2 text-sm",
            "bg-muted/50 border border-border rounded-md",
            "focus:outline-none focus:ring-1 focus:ring-primary/50 focus:border-primary/50",
            "placeholder:text-muted-foreground/50",
            "transition-all"
          )}
        />
      </div>

      {/* Error banner */}
      {error && (
        <div className="flex items-center gap-2 px-3 py-2 text-sm text-red-700 bg-red-50 border border-red-200 rounded-md">
          <AlertCircle className="w-4 h-4 flex-shrink-0" />
          <span>{error}</span>
        </div>
      )}

      {/* Repo list */}
      <div className="max-h-80 overflow-y-auto border border-border rounded-md divide-y divide-border">
        {/* Loading spinner (empty list) */}
        {loading && filteredRepos.length === 0 && (
          <div className="flex items-center justify-center py-10">
            <Loader2 className="w-5 h-5 text-muted-foreground animate-spin" />
          </div>
        )}

        {/* Empty: connect GitHub */}
        {isEmpty && onConnectGitHub && (
          <div className="flex flex-col items-center gap-3 py-10 px-4 text-center">
            <Github className="w-8 h-8 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              Connect GitHub to see your repos
            </p>
            <button
              type="button"
              onClick={onConnectGitHub}
              className="px-4 py-2 text-sm font-medium rounded-md bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
            >
              Connect GitHub
            </button>
          </div>
        )}

        {/* Empty: no callback */}
        {isEmpty && !onConnectGitHub && (
          <div className="flex items-center justify-center py-10 text-sm text-muted-foreground">
            {emptyMessage || "No repositories found"}
          </div>
        )}

        {/* Repo rows */}
        {filteredRepos.map((repo) => {
          const isSelected =
            selectedRepo?.fullName === repo.fullName;
          return (
            <button
              key={repo.fullName}
              type="button"
              onClick={() => onSelect(repo)}
              className={cn(
                "w-full flex items-center gap-3 px-3 py-2.5 text-left transition-colors",
                isSelected
                  ? "bg-primary/10 ring-2 ring-inset ring-primary/30"
                  : "hover:bg-muted/50"
              )}
            >
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-1.5">
                  <span className="text-sm font-semibold text-foreground truncate">
                    {repo.fullName}
                  </span>
                  {repo.isPrivate && (
                    <Lock className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                  )}
                  {repo.language && (
                    <span
                      className={cn(
                        "text-xs flex-shrink-0",
                        LANGUAGE_COLORS[repo.language] ||
                          "text-muted-foreground"
                      )}
                    >
                      {repo.language}
                    </span>
                  )}
                </div>
                {repo.description && (
                  <p className="text-xs text-muted-foreground truncate mt-0.5">
                    {repo.description}
                  </p>
                )}
              </div>

              {repo.updatedAt && (
                <span className="text-xs text-muted-foreground whitespace-nowrap flex-shrink-0">
                  {relativeTime(repo.updatedAt)}
                </span>
              )}

              {isSelected && (
                <Check className="w-4 h-4 text-primary flex-shrink-0" />
              )}
            </button>
          );
        })}

        {/* Loading spinner (incremental) */}
        {loading && filteredRepos.length > 0 && (
          <div className="flex items-center justify-center py-3">
            <Loader2 className="w-4 h-4 text-muted-foreground animate-spin" />
          </div>
        )}

        {/* Load more */}
        {hasMore && !loading && filteredRepos.length > 0 && (
          <div className="flex items-center justify-center py-2">
            <button
              type="button"
              onClick={onLoadMore}
              className="text-sm text-primary hover:text-primary/80 transition-colors"
            >
              Load more
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
