import { useEffect, useMemo, useRef, useState } from "react";
import { ConnectError, Code } from "@connectrpc/connect";
import { FolderOpen, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { listDirectory } from "@/api/filesystem-grpc";
import { getEventBus } from "@/lib/events";
import type { StepProps } from "../types";
import { DirectoryPicker } from "@/components/Projects/DirectoryPicker";

const CLOUD_PROJECT_ROOT = "/home/workspace/projects";

function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48) || "reliant-project";
}

function titleFromIntent(intent: string | undefined): string {
  switch (intent) {
    case "landing_page":
      return "Landing Page";
    case "pitch_deck":
      return "Pitch Deck";
    case "blog_post":
      return "Docs Draft";
    case "build_app":
      return "New App";
    case "existing_codebase":
      return "Existing Project";
    case "explore":
      return "Reliant Sample Project";
    default:
      return "Reliant Project";
  }
}

function basename(path: string): string {
  return path.split("/").filter(Boolean).pop() || "Reliant Project";
}

function projectNameForPath(path: string, fallbackName: string, isGeneratedWorkspace: boolean): string {
  return isGeneratedWorkspace ? fallbackName : basename(path);
}

async function getHomePath(): Promise<string> {
  try {
    const result = await listDirectory("");
    return result.path || "~";
  } catch {
    return "~";
  }
}

function isDevForceShow(): boolean {
  if (typeof window === "undefined") return false;
  return new URLSearchParams(window.location.search).get("devForceShow") === "true";
}

export function ProjectLocationStep({ plan, updatePlan, onNext }: StepProps) {
  const [homePath, setHomePath] = useState("~");
  const [customPath, setCustomPath] = useState(plan.localPath ?? "");
  const [isDirectoryPickerOpen, setIsDirectoryPickerOpen] = useState(false);
  const [savingPath, setSavingPath] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const isNewProject = plan.codeSource === "new_project";
  const isSampleProject = plan.codeSource === "sample_project";
  const usesGeneratedWorkspace = isNewProject || isSampleProject;
  const suggestedName = plan.projectName || titleFromIntent(plan.intent);
  const suggestedSlug = useMemo(() => slugify(suggestedName), [suggestedName]);
  const suggestedLocalPath = homePath === "~" ? `~/Projects/${suggestedSlug}` : `${homePath}/Projects/${suggestedSlug}`;

  useEffect(() => {
    void getHomePath().then(setHomePath);
  }, []);

  const autoAdvancedRef = useRef(false);

  const createAndContinue = async (path: string) => {
    if (!path.trim()) return;

    const projectName = projectNameForPath(path, suggestedName, usesGeneratedWorkspace);
    setSavingPath(path);
    setError(null);
    try {
      if (isDevForceShow()) {
        updatePlan({ localPath: path, projectName });
        onNext();
        return;
      }

      const bus = getEventBus();
      bus.emit("toast:show", { message: `Opening project "${projectName}"...`, variant: "info" });

      const { useProjectStore } = await import("@/store/projectStore");
      const store = useProjectStore.getState();

      try {
        const createdProject = await store.createProject({
          name: projectName,
          path,
          description: "",
          is_git_repo: false,
          default_branch: "main",
        });
        bus.emit("toast:dismiss", {});
        await store.loadProjects();
        updatePlan({ localPath: path, projectName });
        if (createdProject) {
          await useProjectStore.getState().selectProject(createdProject);
          onNext();
          return;
        }
        onNext();
      } catch (projectError) {
        bus.emit("toast:dismiss", {});
        const alreadyExists =
          (projectError instanceof ConnectError && projectError.code === Code.AlreadyExists) ||
          (projectError instanceof Error &&
            (projectError.message.includes("already exists") || projectError.message.includes("409")));
        if (alreadyExists) {
          await store.loadProjects();
          const existing = useProjectStore.getState().projects.find((project) => project.path === path);
          if (existing) {
            await useProjectStore.getState().selectProject(existing);
            updatePlan({ localPath: path, projectName: existing.name });
            onNext();
            return;
          }
        }
        throw projectError;
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to open project");
    } finally {
      setSavingPath(null);
    }
  };

  // Auto-advance for cloud generated workspaces is no longer needed — this step is local-only.
  // If somehow rendered for cloud, just set the plan values.
  useEffect(() => {
    const isCloud = plan.compute === "cloud_free_trial";
    if (!isCloud || !usesGeneratedWorkspace) return;
    if (autoAdvancedRef.current) return;

    const cloudPath = `${CLOUD_PROJECT_ROOT}/${suggestedSlug}`;
    if (!plan.localPath) {
      updatePlan({ localPath: cloudPath, projectName: suggestedName });
    }

    if (isDevForceShow()) return;
    autoAdvancedRef.current = true;
    void createAndContinue(cloudPath);
  }, [plan.compute, plan.localPath, suggestedName, suggestedSlug, updatePlan, usesGeneratedWorkspace]);

  // If this step is somehow shown for cloud + generated workspace (shouldn't happen per stepConfig),
  // show a spinner while auto-advancing.
  if (plan.compute === "cloud_free_trial" && usesGeneratedWorkspace && !isDevForceShow()) {
    return (
      <div className="flex flex-col items-center justify-center py-12 space-y-3">
        <Loader2 className="w-6 h-6 animate-spin text-primary" />
        <p className="text-sm text-muted-foreground">Setting up hosted project&hellip;</p>
        {error && <p className="text-xs text-destructive text-center">{error}</p>}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-2xl font-semibold text-foreground tracking-tight">
          Choose the project folder
        </h2>
        <p className="text-sm text-muted-foreground">
          {usesGeneratedWorkspace
            ? "Choose where Reliant should create or open the starter workspace."
            : "Confirm the existing folder you want Reliant to open."}
        </p>
      </div>

      <div className="space-y-3">
        {usesGeneratedWorkspace && (
          <button
            type="button"
            onClick={() => setCustomPath(suggestedLocalPath)}
            className="flex items-center gap-3 w-full p-4 rounded-lg border border-border/60 bg-muted/30 text-left hover:border-primary/40 hover:bg-muted/70 transition-colors"
          >
            <FolderOpen className="w-4 h-4 text-primary" />
            <div className="min-w-0">
              <span className="block text-sm font-medium text-foreground">Use suggested path</span>
              <span className="block text-xs text-muted-foreground font-mono truncate">{suggestedLocalPath}</span>
            </div>
          </button>
        )}

        <button
          type="button"
          onClick={() => setIsDirectoryPickerOpen(true)}
          className="flex items-center gap-3 w-full p-4 rounded-lg border border-border/60 bg-background text-left hover:border-primary/40 hover:bg-muted/50 transition-colors"
        >
          <FolderOpen className="w-4 h-4 text-primary" />
          <div>
            <span className="block text-sm font-medium text-foreground">Browse folders</span>
            <span className="block text-xs text-muted-foreground">Starts at the daemon home directory.</span>
          </div>
        </button>

        <div className="space-y-1.5">
          <label className="text-xs text-muted-foreground" htmlFor="local-project-path">
            Folder path
          </label>
          <input
            id="local-project-path"
            value={customPath}
            onChange={(event) => setCustomPath(event.target.value)}
            placeholder={suggestedLocalPath}
            className="w-full px-3 py-2.5 rounded-lg text-sm font-mono bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-primary/30"
          />
        </div>
      </div>

      <button
        type="button"
        onClick={() => createAndContinue(customPath)}
        disabled={!customPath.trim() || Boolean(savingPath)}
        className={cn(
          "w-full inline-flex items-center justify-center gap-2 py-3 rounded-lg text-sm font-semibold transition-colors",
          customPath.trim() && !savingPath
            ? "bg-primary text-primary-foreground hover:bg-primary/90"
            : "bg-muted text-muted-foreground cursor-not-allowed",
        )}
      >
        {savingPath && <Loader2 className="w-4 h-4 animate-spin" />}
        {usesGeneratedWorkspace ? "Create or open this folder" : "Open this folder"}
      </button>

      {error && <p className="text-xs text-destructive text-center">{error}</p>}

      <DirectoryPicker
        isOpen={isDirectoryPickerOpen}
        onClose={() => setIsDirectoryPickerOpen(false)}
        onSelect={(path) => setCustomPath(path)}
      />
    </div>
  );
}
