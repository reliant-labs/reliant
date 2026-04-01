import { useState, useEffect } from "react";
import { Save, RefreshCw, GitBranch, CheckCircle2, XCircle } from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { InitializeGitModal } from "../Git/InitializeGitModal";

interface ProjectSettingsProps {
  projectId?: string;
}

export function ProjectSettings({ projectId }: ProjectSettingsProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const [rescanInterval, setRescanInterval] = useState(10);
  const [autoRescan, setAutoRescan] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [hasChanges, setHasChanges] = useState(false);
  const [showInitGitModal, setShowInitGitModal] = useState(false);

  // Load settings from localStorage or backend
  useEffect(() => {
    const savedSettings = localStorage.getItem('reliant_project_settings');
    if (savedSettings) {
      const settings = JSON.parse(savedSettings);
      setRescanInterval(settings.rescanInterval || 10);
      setAutoRescan(settings.autoRescan || false);
    }
  }, [projectId]);

  const handleSave = async () => {
    setIsSaving(true);
    try {
      // Save to localStorage for now
      const settings = {
        rescanInterval,
        autoRescan,
        updatedAt: new Date().toISOString()
      };
      localStorage.setItem('reliant_project_settings', JSON.stringify(settings));
      setHasChanges(false);
    } catch (error) {
      console.error('Failed to save settings:', error);
    } finally {
      setIsSaving(false);
    }
  };

  const handleIntervalChange = (value: number) => {
    setRescanInterval(value);
    setHasChanges(true);
  };

  const handleAutoRescanChange = (checked: boolean) => {
    setAutoRescan(checked);
    setHasChanges(true);
  };

  const handleInitGitSuccess = async () => {
    await refreshCurrentProject();
  };

  return (
    <div className="space-y-8">
      {/* Git Repository Section */}
      <div>
        <h3 className="text-lg font-mono font-semibold mb-4">Git Repository</h3>
        <div className="p-4 elevation-1 rounded-lg border border-border">
          <div className="flex items-start gap-4">
            <div className={`p-3 rounded-lg ${
              currentProject?.is_git_repo 
                ? 'bg-success/10 ring-1 ring-success/20' 
                : 'bg-muted'
            }`}>
              <GitBranch className={`w-5 h-5 ${
                currentProject?.is_git_repo ? 'text-success' : 'text-muted-foreground'
              }`} />
            </div>
            <div className="flex-1">
              <div className="flex items-center gap-2 mb-2">
                {currentProject?.is_git_repo ? (
                  <>
                    <CheckCircle2 className="w-4 h-4 text-success" />
                    <span className="text-sm font-semibold text-foreground">
                      Git Initialized
                    </span>
                  </>
                ) : (
                  <>
                    <XCircle className="w-4 h-4 text-muted-foreground" />
                    <span className="text-sm font-semibold text-foreground">
                      No Git Repository
                    </span>
                  </>
                )}
              </div>
              <p className="text-xs text-muted-foreground mb-3">
                {currentProject?.is_git_repo
                  ? `This project is using git with default branch "${currentProject.default_branch || 'main'}".`
                  : 'Git is required for workspaces and version control features.'}
              </p>
              {!currentProject?.is_git_repo && (
                <button
                  onClick={() => setShowInitGitModal(true)}
                  className="flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground hover:bg-primary/90 rounded text-sm font-mono transition-colors"
                >
                  <GitBranch className="w-4 h-4" />
                  Initialize Git Repository
                </button>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Project Rescan Settings */}
      <div>
        <h3 className="text-lg font-mono font-semibold mb-4">Project Rescan Settings</h3>
        
        <div className="space-y-4">
          {/* Rescan Interval */}
          <div className="space-y-2">
            <label className="block text-sm font-mono text-foreground">
              Commits before rescan prompt
            </label>
            <div className="flex items-center gap-4">
              <input
                type="number"
                min="1"
                max="100"
                value={rescanInterval}
                onChange={(e) => handleIntervalChange(parseInt(e.target.value) || 10)}
                className="w-24 px-3 py-2 elevation-0 border border-border/60 rounded text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary"
              />
              <span className="text-sm text-muted-foreground">
                commits (default: 10)
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
              You'll be prompted to rescan the project after this many commits
            </p>
          </div>

          {/* Auto Rescan */}
          <div className="space-y-2">
            <label className="flex items-center gap-3 cursor-pointer">
              <input
                type="checkbox"
                checked={autoRescan}
                onChange={(e) => handleAutoRescanChange(e.target.checked)}
                className="w-4 h-4 text-primary border-border rounded focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
              />
              <span className="text-sm font-mono text-foreground">
                Automatically rescan without prompting
              </span>
            </label>
            <p className="text-xs text-muted-foreground ml-7">
              When enabled, project rescans will happen automatically in the background
            </p>
          </div>

          {/* Info Box */}
          <div className="mt-4 p-3 elevation-1 rounded-lg border border-border">
            <div className="flex items-start gap-2">
              <RefreshCw className="w-4 h-4 text-muted-foreground mt-0.5" />
              <div className="space-y-1">
                <p className="text-xs font-mono text-foreground">About Project Rescanning</p>
                <p className="text-xs text-muted-foreground">
                  Rescanning updates the AI's understanding of your codebase, refreshes testing 
                  infrastructure, and ensures the LLM has current information about your project structure.
                </p>
              </div>
            </div>
          </div>

          {/* Save Button */}
          {hasChanges && (
            <div className="flex justify-end pt-4">
              <button
                onClick={handleSave}
                disabled={isSaving}
                className="flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground hover:bg-primary/90 rounded text-sm font-mono transition-colors disabled:opacity-50"
              >
                <Save className="w-4 h-4" />
                {isSaving ? 'Saving...' : 'Save Changes'}
              </button>
            </div>
          )}
        </div>
      </div>

      {currentProject && (
        <InitializeGitModal
          isOpen={showInitGitModal}
          onClose={() => setShowInitGitModal(false)}
          onSuccess={handleInitGitSuccess}
          projectId={currentProject.id}
          projectName={currentProject.name}
        />
      )}
    </div>
  );
}