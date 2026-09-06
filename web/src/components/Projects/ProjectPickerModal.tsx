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
      title="Create New Project"
      size="lg"
    >
      <form onSubmit={handleSubmit} className="space-y-6">
        {error && (
          <div className="p-4 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg">
            <div className="flex items-start gap-3">
              <AlertCircle className="w-5 h-5 flex-shrink-0 mt-0.5" />
              <span className="flex-1 text-sm">{error}</span>
            </div>
          </div>
        )}

        <div className="space-y-5">
          <div className="space-y-2">
            <label className="block text-sm font-semibold text-foreground">
              Project Directory <span className="text-destructive">*</span>
            </label>
            <div className="flex gap-3">
              <input
                type="text"
                value={formData.path}
                onChange={(e) => applyPath(e.target.value)}
                className="flex-1 px-4 py-3 bg-background border border-border rounded-lg text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                placeholder={`Enter full path (e.g., ${examplePath})`}
                required
                autoFocus
              />
              <button
                type="button"
                onClick={handleSelectDirectory}
                className="px-4 py-3 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium transition-all flex items-center gap-2 focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
              >
                <FolderOpen className="w-4 h-4" />
                Browse
              </button>
            </div>
          </div>

          <div className="space-y-2">
            <label className="block text-sm font-semibold text-foreground">
              Display Name
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
              className="w-full px-4 py-3 bg-background border border-border rounded-lg text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
              placeholder={deriveName(formData.path) || "my-awesome-project"}
            />
          </div>

          <div className="space-y-2">
            <label className="block text-sm font-semibold text-foreground">
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
              className="w-full px-4 py-3 bg-background border border-border rounded-lg text-sm resize-none focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
              rows={3}
              placeholder="Optional project description..."
            />
          </div>

          <div className="p-4 bg-muted/30 rounded-lg border border-border space-y-2">
            <div className="flex items-center gap-2">
              <GitBranch className="w-4 h-4 text-muted-foreground" />
              <span className="text-sm font-medium text-foreground">Default Branch</span>
            </div>
            <input
              type="text"
              value={formData.default_branch}
              onChange={(e) =>
                setFormData((prev) => ({
                  ...prev,
                  default_branch: e.target.value,
                }))
              }
              className="w-full px-3 py-2 bg-background border border-border rounded-lg text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
              placeholder="main"
            />
            <p className="text-xs text-muted-foreground mt-2">
              Git repository will be auto-detected. If the project folder contains a .git directory, git features will be enabled automatically.
            </p>
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
            {isCreating ? "Creating..." : "Create Project"}
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