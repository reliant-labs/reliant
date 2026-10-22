import { useState, useEffect, Suspense, lazy, useRef, useMemo } from "react";
import { Settings as SettingsIcon } from "lucide-react";
import {
  SettingsNavigation,
  getVisibleSettingsSectionIds,
  type SettingsSection,
} from "./SettingsNavigation";
import { cn } from "../../lib/utils";

const SettingsContent = lazy(() =>
  import("./SettingsContent").then((module) => ({
    default: module.SettingsContent,
  }))
);

interface SettingsViewerTabProps {
  // The URL-driven section. Controlled by SettingsPage which reads it from
  // useParams and validates it against SETTINGS_SECTION_IDS.
  section: SettingsSection;
  // Called with the next section when the user clicks a nav item or uses arrow
  // keys. SettingsPage turns this into a navigate({to:'/settings/$section'}).
  onSectionChange: (section: SettingsSection) => void;
}

export function SettingsViewerTab({ section, onSectionChange }: SettingsViewerTabProps) {
  const [isCollapsed, setIsCollapsed] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Same order as SettingsNavigation sidebar (single source: settingsSections)
  const visibleSections = useMemo(
    () => getVisibleSettingsSectionIds(),
    []
  );

  // If the URL points at a section that isn't visible (e.g. /settings/developer
  // in a non-dev build), redirect to the first visible one. Doing this via the
  // parent's onSectionChange keeps URL the source of truth.
  useEffect(() => {
    if (!visibleSections.includes(section)) {
      const fallback = visibleSections[0] ?? "account";
      onSectionChange(fallback);
    }
  }, [section, visibleSections, onSectionChange]);

  // Keyboard navigation for settings tabs
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Only handle if we're in settings mode and not typing in an input
      // Check if settings container is visible
      if (!containerRef.current) return;

      const target = e.target as HTMLElement;
      const isInputField =
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true" ||
        target.closest("input") !== null ||
        target.closest("textarea") !== null ||
        target.closest("[contenteditable='true']") !== null;

      if (isInputField) {
        return; // Don't interfere with typing
      }

      // Handle ArrowUp and ArrowDown for navigation
      if (e.key === "ArrowUp" || e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();

        const currentIndex = visibleSections.indexOf(section);
        if (currentIndex === -1) return;

        let newIndex: number;
        if (e.key === "ArrowUp") {
          newIndex = currentIndex > 0 ? currentIndex - 1 : visibleSections.length - 1;
        } else {
          newIndex = currentIndex < visibleSections.length - 1 ? currentIndex + 1 : 0;
        }

        const newSection = visibleSections[newIndex];
        if (newSection) {
          onSectionChange(newSection);
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown, true);
    return () => {
      window.removeEventListener("keydown", handleKeyDown, true);
    };
  }, [section, visibleSections, onSectionChange]);

  // Detect width and collapse sidebar when narrow
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const width = entry.contentRect.width;
        // Collapse when width is less than 600px
        setIsCollapsed(width < 600);
      }
    });

    observer.observe(container);

    return () => {
      observer.disconnect();
    };
  }, []);

  // Get API URL for settings
  const getApiUrl = () => {
    if (
      typeof window !== "undefined" &&
      window.RELIANT_CONFIG?.isElectron &&
      window.RELIANT_CONFIG?.grpcUrl
    ) {
      return window.RELIANT_CONFIG.grpcUrl;
    }
    return "";
  };

  return (
    <div ref={containerRef} className="flex h-full min-w-[500px] bg-background">
      {/* Settings Navigation Sidebar */}
      <aside
        className={cn(
          "flex-shrink-0 overflow-y-auto border-r border-border/60 bg-card transition-all duration-200",
          isCollapsed ? "w-16 min-w-16" : "w-[220px] min-w-[220px]"
        )}
      >
        <div className={cn("px-3 pb-2 pt-3", isCollapsed && "px-2")}>
          <div
            className={cn(
              "flex items-center gap-2 rounded-lg border border-border/40 bg-background/50 p-2",
              isCollapsed && "justify-center p-1.5"
            )}
          >
            <div className="flex h-7 w-7 items-center justify-center rounded-md border border-border/50 bg-card text-muted-foreground">
              <SettingsIcon className="h-3.5 w-3.5" />
            </div>
            {!isCollapsed && (
              <div className="min-w-0">
                <h1 className="truncate text-sm font-semibold text-foreground">Settings</h1>
                <p className="truncate text-xs text-muted-foreground">Configure Reliant</p>
              </div>
            )}
          </div>
        </div>
        <SettingsNavigation
          activeSection={section}
          onSectionChange={onSectionChange}
          isCollapsed={isCollapsed}
        />
      </aside>

      {/* Settings Content */}
      <main className="min-w-0 flex-1 overflow-hidden bg-background">
        <Suspense
          fallback={
            <div className="flex h-full items-center justify-center">
              <div className="rounded-lg border border-border/50 bg-card px-4 py-3 text-sm text-muted-foreground">
                Loading settings...
              </div>
            </div>
          }
        >
          <SettingsContent
            activeSection={section}
            apiUrl={getApiUrl()}
          />
        </Suspense>
      </main>
    </div>
  );
}