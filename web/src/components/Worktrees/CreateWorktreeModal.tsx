import { useState, useMemo, useEffect, type ReactNode } from "react";
import { FileText, Cloud, FolderGit2, AlertCircle, GitCommitHorizontal, ChevronDown, ChevronRight } from "lucide-react";
import { Modal } from "../ui/Modal";
import { SearchableDropdown, type DropdownOption } from "../ui/SearchableDropdown";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useBranches } from "../../hooks/useBranches";
import { InitializeGitModal } from "../Git/InitializeGitModal";
import { Button } from "../ui/Button";
import { cn } from "../../lib/utils";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { repoGrpc, type Repo } from "../../api/repo-grpc";
import { worktreeGrpc } from "../../api/worktree-grpc";
import { toast } from "../../lib/toast-manager";
import { logger } from "../../lib/logger";

interface CreateWorktreeModalProps {
  isOpen: boolean;
  onClose: () => void;
  onWorktreeCreated: (worktreeId: string) => void | Promise<void>;
  projectId: string;
  title?: string; // Optional custom title
  sourceWorktreeId?: string; // Source worktree to copy files from (for branch to workspace)
  sourceWorktreeBranch?: string; // Branch of source worktree to use as default base branch
  additionalCopyFiles?: string[]; // Additional files to copy (e.g., modified/untracked files from source)
  lockBaseBranch?: boolean; // If true, prevent changing the base branch (used when branching to workspace)
  extraContent?: ReactNode; // Additional content to render in the form (e.g., copy files toggle)
}

export function CreateWorktreeModal({
  isOpen,
  onClose,
  onWorktreeCreated,
  projectId,
  title = "Create New Workspace", // Default title
  sourceWorktreeId,
  sourceWorktreeBranch,
  additionalCopyFiles = [],
  lockBaseBranch = false,
  extraContent,
}: CreateWorktreeModalProps) {
  const createWorktree = useWorktreeStore((state) => state.createWorktree);
  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const [showInitGitModal, setShowInitGitModal] = useState(false);
  // Only fetch branches when the modal is actually open to avoid duplicate RPCs
  // (multiple CreateWorktreeModal instances mount on page load with isOpen=false)
  const { branches, isLoading: isBranchesLoading, error: branchesError, refetch: refetchBranches } = useBranches(isOpen ? projectId : undefined);

  // Find the default base branch:
  // 1. If sourceWorktreeBranch is provided (branching from existing workspace), use it
  // 2. Otherwise use the current branch from branches list
  //    - If current branch is in detached HEAD state, use the commit SHA
  // 3. Fallback to "main"
  const defaultBaseBranch = useMemo(() => {
    if (sourceWorktreeBranch) {
      return sourceWorktreeBranch;
    }
    const currentBranch = branches.find((b) => b.is_current && !b.is_remote);
    if (currentBranch) {
      // If in detached HEAD state, use the commit SHA instead of the name
      if (currentBranch.is_detached && currentBranch.commit_sha) {
        return currentBranch.commit_sha;
      }
      return currentBranch.name;
    }
    return "main";
  }, [branches, sourceWorktreeBranch]);

  const [formData, setFormData] = useState({
    name: "",
    branch: "",                                  // override; empty → derived from name
    base_branch: defaultBaseBranch,              // single-repo case
    base_branches: {} as Record<string, string>, // multi-repo per-repo overrides; empty value → daemon auto-detect
    copy_files: [".env", ".env.local"] as string[],
    force: false,
  });
  const [advancedOpen, setAdvancedOpen] = useState(false);

  // Refetch branches when modal opens
  useEffect(() => {
    if (isOpen) {
      refetchBranches();
    }
  }, [isOpen, refetchBranches]);

  // Update base_branch when branches load or sourceWorktreeBranch is provided
  useEffect(() => {
    // If sourceWorktreeBranch is set (from git status), always use it as the base branch
    if (sourceWorktreeBranch) {
      setFormData(prev => ({ ...prev, base_branch: sourceWorktreeBranch }));
    } else if (branches.length > 0) {
      // Always update to the default base branch (current branch from useBranches) when branches load
      setFormData(prev => ({ ...prev, base_branch: defaultBaseBranch }));
    }
  }, [branches, defaultBaseBranch, sourceWorktreeBranch]);
  const [customFilesInput, setCustomFilesInput] = useState(".env, .env.local");
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [normalizedName, setNormalizedName] = useState<string | null>(null);
  const [normalizedBranch, setNormalizedBranch] = useState<string | null>(null);

  // Discovered nested repos. The new worktree always spans ALL of them;
  // there's no per-repo selection. We still load the list so we can show
  // per-repo base-branch overrides when there are 2+ repos.
  const [repos, setRepos] = useState<Repo[]>([]);

  useEffect(() => {
    if (!isOpen || !projectId) return;
    let cancelled = false;
    repoGrpc
      .list(projectId)
      .then(({ repos: loaded }) => {
        if (cancelled) return;
        setRepos(loaded);
      })
      .catch((err) => {
        // Non-fatal: fall back to single-repo path if RepoService is unavailable.
        if (cancelled) return;
        logger.warn("[CreateWorktreeModal] Failed to list repos", { err });
        setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [isOpen, projectId]);

  const isMultiRepo = repos.length > 1;

  // Helper function to normalize names (replace spaces with hyphens, trim trailing spaces)
  const normalizeName = (value: string): string => {
    return value.trim().replace(/\s+/g, "-");
  };

  // Transform branches into dropdown options with main/current at top
  const branchOptions = useMemo<DropdownOption[]>(() => {
    const localBranches = branches
      .filter((b) => !b.is_remote)
      .map((b) => {
        // For detached HEAD, use commit SHA as value and show commit indicator
        if (b.is_detached && b.commit_sha) {
          return {
            value: b.commit_sha, // Use full SHA for git operations
            label: b.name, // Short SHA for display
            description: "detached HEAD",
            icon: <GitCommitHorizontal className="w-3.5 h-3.5" />,
            group: "Current State",
            commitAge: b.last_commit_age || 0, // Detached HEAD is always "most recent"
          };
        }
        return {
          value: b.name,
          label: b.name,
          description: b.is_current ? "current" : undefined,
          icon: <FolderGit2 className="w-3.5 h-3.5" />,
          group: "Local Branches",
          commitAge: b.last_commit_age || Infinity,
        };
      });

    // Sort: detached HEAD first, then main, then by most recent commit
    localBranches.sort((a, b) => {
      // Detached HEAD (Current State group) always first
      if (a.group === "Current State") return -1;
      if (b.group === "Current State") return 1;
      // Main branch next
      if (a.value === "main") return -1;
      if (b.value === "main") return 1;
      // Then sort by commit age (most recent first = smaller age value)
      return a.commitAge - b.commitAge;
    });

    const remoteBranches = branches
      .filter((b) => b.is_remote)
      .map((b) => ({
        value: b.name,
        label: b.name.replace(/^origin\//, ''),
        description: "remote",
        icon: <Cloud className="w-3.5 h-3.5" />,
        group: "Remote Branches",
      }));

    return [...localBranches, ...remoteBranches];
  }, [branches]);

  const resetFormState = () => {
    setFormData({
      name: "",
      branch: "",
      base_branch: defaultBaseBranch,
      base_branches: {},
      copy_files: [".env", ".env.local"],
      force: false,
    });
    setNormalizedName(null);
    setNormalizedBranch(null);
    setCustomFilesInput(".env, .env.local");
    setAdvancedOpen(false);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!formData.name.trim()) {
      setError("Name is required");
      return;
    }

    setIsCreating(true);
    setError(null);

    // Name is primary; branch override falls back to normalized name.
    const finalName = normalizeName(formData.name);
    const finalBranch = formData.branch.trim()
      ? normalizeName(formData.branch)
      : finalName;

    try {
      // Merge default copy_files with any additional files (e.g., modified/untracked files from source worktree)
      const allCopyFiles = [...new Set([...formData.copy_files, ...additionalCopyFiles])];

      // Multi-repo branch: project has >1 nested repo. The worktree always
      // spans all of them; only per-repo base-branch overrides are user-tunable.
      if (isMultiRepo) {
        const repoIds = repos.map((r) => r.id);
        // Only send entries the user actually filled in. Empty values fall
        // back to daemon-side per-repo default-branch detection.
        const baseBranchesMap: Record<string, string> = {};
        for (const id of repoIds) {
          const v = formData.base_branches[id]?.trim();
          if (v) baseBranchesMap[id] = v;
        }
        const result = await worktreeGrpc.batchCreate(
          projectId,
          repoIds,
          finalName,
          finalBranch,
          {
            baseBranches: baseBranchesMap,
            copyFiles: allCopyFiles,
            force: formData.force,
          }
        );

        if (result.all_succeeded) {
          const firstWorktreeId = result.results.find((r) => r.worktree)?.worktree?.id;
          if (firstWorktreeId) {
            await onWorktreeCreated(firstWorktreeId);
          }
          toast.success(
            `Workspace "${finalName}" created in ${result.results.length} repos`,
            { duration: 4000 }
          );
          onClose();
          resetFormState();
        } else {
          const failed = result.results.filter((r) => r.error);
          const lines = failed.map((r) => {
            const repoName = repos.find((rr) => rr.id === r.repo_id)?.name || r.repo_id;
            return `${repoName}: ${r.error}`;
          });
          const heading = result.rolled_back
            ? "Batch creation failed; rolled back successful creates"
            : "Batch creation failed";
          toast.error(`${heading}\n${lines.join("\n")}`);
          setError(`${heading}: ${lines.join("; ")}`);
        }
        return;
      }

      // Single-repo (or no-repos-discovered) path: legacy single-create RPC.
      const worktree = await createWorktree({
        name: finalName,
        branch: finalBranch,
        base_branch: formData.base_branch,
        project_id: projectId,
        copy_files: allCopyFiles,
        status: WorktreeStatus.ACTIVE,
        force: formData.force,
        source_worktree_id: sourceWorktreeId,
      });

      await onWorktreeCreated(worktree.id);
      onClose();
      resetFormState();
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to create worktree"
      );
    } finally {
      setIsCreating(false);
    }
  };

  const handleInitGitSuccess = async () => {
    // Refresh the project to update is_git_repo status
    await refreshCurrentProject();
    // Refetch branches now that git is initialized
    await refetchBranches();
    setShowInitGitModal(false);
    // The modal stays open, now showing the workspace creation form
  };

  // Check if project is not a git repo
  const isNotGitRepo = currentProject && !currentProject.is_git_repo;

  // If project is not a git repo, show prompt to initialize git
  if (isOpen && isNotGitRepo) {
    return (
      <>
        <Modal
          isOpen={isOpen && !showInitGitModal}
          onClose={onClose}
          title="Git Repository Required"
          size="md"
        >
          <div className="space-y-6">
            <div className="flex flex-col items-center justify-center py-6 text-center">
              <div className="p-4 rounded-full bg-warning/10 ring-1 ring-warning/20 mb-4">
                <AlertCircle className="w-8 h-8 text-warning" />
              </div>
              <h3 className="text-sm font-semibold text-foreground mb-2">
                Git Repository Required
              </h3>
              <p className="text-sm text-muted-foreground max-w-sm">
                Workspaces require a git repository. Initialize git for this project to enable workspace management.
              </p>
            </div>

            <div className="flex gap-3 pt-4 border-t border-border">
              <Button
                onClick={onClose}
                variant="secondary"
                className="flex-1"
              >
                Cancel
              </Button>
              <Button
                onClick={() => setShowInitGitModal(true)}
                leftIcon={<FolderGit2 className="w-4 h-4" />}
                variant="primary"
                className="flex-1"
              >
                Initialize Git
              </Button>
            </div>
          </div>
        </Modal>

        {currentProject && (
          <InitializeGitModal
            isOpen={showInitGitModal}
            onClose={() => setShowInitGitModal(false)}
            onSuccess={handleInitGitSuccess}
            projectId={projectId}
            projectName={currentProject.name}
          />
        )}
      </>
    );
  }

  return (
    <>
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={title}
      size="lg"
    >
      <form onSubmit={handleSubmit} className="space-y-6">
        {error && (
          <div className="p-4 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg text-sm">
            <div className="flex items-start gap-2">
              <span className="text-destructive mt-0.5">⚠️</span>
              <span className="flex-1">{error}</span>
            </div>
          </div>
        )}

        <div className="space-y-5">
          <div className="space-y-2">
            <label className="block text-sm font-semibold text-foreground">
              Name <span className="text-destructive">*</span>
            </label>
            <div className="relative">
              <FolderGit2 className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
              <input
                type="text"
                value={formData.name}
                onChange={(e) => {
                  const value = e.target.value;
                  setFormData((prev) => ({ ...prev, name: value }));

                  // Show normalized name if spaces are present
                  const normalized = normalizeName(value);
                  if (value !== normalized && value.trim() !== "") {
                    setNormalizedName(normalized);
                  } else {
                    setNormalizedName(null);
                  }
                }}
                className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                placeholder="awesome-feature"
                required
                autoFocus
              />
            </div>
            {normalizedName && (
              <div className="p-2 bg-info/10 border border-info/20 rounded-lg">
                <p className="text-xs text-info font-mono">
                  Workspace and branch will use "{normalizedName}"
                </p>
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              Used as the workspace name and the new branch name. Override the branch in Advanced.
            </p>
          </div>

          <div className="border-t border-border pt-4">
            <button
              type="button"
              onClick={() => setAdvancedOpen((v) => !v)}
              className="flex items-center gap-2 text-sm font-semibold text-muted-foreground hover:text-foreground transition-colors"
            >
              {advancedOpen ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
              Advanced
            </button>

            {advancedOpen && (
              <div className="space-y-5 mt-4">
                <div className="space-y-2">
                  <label className="block text-sm font-semibold text-foreground">
                    Branch Name
                  </label>
                  <div className="relative">
                    <FolderGit2 className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                    <input
                      type="text"
                      value={formData.branch}
                      onChange={(e) => {
                        const value = e.target.value;
                        setFormData((prev) => ({ ...prev, branch: value }));
                        const normalized = normalizeName(value);
                        if (value !== normalized && value.trim() !== "") {
                          setNormalizedBranch(normalized);
                        } else {
                          setNormalizedBranch(null);
                        }
                      }}
                      className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                      placeholder={formData.name ? normalizeName(formData.name) : "Defaults to name"}
                    />
                  </div>
                  {normalizedBranch && (
                    <div className="p-2 bg-info/10 border border-info/20 rounded-lg">
                      <p className="text-xs text-info font-mono">
                        Branch will be "{normalizedBranch}"
                      </p>
                    </div>
                  )}
                </div>

                {/* Multi-repo: per-repo base branch inputs. Single-repo (or no
                    repos discovered): project-level dropdown. The worktree
                    always spans every discovered repo — only the per-repo base
                    branch is user-tunable. */}
                {isMultiRepo ? (
                  <div className="space-y-2">
                    <label className="block text-sm font-semibold text-foreground">
                      Base Branch (per repo)
                    </label>
                    <div className="elevation-0 border border-border/60 rounded-lg divide-y divide-border/60">
                      {repos.map((r) => (
                        <div key={r.id} className="flex items-center gap-3 p-3">
                          <div className="w-1/3 min-w-0">
                            <div className="text-sm font-medium text-foreground truncate">{r.name}</div>
                            {r.relative_path && (
                              <div className="text-xs text-muted-foreground font-mono truncate">{r.relative_path}</div>
                            )}
                          </div>
                          <input
                            type="text"
                            value={formData.base_branches[r.id] || ""}
                            onChange={(e) => {
                              const v = e.target.value;
                              setFormData((prev) => ({
                                ...prev,
                                base_branches: { ...prev.base_branches, [r.id]: v },
                              }));
                            }}
                            className="flex-1 px-3 py-2 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                            placeholder="Auto-detect (e.g. main, master, develop)"
                          />
                        </div>
                      ))}
                    </div>
                    <p className="text-xs text-muted-foreground">
                      Leave blank to auto-detect each repo's default branch. The
                      workspace is created across all repos.
                    </p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <label className="block text-sm font-semibold text-foreground">
                      Base Branch
                    </label>
                    <SearchableDropdown
                      options={branchOptions}
                      value={formData.base_branch}
                      placeholder={isBranchesLoading ? "Loading branches..." : "Select base branch"}
                      emptyMessage={branchesError ? "Failed to load branches" : "No branches found"}
                      onSelect={(value) =>
                        setFormData((prev) => ({ ...prev, base_branch: value || "" }))
                      }
                      disabled={isBranchesLoading || lockBaseBranch}
                      groupBy={true}
                      variant="form"
                      className="w-full"
                    />
                    {branchesError ? (
                      <p className="text-xs text-destructive flex items-center gap-1">
                        <span>⚠️</span>
                        <span>{branchesError}</span>
                      </p>
                    ) : (
                      <p className="text-xs text-muted-foreground">
                        {lockBaseBranch
                          ? "New workspace will branch from the current workspace's branch"
                          : "Branch to create the new workspace from. Leave blank to auto-detect."}
                        {isBranchesLoading && " (loading branches...)"}
                      </p>
                    )}
                  </div>
                )}

                <div className="space-y-2">
                  <label className="block text-sm font-semibold text-foreground">
                    Copy Files/Directories
                  </label>
                  <div className="relative">
                    <FileText className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                    <input
                      type="text"
                      value={customFilesInput}
                      onChange={(e) => {
                        setCustomFilesInput(e.target.value);
                        const files = e.target.value
                          .split(",")
                          .map((f) => f.trim())
                          .filter((f) => f.length > 0);
                        setFormData((prev) => ({ ...prev, copy_files: files }));
                      }}
                      className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                      placeholder=".env, frontend/, backend/config"
                    />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Comma-separated list of files or directories to copy. Directories are copied recursively. File patterns (e.g., ".env") search recursively.
                  </p>
                </div>

                {/* Extra content slot (e.g., copy uncommitted files toggle) */}
                {extraContent}

                <div className="flex items-center gap-3 p-4 elevation-1 border border-border rounded-lg">
                  <div className="relative flex items-center justify-center">
                    <input
                      type="checkbox"
                      id="force-create"
                      checked={formData.force}
                      onChange={(e) =>
                        setFormData((prev) => ({ ...prev, force: e.target.checked }))
                      }
                      className="sr-only"
                    />
                    <div
                      className={cn(
                        "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                        formData.force
                          ? "border-foreground bg-background"
                          : "border-border bg-background"
                      )}
                    >
                      {formData.force && (
                        <svg className="w-3 h-3 text-foreground" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                          <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                        </svg>
                      )}
                    </div>
                  </div>
                  <div className="flex-1">
                    <label
                      htmlFor="force-create"
                      className="block text-sm font-semibold text-foreground cursor-pointer"
                    >
                      Force Create
                    </label>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      Delete existing workspace/branch if they exist and recreate from scratch
                    </p>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>

        <div className="flex gap-3 pt-6 border-t border-border">
          <button
            type="button"
            onClick={onClose}
            className="flex-1 px-5 py-3 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
            disabled={isCreating}
          >
            Cancel
          </button>
          <button
            type="submit"
            className="flex-1 px-5 py-3 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
            disabled={isCreating}
          >
            {isCreating ? "Creating..." : "Create Workspace"}
          </button>
        </div>
      </form>
    </Modal>

    {/* InitializeGitModal for when user clicks Initialize Git from the prompt */}
    {currentProject && (
      <InitializeGitModal
        isOpen={showInitGitModal}
        onClose={() => setShowInitGitModal(false)}
        onSuccess={handleInitGitSuccess}
        projectId={projectId}
        projectName={currentProject.name}
      />
    )}
    </>
  );
}