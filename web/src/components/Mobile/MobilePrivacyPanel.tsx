/**
 * Mobile-native "Privacy" panel — reuses `usePrivacyStore` exactly as
 * desktop `PrivacySettings` does, so toggling crash reporting or analytics
 * here writes the same backend preference desktop reads.
 */

import { useEffect } from "react";
import { AlertTriangle } from "lucide-react";
import { usePrivacyStore } from "../../store/privacyStore";
import { MobileToggleRow } from "./MobileSettingsRow";

export function MobilePrivacyPanel() {
  const { crashReportingEnabled, analyticsEnabled, setCrashReporting, setAnalytics, initialize } =
    usePrivacyStore();

  useEffect(() => {
    initialize().catch((err) => {
      console.error("Failed to initialize privacy settings:", err);
    });
  }, [initialize]);

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
        description="Collect anonymous usage statistics to help improve the app."
        checked={analyticsEnabled}
        onChange={setAnalytics}
      />

      {(!crashReportingEnabled || !analyticsEnabled) && (
        <div className="flex items-start gap-3 border-y border-amber-500/40 bg-amber-500/10 px-4 py-3">
          <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-amber-500" />
          <div>
            <p className="text-sm font-medium text-foreground">Privacy mode active</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {!crashReportingEnabled && !analyticsEnabled
                ? "Crash reporting and analytics are currently disabled."
                : !crashReportingEnabled
                  ? "Crash reporting is currently disabled."
                  : "Analytics is currently disabled."}
            </p>
          </div>
        </div>
      )}

      <div className="bg-muted/30 p-4">
        <p className="mb-2 text-xs text-muted-foreground">
          <strong className="text-foreground">Note:</strong> Your code, conversations, and
          project data are always stored locally and never automatically shared.
        </p>
        <p className="mb-2 text-xs text-muted-foreground">
          <strong className="text-foreground">Changes take effect immediately</strong> — no
          restart required.
        </p>
        <p className="text-xs text-muted-foreground">
          <strong className="text-foreground">Default behavior:</strong> Data collection is
          enabled by default. You can disable it at any time.
        </p>
      </div>
    </div>
  );
}
