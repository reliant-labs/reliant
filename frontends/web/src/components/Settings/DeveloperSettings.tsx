import { useState, useEffect } from "react";
import Button from "@/components/forge-ui/button";
import Card from "../forge-ui/card";
import { RefreshCw, AlertCircle, CheckCircle } from "lucide-react";
import { isForgeUIEnabled, setForgeUIEnabled } from "../../lib/forgeFeature";
import { SettingsPageHeader } from "./SettingsPageHeader";

interface BackendStatus {
  isRunning: boolean;
  port: number;
}

export function DeveloperSettings() {
  const [backendStatus, setBackendStatus] = useState<BackendStatus | null>(null);
  const [isRestarting, setIsRestarting] = useState(false);
  // Seeded from the live gate so the checkbox shows the EFFECTIVE value (which in
  // a dev build is on by default), not a hardcoded false that would misreport it.
  const [forgeUIOn, setForgeUIOn] = useState<boolean>(() => isForgeUIEnabled());

  useEffect(() => {
    checkBackendStatus();
  }, []);

  const checkBackendStatus = async () => {
    try {
      const status = await window.electronAPI.getBackendStatus();
      setBackendStatus(status as BackendStatus);
    } catch (error) {
      console.error('Failed to check backend status:', error);
    }
  };

  const handleRestartBackend = async () => {
    setIsRestarting(true);
    try {
      const result = await window.electronAPI.restartBackend();
      if (result.success) {
        setTimeout(() => {
          checkBackendStatus();
          setIsRestarting(false);
        }, 2000);
      } else {
        console.error('Failed to restart backend:', result.error);
        setIsRestarting(false);
      }
    } catch (error) {
      console.error('Failed to restart backend:', error);
      setIsRestarting(false);
    }
  };

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Developer"
        description="Tools for testing Reliant itself, such as backend status and experimental features. You only need this page if you are developing or debugging Reliant."
      />

      {/* Backend Status */}
      <Card padding="none">
        <div className="p-4">
          <h3 className="text-sm font-semibold mb-3">Backend Status</h3>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-sm">Status</span>
              <div className="flex items-center gap-2">
                {backendStatus?.isRunning ? (
                  <>
                    <CheckCircle className="w-4 h-4 text-success" />
                    <span className="text-sm text-success-ink">Running on port {backendStatus.port}</span>
                  </>
                ) : (
                  <>
                    <AlertCircle className="w-4 h-4 text-destructive-ink" />
                    <span className="text-sm text-destructive-ink">Not running</span>
                  </>
                )}
              </div>
            </div>
            <Button
              onClick={handleRestartBackend}
              disabled={isRestarting}
              size="sm"
              className="w-full"
            >
              {isRestarting ? (
                <>
                  <RefreshCw className="w-4 h-4 mr-2 animate-spin" />
                  Restarting...
                </>
              ) : (
                <>
                  <RefreshCw className="w-4 h-4 mr-2" />
                  Restart Backend
                </>
              )}
            </Button>
          </div>
        </div>
      </Card>

      {/* Experimental: the forge UI.
          The gate lives in lib/forgeFeature so the route guard, the sidebar
          entry and this toggle cannot disagree — see that file for why it is a
          synchronous localStorage read rather than a server setting. */}
      <Card padding="none">
        <div className="p-4">
          <h3 className="text-sm font-semibold mb-2">Experimental Features</h3>
          <p className="text-xs text-muted-foreground mb-4">
            Optional surfaces. Changing one affects only this browser.
          </p>

          <div className="space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <label htmlFor="forge-ui-enabled" className="text-sm">
                  Forge environment UI
                </label>
                <p className="text-xs text-muted-foreground mt-1">
                  Release topology, environment secrets, runtime checks, and the
                  promote/deploy flows for forge projects. Adds a Forge entry to the
                  sidebar. Deploying from this UI applies to a real cluster.
                </p>
              </div>
              <input
                type="checkbox"
                id="forge-ui-enabled"
                checked={forgeUIOn}
                onChange={(e) => {
                  setForgeUIEnabled(e.target.checked);
                  setForgeUIOn(e.target.checked);
                }}
                className="mt-0.5 h-4 w-4 shrink-0"
              />
            </div>
          </div>
        </div>
      </Card>
    </div>
  );
}