import { FileText, Terminal as TerminalIcon, FolderOpen, Workflow, FolderGit2 } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { useTerminalStore } from "../../store/terminalStore";
import { useViewerStore } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { BrandMark } from "../icons/BrandMark";

interface MenuItem {
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  shortcut: string;
  onClick: () => void;
}

interface NavigationOverlayProps {
  onClose: () => void;
}

export function NavigationOverlay({ onClose }: NavigationOverlayProps) {
  const toggleTerminal = useTerminalStore((state) => state.toggleTerminal);
  const openWorktreesViewer = useViewerStore((state) => state.openWorktreesViewer);
  const openProjectsViewer = useViewerStore((state) => state.openProjectsViewer);
  const navigate = useNavigate();
  const currentProject = useProjectStore((state) => state.currentProject);

  const menuItems: MenuItem[] = [
    {
      label: "Show Files",
      icon: FileText,
      shortcut: "⌘ B",
      onClick: () => {
        window.dispatchEvent(new CustomEvent('toggle-file-browser'));
        onClose();
      },
    },
    {
      label: "Show Terminal",
      icon: TerminalIcon,
      shortcut: "⌘ J",
      onClick: () => {
        toggleTerminal();
        onClose();
      },
    },
    {
      label: "Workspaces",
      icon: FolderGit2,
      shortcut: "⌘ W",
      onClick: () => {
        if (currentProject?.id) {
          openWorktreesViewer(currentProject.id);
        }
        onClose();
      },
    },
    {
      label: "Projects",
      icon: FolderOpen,
      shortcut: "⌘ P",
      onClick: () => {
        if (currentProject?.id) {
          openProjectsViewer(currentProject.id);
        }
        onClose();
      },
    },
    {
      label: "Workflow Builder",
      icon: Workflow,
      shortcut: "⌘ K",
      onClick: () => {
        navigate({ to: '/workflow' });
        onClose();
      },
    },
  ];

  return (
    <>
      {/* Backdrop */}
      <div 
        className="fixed inset-0 bg-black/50 backdrop-blur-sm z-50"
        onClick={onClose}
      />
      
      {/* Navigation Menu */}
      <div className="fixed inset-0 flex items-center justify-center z-50 pointer-events-none">
        <div 
          className="pointer-events-auto max-w-sm w-full mx-8 chat-background rounded-lg border border-border/50 shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="p-8">
            {/* Logo */}
            <div className="flex items-center justify-center mb-12">
              <BrandMark className="w-32 h-32 opacity-50" />
            </div>

            {/* Menu Items */}
            <div className="space-y-1">
              {menuItems.map((item) => {
                const Icon = item.icon;
                return (
                  <button
                    key={item.label}
                    onClick={item.onClick}
                    className="w-full flex items-center justify-between px-4 py-2.5 rounded-md transition-all duration-150 group text-left"
                    style={{
                      backgroundColor: 'transparent',
                    }}
                    onMouseEnter={(e) => {
                      e.currentTarget.style.backgroundColor = 'rgba(255, 255, 255, 0.1)';
                      e.currentTarget.style.borderColor = 'rgba(255, 255, 255, 0.2)';
                      e.currentTarget.style.border = '1px solid rgba(255, 255, 255, 0.2)';
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.backgroundColor = 'transparent';
                      e.currentTarget.style.border = 'none';
                    }}
                  >
                    <div className="flex items-center gap-2.5">
                      <Icon className="w-4 h-4 text-muted-foreground group-hover:text-primary transition-colors" />
                      <span className="text-sm text-muted-foreground group-hover:text-primary transition-colors font-medium">
                        {item.label}
                      </span>
                    </div>
                    <div className="flex items-center gap-0.5">
                      {item.shortcut.split(' ').map((key, idx) => (
                        <kbd
                          key={idx}
                          className="px-1.5 py-0.5 text-[10px] font-mono bg-muted/40 border border-border/40 rounded text-muted-foreground/80 group-hover:text-primary group-hover:border-primary/50 transition-colors"
                        >
                          {key}
                        </kbd>
                      ))}
                    </div>
                  </button>
                );
              })}
            </div>
          </div>
        </div>
      </div>
    </>
  );
}