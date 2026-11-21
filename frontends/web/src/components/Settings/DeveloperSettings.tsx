import { useState, useEffect } from "react";
import Button from "@/components/forge-ui/button";
import Card from "../forge-ui/card";
import { RefreshCw, AlertCircle, CheckCircle } from "lucide-react";
import { SettingsPageHeader } from "./SettingsPageHeader";

interface BackendStatus {
  isRunning: boolean;
  port: number;
}

export function DeveloperSettings() {
  const [backendStatus, setBackendStatus] = useState<BackendStatus | null>(null);
  const [isRestarting, setIsRestarting] = useState(false);

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
        description="Tools for testing Reliant itself, such as the backend status. You only need this page if you are developing or debugging Reliant."
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
    </div>
  );
}