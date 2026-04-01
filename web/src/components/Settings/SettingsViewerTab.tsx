import { useState, useEffect, Suspense, lazy, useRef, useMemo } from "react";
import {
  SettingsNavigation,
  getVisibleSettingsSectionIds,
  type SettingsSection,
} from "./SettingsNavigation";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { api } from "../../api/client";

const SettingsContent = lazy(() =>
  import("./SettingsContent").then((module) => ({
    default: module.SettingsContent,
  }))
);

interface SettingsViewerTabProps {
  initialSection?: string;
}

// Get persisted section from localStorage (sync read for initial render)
const getPersistedSection = (): SettingsSection => {
  const saved = settingsSync.getSetting(SETTINGS_KEYS.SETTINGS_SECTION, "account");
  return saved as SettingsSection;
};

export function SettingsViewerTab({ initialSection }: SettingsViewerTabProps) {
  const [activeSection, setActiveSection] = useState<SettingsSection>(
    (initialSection as SettingsSection) || getPersistedSection()
  );
  const [isCollapsed, setIsCollapsed] = useState(false);
  const [skillsEnabled, setSkillsEnabled] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Update active section when initialSection prop changes (e.g., from onboarding wizard)
  useEffect(() => {
    if (initialSection) {
      setActiveSection(initialSection as SettingsSection);
    }
  }, [initialSection]);

  useEffect(() => {
    let isMounted = true;

    const loadSkillsFeature = async () => {
      try {
        const setting = await api.settings.getSetting("features.skills_enabled");
        const enabled = setting?.value?.toLowerCase() === "true";
        if (isMounted) {
          setSkillsEnabled(enabled);
        }
      } catch {
        if (isMounted) {
          setSkillsEnabled(false);
        }
      }
    };

    loadSkillsFeature();

    return () => {
      isMounted = false;
    };
  }, []);

  // Listen for navigation events from other components
  useEffect(() => {
    const handleNavigate = (event: CustomEvent<{ section: string }>) => {
      const requestedSection = event.detail.section as SettingsSection;
      if (requestedSection === "skills" && !skillsEnabled) {
        setActiveSection("account");
        return;
      }
      setActiveSection(requestedSection);
    };

    window.addEventListener('navigate-to-settings-section', handleNavigate as EventListener);
    return () => {
      window.removeEventListener('navigate-to-settings-section', handleNavigate as EventListener);
    };
  }, [skillsEnabled]);

  // Persist active section when it changes
  useEffect(() => {
    settingsSync.setSetting(SETTINGS_KEYS.SETTINGS_SECTION, activeSection).catch(console.error);
  }, [activeSection]);

  // Same order as SettingsNavigation sidebar (single source: settingsSections)
  const visibleSections = useMemo(
    () => getVisibleSettingsSectionIds(skillsEnabled),
    [skillsEnabled]
  );

  useEffect(() => {
    if (!visibleSections.includes(activeSection)) {
      setActiveSection(visibleSections[0] ?? "account");
    }
  }, [activeSection, visibleSections]);

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

        const currentIndex = visibleSections.indexOf(activeSection);
        if (currentIndex === -1) return;

        let newIndex: number;
        if (e.key === "ArrowUp") {
          newIndex = currentIndex > 0 ? currentIndex - 1 : visibleSections.length - 1;
        } else {
          newIndex = currentIndex < visibleSections.length - 1 ? currentIndex + 1 : 0;
        }

        const newSection = visibleSections[newIndex];
        if (newSection) {
          setActiveSection(newSection);
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown, true);
    return () => {
      window.removeEventListener("keydown", handleKeyDown, true);
    };
  }, [activeSection, visibleSections]);

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
      window.RELIANT_CONFIG?.backendUrl
    ) {
      return window.RELIANT_CONFIG.backendUrl;
    }
    return "";
  };

  return (
    <div ref={containerRef} className="flex h-full bg-background min-w-[500px]">
      {/* Settings Navigation Sidebar */}
      <div
        className="border-r border-border flex-shrink-0 overflow-y-auto transition-all duration-200"
        style={{
          width: isCollapsed ? '64px' : '256px',
          minWidth: isCollapsed ? '64px' : '200px',
        }}
      >
        <SettingsNavigation
          activeSection={activeSection}
          onSectionChange={setActiveSection}
          isCollapsed={isCollapsed}
          skillsEnabled={skillsEnabled}
        />
      </div>

      {/* Settings Content */}
      <div className="flex-1 overflow-hidden">
        <Suspense
          fallback={
            <div className="flex items-center justify-center h-full">
              <div className="text-sm text-muted-foreground">Loading settings...</div>
            </div>
          }
        >
          <SettingsContent
            activeSection={activeSection}
            apiUrl={getApiUrl()}
          />
        </Suspense>
      </div>
    </div>
  );
}
