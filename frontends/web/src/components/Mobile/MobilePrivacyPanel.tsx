/**
 * Mobile-native "Privacy" panel — reads and writes privacy consent exactly as
 * desktop `PrivacySettings` does, so toggling crash reporting or analytics
 * here writes the same backend preference desktop reads. The note under the
 * switches is desktop's "Where your data lives", shortened.
 */

import { ExternalLink } from "lucide-react";
import { usePrivacyConsent, useUpdatePrivacySettings } from "../../hooks/privacy-queries";
import { MobileToggleRow } from "./MobileSettingsRow";
import { hasControlPlane } from "../../services/controlPlane/config";
import { PRIVACY_DOCS_URL } from "../Settings/privacyLinks";

export function MobilePrivacyPanel() {
  const { crashReportingEnabled, analyticsEnabled } = usePrivacyConsent();
  const { setCrashReporting, setAnalytics } = useUpdatePrivacySettings();
  const servers = hasControlPlane ? "Reliant's servers" : "your Reliant server";

  return (
    <div className="divide-y divide-border">
      <MobileToggleRow
        label="Crash and error reporting"
        description="Send crash reports and error logs, including stack traces, to help fix issues."
        checked={crashReportingEnabled}
        onChange={setCrashReporting}
      />
      <MobileToggleRow
        label="Analytics and usage data"
        description="Send product usage events, tied to your account, to help improve the app."
        checked={analyticsEnabled}
        onChange={setAnalytics}
      />

      <div className="space-y-2 bg-background p-4 text-xs text-muted-foreground">
        <p>
          <strong className="text-foreground">Chats are stored on {servers}</strong>, with what agents read and
          commands print. Your code stays on the machine it runs on; a Reliant cloud machine keeps the whole
          repository on its disk.
        </p>
        <p>These switches only control crash reports and usage analytics. A change applies immediately.</p>
        <a
          href={PRIVACY_DOCS_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex min-h-[44px] items-center gap-1 font-medium text-primary"
        >
          What is stored where
          <ExternalLink className="h-3 w-3" aria-hidden="true" />
        </a>
      </div>
    </div>
  );
}
