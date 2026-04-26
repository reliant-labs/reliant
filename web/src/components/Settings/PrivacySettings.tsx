import { useEffect } from "react";
import { usePrivacyStore } from "../../store/privacyStore";
import { Shield, AlertTriangle } from "lucide-react";
import { Toggle } from "../ui/Toggle";

export function PrivacySettings() {
  const {
    crashReportingEnabled,
    analyticsEnabled,
    setCrashReporting,
    setAnalytics,
    initialize,
  } = usePrivacyStore();

  useEffect(() => {
    // Initialize privacy settings (async to load from Electron if available)
    initialize().catch((err) => {
      console.error('Failed to initialize privacy settings:', err);
    });
  }, [initialize]);

  return (
    <div className="space-y-6">
      <div>
        <div className="flex items-center gap-2 mb-2">
          <Shield className="w-5 h-5" />
          <h2 className="text-lg font-semibold">Privacy Settings</h2>
        </div>
        <p className="text-sm text-muted-foreground">
          Control what data you share with us to improve the application.
        </p>
      </div>

      <div className="space-y-4">
        {/* Crash Reporting Setting */}
        <div className="flex items-start justify-between p-4 border border-border/40 rounded-lg bg-card shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
          <div className="flex-1 pr-4">
            <div className="flex items-center gap-2 mb-1">
              <h3 className="text-sm font-medium">Crash and Error Reporting</h3>
            </div>
            <p className="text-xs text-muted-foreground mb-2">
              Automatically send crash reports and error logs to help us
              identify and fix issues. This includes stack traces and diagnostic
              information.
            </p>
            <p className="text-xs text-muted-foreground">
              Provider: <span>Sentry</span>
            </p>
          </div>
          <Toggle
            checked={crashReportingEnabled}
            onChange={setCrashReporting}
            label={`${crashReportingEnabled ? "Disable" : "Enable"} crash reporting`}
          />
        </div>

        {/* Analytics Setting */}
        <div className="flex items-start justify-between p-4 border border-border/40 rounded-lg bg-card shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
          <div className="flex-1 pr-4">
            <div className="flex items-center gap-2 mb-1">
              <h3 className="text-sm font-medium">Analytics and Usage Data</h3>
            </div>
            <p className="text-xs text-muted-foreground mb-2">
              Collect anonymous usage statistics and feature analytics to help
              us understand how the application is used and improve user
              experience.
            </p>
            <p className="text-xs text-muted-foreground">
              Provider: <span>Statsig</span>
            </p>
          </div>
          <Toggle
            checked={analyticsEnabled}
            onChange={setAnalytics}
            label={`${analyticsEnabled ? "Disable" : "Enable"} analytics`}
          />
        </div>

        {/* Privacy mode notice - shown when analytics/crash reporting is disabled */}
        {(!crashReportingEnabled || !analyticsEnabled) && (
          <div className="flex items-start gap-3 p-4 border border-amber-500/40 bg-amber-500/10 rounded-lg">
            <AlertTriangle className="w-5 h-5 text-amber-500 flex-shrink-0 mt-0.5" />
            <div className="flex-1">
              <p className="text-sm text-foreground font-medium mb-1">
                Privacy Mode Active
              </p>
              <p className="text-xs text-muted-foreground">
                {!crashReportingEnabled && !analyticsEnabled
                  ? "Crash reporting and analytics are currently disabled."
                  : !crashReportingEnabled
                  ? "Crash reporting is currently disabled."
                  : "Analytics is currently disabled."}
              </p>
            </div>
          </div>
        )}

        {/* Information notice */}
        <div className="border-t border-border/30 pt-5 mt-5"></div>
        <div className="p-4 border border-border/40 rounded-lg bg-muted/30">
          <p className="text-xs text-muted-foreground mb-2">
            <strong className="text-foreground">Note:</strong> These settings control telemetry and crash
            reporting. Your code, conversations, and project data are always
            stored locally and never automatically shared unless you explicitly
            share them.
          </p>
          <p className="text-xs text-muted-foreground mb-2">
            <strong className="text-foreground">Changes take effect immediately</strong> - no restart required.
          </p>
          <p className="text-xs text-muted-foreground">
            <strong className="text-foreground">Default behavior:</strong> Data collection is enabled by default (opt-out model). 
            You can disable it at any time.
          </p>
        </div>
      </div>
    </div>
  );
}