import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { FolderOpen, GitBranch, Loader2, Plus } from "lucide-react";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { ProjectPickerModal } from "@/components/Projects/ProjectPickerModal";
import { finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { leaveOnboarding } from "../leaveOnboarding";
import { markOnboardingFinalized } from "../analytics";
import { ProvisioningGate } from "../ProvisioningGate";
import { useCommitLaunchPlan } from "../useCommitLaunchPlan";
import type { StepProps } from "../types";

export function ProjectPickerStep({ plan, updatePlan, onBack }: StepProps) {
  const navigate = useNavigate();
  const completeOnboardingMutation = useCompleteOnboarding();

  const projects = useProjectStore((state) => state.projects);
  const isLoading = useProjectStore((state) => state.isLoading);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);

  const [completing, setCompleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  // The commit point. Nothing is created until this runs, and it runs once,
  // here, after completeOnboarding — never from an effect.
  const { commit, runCommit, retry } = useCommitLaunchPlan(updatePlan);
  // `isLoading` is false before the first fetch has even started, so it can't
  // by itself distinguish "no projects" from "haven't looked yet". This flips
  // when the initial load settles — success or failure — and is what the
  // auto-open below waits on.
  const [projectsSettled, setProjectsSettled] = useState(false);
  const autoOpenedRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    void loadProjects()
      .finally(() => {
        if (!cancelled) setProjectsSettled(true);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [loadProjects]);

  // A fresh install reaches this step with nothing to pick, and creating a
  // project is the only way forward — so open the create modal rather than
  // printing a message asking the user to click the button themselves.
  // Guarded by a ref so dismissing it leaves the user on the (empty) list
  // instead of a modal that immediately comes back.
  useEffect(() => {
    if (autoOpenedRef.current) return;
    if (!projectsSettled || isLoading) return;
    if (projects.length > 0) return;
    autoOpenedRef.current = true;
    setIsCreateModalOpen(true);
  }, [projectsSettled, isLoading, projects.length]);

  const sortedProjects = useMemo(() => {
    return [...projects].sort(
      (a, b) =>
        new Date(b.last_active).getTime() - new Date(a.last_active).getTime(),
    );
  }, [projects]);

  // The exit: the gate's Continue is the ONLY way out, which is what makes it
  // impossible to leave while the machine is still provisioning — or to leave
  // without seeing that provisioning failed.
  const goToChat = useCallback(() => {
    void leaveOnboarding("completed_cloud_gate_continue", navigate);
  }, [navigate]);

  const finalize = useCallback(
    async (source: "existing" | "new") => {
      if (!plan.compute) {
        setError("Missing compute selection. Go back and try again.");
        return;
      }
      setError(null);
      setCompleting(true);
      try {
        await completeOnboardingMutation.mutateAsync({
          compute: plan.compute,
          modelProvider: plan.modelProvider,
        });
        markOnboardingFinalized(plan, source === "existing" ? "existing" : "new");
        await finalizeOnboardingSideEffects();
        await runCommit(plan);
      } catch (err) {
        logger.warn("[ProjectPickerStep] finalize failed", err);
        setError(err instanceof Error ? err.message : "Failed to finish onboarding");
        setCompleting(false);
      }
    },
    [completeOnboardingMutation, plan, runCommit],
  );

  const handleSelectExisting = useCallback(
    async (project: Project) => {
      if (completing) return;
      try {
        await selectProject(project);
      } catch (err) {
        logger.warn("[ProjectPickerStep] selectProject failed", err);
        setError(err instanceof Error ? err.message : "Failed to open project");
        return;
      }
      await finalize("existing");
    },
    [completing, finalize, selectProject],
  );

  const handleProjectCreated = useCallback(
    async (createdProject?: { id: string }) => {
      setIsCreateModalOpen(false);
      if (!createdProject) return;
      try {
        await loadProjects();
        const full = useProjectStore
          .getState()
          .projects.find((p) => p.id === createdProject.id);
        if (full) {
          await selectProject(full);
        }
      } catch (err) {
        logger.warn("[ProjectPickerStep] post-create select failed", err);
        setError(err instanceof Error ? err.message : "Failed to open project");
        return;
      }
      await finalize("new");
    },
    [finalize, loadProjects, selectProject],
  );

  if (commit) {
    return (
      <div className="space-y-6">
        <ProvisioningGate
          commit={commit}
          onContinue={goToChat}
          onRetry={() => void retry(plan)}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Pick a project
        </h2>
        <p className="text-sm text-muted-foreground">
          Choose a folder for Reliant to work in. You can add more later.
        </p>
      </div>

      <button
        type="button"
        onClick={() => setIsCreateModalOpen(true)}
        disabled={completing}
        className={cn(
          "flex w-full items-center gap-3 rounded-xl border-2 border-primary/40 p-5 text-left transition-all",
          "bg-[linear-gradient(135deg,rgba(56,189,248,0.18),rgba(168,85,247,0.18))]",
          !completing && "hover:border-primary",
          completing && "cursor-not-allowed opacity-80",
        )}
      >
        <div className="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-primary/20 text-primary ring-1 ring-primary/30">
          <Plus className="h-5 w-5" />
        </div>
        <div className="min-w-0 space-y-1">
          <span className="block text-base font-semibold text-foreground">
            Create a new project
          </span>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Pick a folder on your machine — an existing codebase, or an empty one to start fresh.
          </p>
        </div>
      </button>

      {/* Only worth a section header and a bordered box when there is
          something in it — for a first-time user the create modal is already
          open over the top of this. */}
      {(isLoading || sortedProjects.length > 0) && (
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-medium text-foreground">Your projects</h3>
          {isLoading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        <div className="max-h-64 overflow-y-auto rounded-lg border border-border/40">
          {sortedProjects.map((project) => (
            <button
              key={project.id}
              type="button"
              onClick={() => handleSelectExisting(project)}
              disabled={completing}
              className={cn(
                "flex w-full items-center gap-3 border-b border-border/30 px-3 py-2.5 text-left transition-colors last:border-b-0",
                !completing && "hover:bg-muted/50",
                completing && "cursor-not-allowed opacity-60",
              )}
            >
              <FolderOpen className="h-4 w-4 flex-shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium text-foreground">
                    {project.name}
                  </span>
                  {project.is_git_repo && (
                    <GitBranch className="h-3 w-3 flex-shrink-0 text-muted-foreground/50" />
                  )}
                </div>
                <p className="truncate text-xs text-muted-foreground">
                  {project.path.replace(/^\/(?:Users|home)\/[^/]+/, "~")}
                </p>
              </div>
            </button>
          ))}
        </div>
      </div>
      )}

      {error && <p className="text-center text-xs text-destructive">{error}</p>}

      {completing && (
        <div className="flex items-center justify-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Finishing setup...
        </div>
      )}

      <button
        type="button"
        onClick={onBack}
        disabled={completing}
        className="w-full py-1 text-center text-xs text-muted-foreground transition-colors hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
      >
        Back
      </button>

      <ProjectPickerModal
        isOpen={isCreateModalOpen}
        onClose={() => setIsCreateModalOpen(false)}
        onProjectCreated={handleProjectCreated}
      />
    </div>
  );
}
