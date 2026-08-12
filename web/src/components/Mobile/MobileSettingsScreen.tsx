/**
 * `/m/settings` — a drill-in list of sections, each opening its own
 * full-screen sub-screen.
 *
 * Deliberately NOT the desktop sidebar+panel layout: `settings` stays false
 * on this surface (the ~15-section authoring/integration tree has no
 * business on a phone), and this screen is a separate, narrower
 * `settingsSection`-style axis in `lib/surface.ts` — a fixed list of
 * standalone screens, each gated by its own capability so a section that
 * regresses shows up as a one-line diff rather than a silent removal.
 *
 * Navigation is internal component state, not nested routes: `/m/settings`
 * is a single flat route (see `routes.tsx`, owned by another agent), so a
 * section click swaps which sub-screen renders rather than pushing a new
 * URL. Each sub-screen's own "back" button clears the selection.
 *
 * Sections not listed here (MCP, Prompts, Keyboard shortcuts, Developer,
 * Connectors, Port access rules) are desktop-only by design — see the
 * "Manage on desktop" note at the bottom of the list.
 */

import { useState } from "react";
import {
  Bell,
  ChevronRight,
  CircleDollarSign,
  FolderGit2,
  Github,
  Info,
  Monitor,
  Palette,
  Shield,
  Sparkles,
  User,
} from "lucide-react";
import { useCapabilities } from "../../lib/surfaceContext";
import type { SurfaceCapabilities } from "../../lib/surface";
import { hasControlPlane } from "../../services/controlPlane/config";
import { MobileAccountScreen } from "./MobileAccountScreen";
import { MobileAISettingsScreen } from "./MobileAISettingsScreen";
import { MobileBillingScreen } from "./MobileBillingScreen";
import { MobileGitHubScreen } from "./MobileGitHubScreen";
import { MobileNotificationsScreen } from "./MobileNotificationsScreen";
import { MobilePrivacyScreen } from "./MobilePrivacyScreen";
import { MobileAppearanceScreen } from "./MobileAppearanceScreen";
import { MobileWorkspacePreferencesScreen } from "./MobileWorkspacePreferencesScreen";
import { MobileAboutScreen } from "./MobileAboutScreen";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_ROW,
  MobileCardGroup,
  MobileRowIcon,
  MobileScreenBody,
  MobileScreenHeader,
} from "./MobileChrome";

type SectionId =
  | "account"
  | "ai"
  | "billing"
  | "github"
  | "notifications"
  | "privacy"
  | "appearance"
  | "workspace"
  | "about";

/**
 * Which card group a section renders in.
 *
 * A single 8-row list gave the eye nowhere to land — it read as one
 * undifferentiated column. Three labelled groups split it along the lines a
 * user actually reasons about: who you are, how the app behaves, what it
 * knows about you.
 */
type SectionGroupId = "identity" | "preferences" | "app";

// No group label may repeat a row label inside it ("Account" over an Account
// row): it reads as a duplicate rather than a heading, and it makes the row
// unaddressable by name for anything querying the screen.
const SECTION_GROUPS: { id: SectionGroupId; label: string }[] = [
  { id: "identity", label: "You" },
  { id: "preferences", label: "Preferences" },
  { id: "app", label: "Privacy & app" },
];

interface SectionLink {
  id: SectionId;
  label: string;
  description: string;
  icon: typeof Bell;
  group: SectionGroupId;
  /** `settings*` capability gating this row. */
  capability: keyof SurfaceCapabilities;
  /** Extra gate beyond the capability flag (e.g. cloud-only sections). */
  hidden?: boolean;
}

function SectionRow({
  section,
  onSelect,
}: {
  section: SectionLink;
  onSelect: (id: SectionId) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onSelect(section.id)}
      className={MOBILE_ROW}
    >
      <MobileRowIcon icon={section.icon} />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-foreground">{section.label}</p>
        <p className="truncate text-xs text-muted-foreground">
          {section.description}
        </p>
      </div>
      <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
    </button>
  );
}

export function MobileSettingsScreen() {
  const capabilities = useCapabilities();
  const [active, setActive] = useState<SectionId | null>(null);

  // Computed per render (not module scope) so `hasControlPlane` — read at
  // call time — can't get frozen into a stale constant, which matters both
  // for tests that mock it and for keeping this file's shape consistent
  // with SettingsNavigation's identical inline pattern.
  const sections: SectionLink[] = [
    {
      id: "account",
      label: "Account",
      description: "Identity, theme, sign out",
      icon: User,
      group: "identity",
      capability: "mobileAccount",
    },
    {
      id: "billing",
      label: "Billing",
      description: "Plan, credits, and usage",
      icon: CircleDollarSign,
      group: "identity",
      capability: "settingsBilling",
      hidden: !hasControlPlane,
    },
    {
      id: "github",
      label: "GitHub",
      description: "Connect your account and clone repos",
      icon: Github,
      group: "identity",
      capability: "settingsGitHub",
      hidden: !hasControlPlane,
    },
    {
      id: "ai",
      label: "AI providers",
      description: "API keys and default model",
      icon: Sparkles,
      group: "preferences",
      capability: "settingsAI",
    },
    {
      id: "appearance",
      label: "Appearance",
      description: "Theme, fonts, and layout",
      icon: Palette,
      group: "preferences",
      capability: "settingsAppearance",
    },
    {
      id: "notifications",
      label: "Notifications",
      description: "Alerts and sound",
      icon: Bell,
      group: "preferences",
      capability: "settingsNotifications",
    },
    {
      id: "workspace",
      label: "Workspace preferences",
      description: "Archive, branch, and delete defaults",
      icon: FolderGit2,
      group: "preferences",
      capability: "settingsWorkspace",
    },
    {
      id: "privacy",
      label: "Privacy",
      description: "Crash reports and analytics",
      icon: Shield,
      group: "app",
      capability: "settingsPrivacy",
    },
    {
      id: "about",
      label: "About",
      description: "Version and links",
      icon: Info,
      group: "app",
      capability: "settingsAbout",
    },
  ];

  const visible = sections.filter(
    (s) => !s.hidden && capabilities[s.capability],
  );

  if (active) {
    const onBack = () => setActive(null);
    switch (active) {
      case "account":
        return <MobileAccountScreen onBack={onBack} />;
      case "ai":
        return <MobileAISettingsScreen onBack={onBack} />;
      case "billing":
        return <MobileBillingScreen onBack={onBack} />;
      case "github":
        return <MobileGitHubScreen onBack={onBack} />;
      case "notifications":
        return <MobileNotificationsScreen onBack={onBack} />;
      case "privacy":
        return <MobilePrivacyScreen onBack={onBack} />;
      case "appearance":
        return <MobileAppearanceScreen onBack={onBack} />;
      case "workspace":
        return <MobileWorkspacePreferencesScreen onBack={onBack} />;
      case "about":
        return <MobileAboutScreen onBack={onBack} />;
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The menu button is the only way off a top-level destination — without
          it this screen is reachable from the drawer and then inescapable
          except via the browser's back gesture. */}
      <MobileScreenHeader title="Settings" leading={<MobileMenuButton />} />

      <MobileScreenBody>
        {SECTION_GROUPS.map(({ id, label }) => {
          const rows = visible.filter((s) => s.group === id);
          // A group whose every section is capability-gated off renders as a
          // stray heading over an empty card, which looks like a load failure.
          if (rows.length === 0) return null;
          return (
            <MobileCardGroup key={id} label={label}>
              {rows.map((section) => (
                <SectionRow
                  key={section.id}
                  section={section}
                  onSelect={setActive}
                />
              ))}
            </MobileCardGroup>
          );
        })}

        {/* Styled as a quiet informational card rather than loose footer prose:
            it explains a deliberate absence, so it should read as content
            rather than as a caption someone forgot to place. */}
        <div className="flex gap-3 rounded-lg border border-border bg-muted/40 p-4">
          <Monitor className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <p className="text-xs leading-relaxed text-muted-foreground">
            MCP servers, prompts, keyboard shortcuts, developer tools,
            connectors, and port access rules aren&apos;t available here.
            Manage those on desktop.
          </p>
        </div>
      </MobileScreenBody>
    </div>
  );
}
