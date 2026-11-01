import { AISettings } from "./AISettings";
import { AboutSection } from "./AboutSection";
import { DeveloperSettings } from "./DeveloperSettings";
import { AppearanceSettings } from "./AppearanceSettings";
import { KeyboardShortcutsSettings } from "./KeyboardShortcutsSettings";
import { AccountSettings } from "./AccountSettings";
import { PrivacySettings } from "./PrivacySettings";
import { NotificationSettings } from "./NotificationSettings";
import { MCPSettings } from "./MCPSettings";
import { BranchesSection } from "./BranchesSection";
import { BrowserSettings } from "./BrowserSettings";
import { TokenSettings } from "./TokenSettings";
import { MemberPermissions } from "./cloud/MemberPermissions";
import { GitConnectionsSettings } from "./GitConnectionsSettings";
import type { SettingsSection } from "./SettingsNavigation";
import { lazy, Suspense, type ReactNode } from "react";
import { PromptsSettings } from "./PromptsSettings";
import { PresetsSettings } from "./PresetsSettings";
import { useProjectStore } from "../../store/projectStore";
import { ProjectsSection } from "./ProjectsSection";
import { AutomationSettings } from "./AutomationSettings";

// Cloud settings sections are lazy-loaded so they code-split out of the main
// SettingsContent chunk — they're only fetched when the user opens a cloud
// section. Named exports are adapted to the default export React.lazy expects.
const BillingSection = lazy(() =>
  import("./cloud/billing").then((m) => ({ default: m.BillingSection }))
);
const MachinesSection = lazy(() =>
  import("./cloud/machines").then((m) => ({ default: m.MachinesSection }))
);

interface SettingsContentProps {
  activeSection: SettingsSection;
}

/**
 * Every settings page in ONE layout: the page scrolls on the app background,
 * its content sits in one column of one width, the section's
 * SettingsPageHeader opens it on that background, and its blocks are bg-card
 * panels under the header.
 *
 * This replaced five widths (a 700px card, 700px inside 7xl, max-w-4xl,
 * max-w-5xl, 7xl, and full width), a generic card that wrapped whole pages —
 * so every panel inside it was a card in a card — with a `[&_h3]:text-sm`
 * parent selector that inverted the heading ladder, and a `.cloud-settings`
 * scope that switched Billing and Machines to a different font. A section
 * draws its own panels; nothing here wraps it in one.
 */
function SettingsLayout({ children }: { children: ReactNode }) {
  return (
    <div className="h-full overflow-auto bg-background px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
      <div className="mx-auto w-full max-w-4xl" data-testid="settings-layout">
        {children}
      </div>
    </div>
  );
}

export function SettingsContent({ activeSection }: SettingsContentProps) {
  const currentProject = useProjectStore((state) => state.currentProject);

  const renderContent = () => {
    switch (activeSection) {
      case "account":
        return <AccountSettings />;
      case "ai":
        return <AISettings />;
      case "shortcuts":
        return <KeyboardShortcutsSettings />;
      case "about":
        return <AboutSection />;
      case "appearance":
        return <AppearanceSettings />;
      case "notifications":
        return <NotificationSettings />;
      case "privacy":
        return <PrivacySettings />;
      case "browser":
        return <BrowserSettings />;
      case "prompts":
        return <PromptsSettings projectId={currentProject?.id} />;
      case "mcp":
        return <MCPSettings />;
      case "tokens":
        return <TokenSettings />;
      case "member-permissions":
        return <MemberPermissions />;
      case "git-connections":
        return <GitConnectionsSettings />;
      case "developer":
        return <DeveloperSettings />;
      case "projects":
        return <ProjectsSection />;
      case "presets":
        return <PresetsSettings />;
      case "branches":
        return <BranchesSection />;
      case "automations":
        return <AutomationSettings />;
      case "billing":
      case "machines": {
        const CloudSection = activeSection === "billing" ? BillingSection : MachinesSection;
        return (
          <Suspense
            fallback={
              <div className="rounded-lg border border-border bg-card px-4 py-3 text-sm text-muted-foreground">
                Loading…
              </div>
            }
          >
            <CloudSection />
          </Suspense>
        );
      }
    }
  };

  return <SettingsLayout>{renderContent()}</SettingsLayout>;
}
