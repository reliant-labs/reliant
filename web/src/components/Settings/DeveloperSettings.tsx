import { useState, useEffect } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Card } from "../../components/ui/Card";
import { FileText, FolderOpen, RefreshCw, AlertCircle, CheckCircle } from "lucide-react";

interface MockDriverConfig {
  enabled: boolean;
  mode: 'replay' | 'mock_static' | 'demo';
  replayFile?: string;
  scenario?: string;
}

interface BackendStatus {
  isRunning: boolean;
  port: number;
  mockDriverActive?: boolean;
  mockDriverFile?: string;
}

const PREDEFINED_REPLAY_FILES = [
  { value: 'none', label: 'None' },
  { value: 'test/replays/santa_tracker_full.json', label: 'Santa Tracker Full' },
];

export function DeveloperSettings() {
  const [config, setConfig] = useState<MockDriverConfig>({
    enabled: false,
    mode: 'replay',
    replayFile: '',
    scenario: '',
  });

  const [backendStatus, setBackendStatus] = useState<BackendStatus | null>(null);
  const [isRestarting, setIsRestarting] = useState(false);
  const [saveStatus, setSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');

  useEffect(() => {
    loadConfig();
    checkBackendStatus();
  }, []);

  const loadConfig = async () => {
    try {
      const savedConfig = await window.electronAPI.getMockDriverConfig();
      if (savedConfig) {
        setConfig(savedConfig);
      }
    } catch (error) {
      console.error('Failed to load mock driver config:', error);
    }
  };

  const checkBackendStatus = async () => {
    try {
      const status = await window.electronAPI.getBackendStatus();
      setBackendStatus(status as BackendStatus);
    } catch (error) {
      console.error('Failed to check backend status:', error);
    }
  };

  const saveConfig = async (newConfig: MockDriverConfig) => {
    setSaveStatus('saving');
    try {
      await window.electronAPI.setMockDriverConfig(newConfig);
      setConfig(newConfig);
      setSaveStatus('saved');
      setTimeout(() => setSaveStatus('idle'), 2000);
    } catch (error) {
      console.error('Failed to save mock driver config:', error);
      setSaveStatus('error');
      setTimeout(() => setSaveStatus('idle'), 3000);
    }
  };

  const handleToggle = () => {
    const newConfig = { ...config, enabled: !config.enabled };
    saveConfig(newConfig);
  };

  const handleModeChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newConfig = { ...config, mode: e.target.value as MockDriverConfig['mode'] };
    saveConfig(newConfig);
  };

  const handleReplayFileChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const value = e.target.value;
    const newConfig = {
      ...config,
      replayFile: value === 'none' ? '' : value
    };
    saveConfig(newConfig);
  };

  const handleBrowseFile = async () => {
    try {
      const filePath = await window.electronAPI.browseMockFile();
      if (filePath) {
        const newConfig = { ...config, replayFile: filePath };
        saveConfig(newConfig);
      }
    } catch (error) {
      console.error('Failed to browse for file:', error);
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
      <div>
        <h2 className="text-base font-semibold mb-2">Developer Settings</h2>
        <p className="text-sm text-muted-foreground">
          Configure mock drivers and development tools for testing
        </p>
      </div>

      {/* Backend Status */}
      <Card>
        <div className="p-4">
          <h3 className="text-sm font-semibold mb-3">Backend Status</h3>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-sm">Status</span>
              <div className="flex items-center gap-2">
                {backendStatus?.isRunning ? (
                  <>
                    <CheckCircle className="w-4 h-4 text-success" />
                    <span className="text-sm text-success">Running on port {backendStatus.port}</span>
                  </>
                ) : (
                  <>
                    <AlertCircle className="w-4 h-4 text-destructive" />
                    <span className="text-sm text-destructive">Not running</span>
                  </>
                )}
              </div>
            </div>
            {backendStatus?.mockDriverActive && (
              <div className="text-xs text-muted-foreground">
                Mock driver active: {backendStatus.mockDriverFile}
              </div>
            )}
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

      {/* Mock Driver Configuration */}
      <Card>
        <div className="p-4">
          <h3 className="text-sm font-semibold mb-2">Mock Driver Configuration</h3>
          <p className="text-xs text-muted-foreground mb-4">
            Configure mock drivers for testing without real API calls
          </p>

          <div className="space-y-4">
            {/* Enable/Disable Toggle */}
            <div className="flex items-center justify-between">
              <label htmlFor="mock-enabled" className="text-sm">Enable Mock Driver</label>
              <input
                type="checkbox"
                id="mock-enabled"
                checked={config.enabled}
                onChange={handleToggle}
                className="w-4 h-4"
              />
            </div>

            {config.enabled && (
              <>
                {/* Mode Selection */}
                <div className="space-y-2">
                  <label htmlFor="mock-mode" className="text-sm block">Mock Mode</label>
                  <select
                    id="mock-mode"
                    value={config.mode}
                    onChange={handleModeChange}
                    className="w-full px-3 py-2 border border-border/40 rounded-md bg-background text-sm"
                  >
                    <option value="replay">Replay (JSON file)</option>
                    <option value="mock_static">Mock Static (Scenario)</option>
                    <option value="demo">Demo (Simple exchanges)</option>
                  </select>
                </div>

                {/* Replay File Selection */}
                {config.mode === 'replay' && (
                  <div className="space-y-2">
                    <label htmlFor="replay-file" className="text-sm block">Replay File</label>
                    <div className="flex gap-2">
                      <select
                        value={config.replayFile || 'none'}
                        onChange={handleReplayFileChange}
                        className="flex-1 px-3 py-2 border border-border/40 rounded-md bg-background text-sm"
                      >
                        {PREDEFINED_REPLAY_FILES.map(file => (
                          <option key={file.value} value={file.value}>
                            {file.label}
                          </option>
                        ))}
                      </select>
                      <Button
                        onClick={handleBrowseFile}
                        variant="outline"
                        size="sm"
                      >
                        <FolderOpen className="w-4 h-4" />
                      </Button>
                    </div>
                    {config.replayFile && config.replayFile !== 'none' && (
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        <FileText className="w-3 h-3" />
                        {config.replayFile}
                      </div>
                    )}
                  </div>
                )}

                {/* Scenario Input for Mock Static */}
                {config.mode === 'mock_static' && (
                  <div className="space-y-2">
                    <label htmlFor="scenario" className="text-sm block">Scenario Name</label>
                    <Input
                      id="scenario"
                      value={config.scenario || ''}
                      onChange={(e) => {
                        const newConfig = { ...config, scenario: e.target.value };
                        saveConfig(newConfig);
                      }}
                      placeholder="Enter scenario name"
                    />
                    <p className="text-xs text-muted-foreground">
                      Define custom scenarios for the mock static driver
                    </p>
                  </div>
                )}
              </>
            )}

            {/* Save Status */}
            {saveStatus !== 'idle' && (
              <div className={`p-3 rounded-lg border ${
                saveStatus === 'saved' ? 'border-success bg-success/5' :
                saveStatus === 'error' ? 'border-destructive bg-destructive/5' :
                'border-border bg-muted'
              }`}>
                <div className="flex items-center gap-2">
                  <AlertCircle className="h-4 w-4" />
                  <span className="text-sm">
                    {saveStatus === 'saving' && 'Saving configuration...'}
                    {saveStatus === 'saved' && 'Configuration saved. Restart backend to apply changes.'}
                    {saveStatus === 'error' && 'Failed to save configuration'}
                  </span>
                </div>
              </div>
            )}
          </div>
        </div>
      </Card>

      {/* Additional Information */}
      <div className="p-3 rounded-lg border border-border/40 bg-muted/30 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <div className="flex items-start gap-2">
          <AlertCircle className="h-4 w-4 mt-0.5" />
          <div className="text-sm">
            <strong>Note:</strong> Changes to mock driver settings require a backend restart to take effect.
            The mock driver intercepts API calls and returns predefined responses for testing purposes.
          </div>
        </div>
      </div>
    </div>
  );
}