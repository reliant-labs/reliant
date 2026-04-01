// Modern App Layout (2.0) - Navigation panel on right, cleaner UI

import { TabbedViewerPanel } from "./TabbedViewerPanel";
import { cn } from "../../lib/utils";

interface ModernAppLayoutProps {
  children: React.ReactNode; // Main chat interface
  hasOpenViewers: boolean;
  isTerminalOpen: boolean;
  currentProject: any;
}

export function ModernAppLayout({
  children,
  hasOpenViewers,
  isTerminalOpen,
  currentProject,
}: ModernAppLayoutProps) {
  return (
    <main className="flex flex-1 overflow-hidden">
      {/* Main Content Area */}
      <div
        className={cn(
          "flex-1 flex flex-col min-w-0 transition-all duration-200",
          isTerminalOpen && "pb-64"
        )}
      >
        {children}
      </div>

      {/* Right Side - Tabbed Viewer Panel (includes nav as background UI) */}
      {hasOpenViewers && (
        <TabbedViewerPanel hasTerminal={isTerminalOpen && !!currentProject} />
      )}
    </main>
  );
}
