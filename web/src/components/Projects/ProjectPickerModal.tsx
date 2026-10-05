import { useState } from "react";
import { FolderOpen, GitBranch, AlertCircle } from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";
import { Modal } from "../ui/Modal";
import { DirectoryPicker } from "./DirectoryPicker";
import { useProjectStore } from "../../store/projectStore";
import { toast } from "../../lib/toast-manager";
import { basename, examplePathForPlatform, isAbsolutePath } from "../../lib/pathUtils";
import { useDetectedOS } from "../ReliantDownloadOptions";

interface Project {
  id: string;
  name: string;
  path: string;
  description?: string;
  is_git_repo?: boolean;
}

interface ProjectPickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onProjectCreated: (project?: Project) => void;
}

export function ProjectPickerModal({
  isOpen,
  onClose,
  onProjectCreated,
}: ProjectPickerModalProps) {
  const createProject = useProjectStore((state) => state.createProject);
  const [formData, setFormData] = useState({
    name: "",
    path: "",
    description: "",
    default_branch: "main",
  });
  // The display name defaults to the directory basename and keeps tracking the
  // path until the user explicitly renames. Clearing the field resumes tracking.
  const [nameManuallyEdited, setNameManuallyEdited] = useState(false);
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isDirectoryPickerOpen, setIsDirectoryPickerOpen] = useState(false);

  const isElectron = !!window.electronAPI?.selectDirectory;
  // Only used to pick the example path we show — a Windows user given a
  // "/Users/you/..." example has been shown something they cannot type.
  const detectedOS = useDetectedOS();
  const examplePath = examplePathForPlatform(detectedOS);

  // Last non-empty path segment, e.g. "/Users/you/projects/my-app/" -> "my-app",
  // "C:\Users\you\projects\my-app" -> "my-app".
  const deriveName = (path: string) => basename(path);

  const applyPath = (selectedPath: string) => {
    setFormData((prev) => ({
      ...prev,
      path: selectedPath,
      name: nameManuallyEdited ? prev.name : deriveName(selectedPath),
    }));
  };

  const handleSelectDirectory = async () => {
    if (isElectron) {
      try {
        const selectedPath = await window.electronAPI!.selectDirectory();
        if (selectedPath) {
          applyPath(selectedPath);
        }
      } catch (err) {
        console.error("Failed to select directory via Electron:", err);
      }
    } else {
      setIsDirectoryPickerOpen(true);
    }
  };

  const handleDirectorySelected = (selectedPath: string) => {
    applyPath(selectedPath);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!formData.path) {
      setError("Choose a project directory");
      return;
    }

    // The daemon that owns this path may be on a different OS than this
    // browser, so accept any platform's absolute form and send it through
    // unchanged rather than rewriting it to local convention.
    if (!isAbsolutePath(formData.path)) {
      setError(`Enter a full path to the project folder, for example ${examplePath}`);
      return;
    }

    // Name is optional in the UI; fall back to the directory basename.
    const name = formData.name.trim() || deriveName(formData.path) || "New Project";

    setIsCreating(true);
    setError(null);

    try {
      const createdProject = await createProject({ ...formData, name });
      onProjectCreated(createdProject);
      onClose();

      setNameManuallyEdited(false);
      setFormData({
        name: "",
        path: "",
        description: "",
        default_branch: "main",
      });
    } catch (err) {
      // If project already exists at this path, find and open it
      const isAlreadyExists =
        (err instanceof ConnectError && err.code === Code.AlreadyExists) ||
        (err instanceof Error && (err.message.includes("already exists") || err.message.includes("409")));
      if (isAlreadyExists) {
        const { projects, loadProjects } = useProjectStore.getState();
        let existing = projects.find((p) => p.path === formData.path);
        if (!existing) {
          await loadProjects();
          existing = useProjectStore.getState().projects.find((p) => p.path === formData.path);
        }
        if (existing) {
          toast.success(`Opening existing project "${existing.name}"`);
          onProjectCreated(existing);
          onClose();
          return;
        }
      }

      let errorMessage = "Failed to create project";
      if (err instanceof Error) {
        if (err.message.includes("permission")) {
          errorMessage =
            "Permission denied. Please check that you have access to this location.";
        } else {
          errorMessage = err.message;
        }
      }

      setError(errorMessage);
    } finally {
      setIsCreating(false);
    }
  };

  return (
    <>
    <Modal 
      isOpen={isOpen} 
      onClose={onClose}
      title="New project"
      size="lg"
    >
      <form onSubmit={handleSubmit} className="space-y-5">
        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-destructive-ink" role="alert">
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
              <span className="flex-1 text-sm">{error}</span>
            </div>
          </div>
        )}

        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-foreground">
              Folder <span className="text-destructive-ink" aria-hidden="true">*</span>
            </label>
            <div className="flex gap-2">
              <input
                type="text"
                value={formData.path}
                onChange={(e) => applyPath(e.target.value)}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground placeholder:text-muted-foreground/70 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary flex-1 font-mono"
                placeholder={`Enter full path (e.g., ${examplePath})`}
                required
                autoFocus
              />
              <button
                type="button"
                onClick={handleSelectDirectory}
                className="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-input bg-card px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:opacity-50"
              >
                <FolderOpen className="h-4 w-4" aria-hidden="true" />
                Browse
              </button>
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-foreground">
              Name
              <span className="ml-2 text-xs font-normal text-muted-foreground">
                optional — defaults to the folder name
              </span>
            </label>
            <input
              type="text"
              value={formData.name}
              onChange={(e) => {
                const value = e.target.value;
                // Resume tracking the folder name if the user clears the field.
                setNameManuallyEdited(value.trim().length > 0);
                setFormData((prev) => ({ ...prev, name: value }));
              }}
              className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground placeholder:text-muted-foreground/70 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary"
              placeholder={deriveName(formData.path) || "my-awesome-project"}
            />
          </div>

          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-foreground">
              Description
            </label>
            <textarea
              value={formData.description}
              onChange={(e) =>
                setFormData((prev) => ({
                  ...prev,
                  description: e.target.value,
                }))
              }
              className="w-full resize-none rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground/70 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary"
              rows={3}
              placeholder="Optional"
            />
          </div>

          <div className="space-y-1.5">
            <label className="flex items-center gap-1.5 text-sm font-medium text-foreground">
              <GitBranch className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
              Default branch
            </label>
            <input
              type="text"
              value={formData.default_branch}
              onChange={(e) =>
                setFormData((prev) => ({
                  ...prev,
                  default_branch: e.target.value,
                }))
              }
              className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground placeholder:text-muted-foreground/70 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary font-mono"
              placeholder="main"
            />
            <p className="text-xs text-muted-foreground">
              Git is detected automatically: if the folder contains a .git directory, git features turn on.
            </p>
          </div>
        </div>

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-input bg-card px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:opacity-50"
            disabled={isCreating}
          >
            Cancel
          </button>
          <button
            type="submit"
            className="inline-flex h-9 items-center justify-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow-sm transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
            disabled={isCreating}
          >
            {isCreating ? "Creating…" : "Create project"}
          </button>
        </div>
      </form>
    </Modal>

      <DirectoryPicker
        isOpen={isDirectoryPickerOpen}
        onClose={() => setIsDirectoryPickerOpen(false)}
        onSelect={handleDirectorySelected}
      />
    </>
  );
}