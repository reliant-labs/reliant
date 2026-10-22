/**
 * `/m/settings` → "GitHub" section.
 *
 * Two internal views behind one back-stack slot, the same drill-in-within-
 * drill-in pattern `MobileSettingsScreen` itself uses for its top-level
 * sections: `MobileGitHubPanel` (connection status/connect/disconnect) is
 * the landing view, and tapping "Clone a repo" pushes
 * `MobileGitHubRepoList` in its place. Repo browsing lives here rather than
 * on `/m/projects` — see that screen's own module comment, which already
 * scopes it to list+select because clone needs more surface than a phone
 * screen hosts well; this is that surface, reached from the credential it
 * depends on rather than bolted onto the project picker.
 */

import { useState } from "react";
import { ChevronRight, Github } from "lucide-react";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import {
  MobileCardGroup,
  MOBILE_ROW,
  MobileRowIcon,
  MobileScreenHeader,
} from "./MobileChrome";
import { MobileGitHubPanel } from "./MobileGitHubPanel";
import { MobileGitHubRepoList } from "./MobileGitHubRepoList";
import { MobileMenuButton } from "./MobileMenuButton";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobileGitHubScreen({ onBack }: { onBack?: () => void }) {
  const [browsing, setBrowsing] = useState(false);
  const { hasToken } = useGitHubCredential();

  if (browsing) {
    return <MobileGitHubRepoList onBack={() => setBrowsing(false)} />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {onBack ? (
        <MobileSettingsSectionHeader title="GitHub" onBack={onBack} />
      ) : (
        <MobileScreenHeader title="GitHub" leading={<MobileMenuButton />} />
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <MobileGitHubPanel />

        {hasToken && (
          <div className="px-4 pb-4">
            <MobileCardGroup label="Repositories">
              <button
                type="button"
                onClick={() => setBrowsing(true)}
                className={MOBILE_ROW}
              >
                <MobileRowIcon icon={Github} />
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium text-foreground">Clone a repo</p>
                  <p className="truncate text-xs text-muted-foreground">
                    Browse your repositories and clone one onto a machine
                  </p>
                </div>
                <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
              </button>
            </MobileCardGroup>
          </div>
        )}
      </div>
    </div>
  );
}
