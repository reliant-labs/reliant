import { FileText, Terminal, History, FolderOpen, GitBranch, Workflow } from "lucide-react";
import { cn } from "../../lib/utils";
import { isDev } from "../../lib/constants";

interface NavigationItem {
  id: string;
  icon: React.ElementType;
  label: string;
  description: string;
  onClick: () => void;
}

interface WelcomeScreenProps {
  onOpenFileBrowser: () => void;
  onOpenTerminal: () => void;
  onOpenDiffViewer: () => void;
  onOpenProjects: () => void;
  onOpenWorktrees: () => void;
  onOpenWorkflows?: () => void;
}

export function WelcomeScreen({
  onOpenFileBrowser,
  onOpenTerminal,
  onOpenDiffViewer,
  onOpenProjects,
  onOpenWorktrees,
  onOpenWorkflows,
}: WelcomeScreenProps) {
  const navigationItems: NavigationItem[] = [
    {
      id: "files",
      icon: FileText,
      label: "File Browser",
      description: "Browse and edit project files",
      onClick: onOpenFileBrowser,
    },
    {
      id: "terminal",
      icon: Terminal,
      label: "Terminal",
      description: "Open integrated terminal",
      onClick: onOpenTerminal,
    },
    {
      id: "changes",
      icon: History,
      label: "Recent Changes",
      description: "View and review recent changes",
      onClick: onOpenDiffViewer,
    },
    {
      id: "projects",
      icon: FolderOpen,
      label: "Projects",
      description: "Manage your projects",
      onClick: onOpenProjects,
    },
    {
      id: "worktrees",
      icon: GitBranch,
      label: "Worktrees",
      description: "Manage Git worktrees",
      onClick: onOpenWorktrees,
    },
  ];

  // Add workflows only in dev mode
  if (isDev && onOpenWorkflows) {
    navigationItems.push({
      id: "workflows",
      icon: Workflow,
      label: "Workflow Builder",
      description: "Create and manage workflows",
      onClick: onOpenWorkflows,
    });
  }

  return (
    <div className="flex flex-col items-center justify-center h-full bg-background p-8">
      {/* Logo */}
      <div className="mb-12 flex flex-col items-center">
        <div className="text-6xl font-bold mb-4 bg-gradient-to-r from-primary to-primary/60 bg-clip-text text-transparent">
          RELIANT
        </div>
        <div className="text-sm text-muted-foreground font-mono">
          The world's best multi-agent coding assistant
        </div>
      </div>

      {/* Navigation Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 max-w-4xl w-full">
        {navigationItems.map((item) => {
          const Icon = item.icon;
          return (
            <button
              key={item.id}
              onClick={item.onClick}
              className={cn(
                "group flex flex-col items-start gap-3 p-6 rounded-lg border border-border",
                "bg-card hover:bg-accent/50 transition-all duration-200",
                "hover:shadow-md hover:scale-[1.02] active:scale-[0.98]",
                "text-left"
              )}
            >
              <div className="p-2 rounded-md bg-primary/10 group-hover:bg-primary/20 transition-colors">
                <Icon className="w-6 h-6 text-primary" />
              </div>
              <div className="flex-1">
                <h3 className="text-sm font-semibold mb-1">{item.label}</h3>
                <p className="text-xs text-muted-foreground">{item.description}</p>
              </div>
            </button>
          );
        })}
      </div>

      {/* Helper Text */}
      <div className="mt-12 text-center text-sm text-muted-foreground">
        <p className="mb-2">Press <kbd className="px-2 py-1 bg-muted rounded text-xs">Cmd+N</kbd> to start a new chat</p>
        <p>Or select an option above to get started</p>
      </div>
    </div>
  );
}
