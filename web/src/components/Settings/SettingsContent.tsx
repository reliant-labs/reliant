import { CombinedGeneralSettings } from "./CombinedGeneralSettings";
import { AboutSection } from "./AboutSection";
import { DeveloperSettings } from "./DeveloperSettings";
import { AppearanceSettings } from "./AppearanceSettings";
import { KeyboardShortcutsSettings } from "./KeyboardShortcutsSettings";
import { AccountSettings } from "./AccountSettings";
import { PrivacySettings } from "./PrivacySettings";
import { NotificationSettings } from "./NotificationSettings";
import { MCPSettings } from "./MCPSettings";
import { WorkspacesSection } from "./WorkspacesSection";
import { BrowserSettings } from "./BrowserSettings";
import { TokenSettings } from "./TokenSettings";
import type { SettingsSection } from "./SettingsNavigation";
import { useEffect, useState } from "react";
import { PromptsSettings } from "./PromptsSettings";
import { useProjectStore } from "../../store/projectStore";
import { api } from "../../api/client";
import { ProjectPanel } from "../Projects/ProjectPanel";

interface SettingsContentProps {
  activeSection: SettingsSection;
  apiUrl: string;
}

interface ProviderStatus {
  provider: string;
  configured: boolean;
  hasApiKey: boolean;
  maskedKey?: string;
  displayName: string;
}

export function SettingsContent({
  apiUrl,
  activeSection,
}: SettingsContentProps) {
  const [providers, setProviders] = useState<ProviderStatus[]>([]);
  const currentProject = useProjectStore((state) => state.currentProject);

  useEffect(() => {
    fetchProviderStatuses();
  }, [apiUrl]);

  const fetchProviderStatuses = async () => {
    try {
      const data = await api.settings.getProviders();
      setProviders(data || []);
    } catch (error) {
      console.error("Failed to fetch provider statuses:", error);
      setProviders([]);
    }
  };

  const renderContent = () => {
    if (activeSection === "account") {
      return <AccountSettings />;
    }
    if (activeSection === "shortcuts") {
      return <KeyboardShortcutsSettings />;
    }
    if (activeSection === "about") {
      return <AboutSection />;
    }
    if (activeSection === "appearance") {
      return <AppearanceSettings />;
    }
    if (activeSection === "notifications") {
      return <NotificationSettings />;
    }
    if (activeSection === "privacy") {
      return <PrivacySettings />;
    }
    if (activeSection === "browser") {
      return <BrowserSettings />;
    }
    if (activeSection === "prompts") {
      return (
        <PromptsSettings
          projectId={currentProject?.id}
        />
      );
    }

    if (activeSection === "mcp") {
      return <MCPSettings />;
    }
    if (activeSection === "tokens") {
      return <TokenSettings />;
    }
    if (activeSection === "developer") {
      return <DeveloperSettings />;
    }
    return (
      <CombinedGeneralSettings
        providers={providers}
        onProvidersUpdate={fetchProviderStatuses}
      />
    );
  };

  // Special handling for sections that need full height/width
  if (activeSection === "about") {
    return (
      <div className="h-full overflow-auto px-8 py-8">
        <div className="mx-auto max-w-5xl rounded-xl border border-border/50 bg-card p-6 shadow-sm">
          <AboutSection />
        </div>
      </div>
    );
  }

  if (activeSection === "projects") {
    return (
      <div className="h-full overflow-auto bg-background">
        <ProjectPanel />
      </div>
    );
  }

  if (activeSection === "workspaces") {
    return (
      <div className="h-full overflow-hidden bg-background">
        <WorkspacesSection />
      </div>
    );
  }

  return (
    <div className="h-full overflow-auto px-8 py-8">
      <div className="mx-auto max-w-[700px] rounded-xl border border-border/50 bg-card p-6 shadow-sm [&_h2]:text-xl [&_h2]:font-bold [&_h2]:tracking-tight [&_h2]:text-foreground [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground">
        {renderContent()}
      </div>
    </div>
  );
}