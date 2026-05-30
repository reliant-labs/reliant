import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { FolderOpen, GitBranch, Loader2, Plus } from "lucide-react";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { trackEvent } from "@/lib/analytics";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { ProjectPickerModal } from "@/components/Projects/ProjectPickerModal";
import { finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { DaemonConnectingGate } from "../DaemonConnectingGate";
import type { StepProps } from "../types";

export function ProjectPickerStep({ plan, onBack }: StepProps) {
  const navigate = useNavigate();
  const completeOnboardingMutation = useCompleteOnboarding();

  const projects = useProjectStore((state) => state.projects);
  const isLoading = useProjectStore((state) => state.isLoading);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);

  const [completing, setCompleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  // After completeOnboarding succeeds for a cloud user we render the daemon
  // gate instead of navigating immediately, so the user gets a clear signal
  // whether the daemon came online. For local daemons ComputeStep already
  // gates on activeDaemon, so we skip the gate.
  const [showDaemonGate, setShowDaemonGate] = useState(false);

  useEffect(() => {
    void loadProjects();
  }, [loadProjects]);

  const sortedProjects = useMemo(() => {
    return [...projects].sort(
      (a, b) =>
        new Date(b.last_active).getTime() - new Date(a.last_active).getTime(),
    );
  }, [projects]);

  const isCloud = plan.compute === "cloud_free_trial";

  const goToChat = useCallback(() => {
    navigate({ to: "/", search: { step: undefined, plan: undefined } });
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
        trackEvent("onboarding_completed", {
          provider: plan.modelProvider ?? "unknown",
          compute: plan.compute ?? "unknown",
          project_source: source,
        });
        await finalizeOnboardingSideEffects(plan.modelProvider);
        // Cloud daemons may still be provisioning at this point — show the
        // gate so the user knows whether to wait or report. Local daemons
        // were already verified ACTIVE by ComputeStep.
        if (isCloud) {
          setShowDaemonGate(true);
        } else {
          goToChat();
        }
      } catch (err) {
        logger.warn("[ProjectPickerStep] finalize failed", err);
        setError(err instanceof Error ? err.message : "Failed to finish onboarding");
        setCompleting(false);
      }
    },
    [completeOnboardingMutation, goToChat, isCloud, plan],
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

  if (showDaemonGate) {
    return (
      <div className="space-y-6">
        <DaemonConnectingGate onContinue={goToChat} />
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
          Open one of your existing projects or create a new one to finish setup.
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
            Point Reliant at a folder on this machine and we'll set things up.
          </p>
        </div>
      </button>

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-medium text-foreground">Your projects</h3>
          {isLoading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        <div className="max-h-64 overflow-y-auto rounded-lg border border-border/40">
          {!isLoading && sortedProjects.length === 0 && (
            <div className="px-4 py-6 text-center text-xs text-muted-foreground">
              No existing projects. Create one above to continue.
            </div>
          )}

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
