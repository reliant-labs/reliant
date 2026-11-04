import { useState, useEffect, Suspense, lazy, useRef, useMemo } from "react";
import { SettingsHeader } from "./SettingsHeader";
import { useTitleBarChrome } from "../../hooks/useTitleBarChrome";
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
  onClose: () => void;
}

export function SettingsViewerTab({ section, onSectionChange, onClose }: SettingsViewerTabProps) {
  const [isCollapsed, setIsCollapsed] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const navigationRef = useRef<HTMLDivElement>(null);
  const chrome = useTitleBarChrome();

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
      // Arrow keys belong to the navigation only, never a form, dialog or
      // the exit button. Keep focus with the section the user just selected.
      const target = e.target as HTMLElement;
      if (!navigationRef.current?.contains(target) || !target.closest("button[aria-current], button[aria-label]")) return;

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
          const buttons = navigationRef.current.querySelectorAll<HTMLButtonElement>("nav button");
          buttons[newIndex]?.focus();
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

  return (
    <div ref={containerRef} className="flex h-full min-w-0 bg-background">
      {/* The exit stays put while a short window scrolls its section list. */}
      <aside
        aria-label="Settings"
        className={cn(
          "flex shrink-0 flex-col border-r border-border/60 bg-card",
          // macOS's native traffic lights need 80px even in the compact rail.
          isCollapsed ? (chrome.isMac && !chrome.isFullscreen ? "w-24" : "w-16") : "w-[220px]",
        )}
      >
        {chrome.isMac && !chrome.isFullscreen && (
          <div aria-hidden="true" className="h-10 shrink-0" style={chrome.dragRegionStyle} />
        )}
        <SettingsHeader onClose={onClose} isCollapsed={isCollapsed} {...chrome} />
        <div ref={navigationRef} className="min-h-0 flex-1 overflow-y-auto">
          <SettingsNavigation
            activeSection={section}
            onSectionChange={onSectionChange}
            isCollapsed={isCollapsed}
          />
        </div>
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
          <SettingsContent activeSection={section} />
        </Suspense>
      </main>
    </div>
  );
}