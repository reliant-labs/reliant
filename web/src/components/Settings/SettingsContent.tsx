import { AISettings } from "./AISettings";
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
import { ConnectorSettings } from "./ConnectorSettings";
import { GitConnectionsSettings } from "./GitConnectionsSettings";
import type { SettingsSection } from "./SettingsNavigation";
import { lazy, Suspense, useEffect, useState } from "react";
import { PromptsSettings } from "./PromptsSettings";
import { useProjectStore } from "../../store/projectStore";
import { api } from "../../api/client";
import { ProjectPanel } from "../Projects/ProjectPanel";

// Cloud settings sections are lazy-loaded so they code-split out of the main
// SettingsContent chunk — they're only fetched when the user opens a cloud
// section. Named exports are adapted to the default export React.lazy expects.
const BillingSection = lazy(() =>
  import("./cloud/billing").then((m) => ({ default: m.BillingSection }))
);
const EnvironmentsSection = lazy(() =>
  import("./cloud/environments").then((m) => ({ default: m.EnvironmentsSection }))
);

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
    if (activeSection === "connectors") {
      return <ConnectorSettings />;
    }
    if (activeSection === "git-connections") {
      return <GitConnectionsSettings />;
    }
    if (activeSection === "developer") {
      return <DeveloperSettings />;
    }
    return null;
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

  // The "AI" section is a tabbed container (bring-your-own providers + managed
  // Reliant AI). It gets its own full-height wrapper with a wider container so
  // the Reliant AI tab's data tables have room; the providers tab renders inside
  // its own narrow card (see AISettings).
  if (activeSection === "general") {
    return (
      <div className="h-full overflow-auto px-8 py-8">
        <div className="mx-auto max-w-5xl">
          <AISettings
            providers={providers}
            onProvidersUpdate={fetchProviderStatuses}
          />
        </div>
      </div>
    );
  }

  // Cloud settings sections. Rendered inside the `.cloud-settings` scoped
  // treatment (Inter + admin-like density) with a wider container than the
  // generic settings card so their data tables have room to breathe. The id →
  // component map is the contract the vertical agents plug into:
  //   billing        → <BillingSection/>      (./cloud/billing)
  //   environments   → <EnvironmentsSection/> (./cloud/environments)
  if (activeSection === "billing" || activeSection === "environments") {
    const CloudSection =
      activeSection === "billing" ? BillingSection : EnvironmentsSection;
    return (
      <div className="cloud-settings h-full overflow-auto bg-background px-8 py-8">
        <div className="mx-auto max-w-5xl">
          <Suspense
            fallback={
              <div className="flex h-full items-center justify-center">
                <div className="rounded-lg border border-border/50 bg-card px-4 py-3 text-sm text-muted-foreground">
                  Loading…
                </div>
              </div>
            }
          >
            <CloudSection />
          </Suspense>
        </div>
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