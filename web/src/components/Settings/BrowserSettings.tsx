import { useState, useEffect } from "react";
import { Globe, Save, Check, ExternalLink } from "lucide-react";
import { Button } from "../ui/Button";
import { toast } from "../../lib/toast-manager";
import { api } from "../../api/client";
import { cn } from "../../lib/utils";

export function BrowserSettings() {
  const [defaultPage, setDefaultPage] = useState("https://www.google.com");
  const [openLinksInApp, setOpenLinksInApp] = useState(true);
  const [isLoading, setIsLoading] = useState(false);
  const [isSaving, setIsSaving] = useState(false);

  // Load settings on mount
  useEffect(() => {
    loadSettings();
  }, []);

  const loadSettings = async () => {
    setIsLoading(true);
    try {
      const preferences = await api.settings.getPreferences();
      // Browser default page stored in additional settings
      if (preferences.additional?.browserDefaultPage) {
        setDefaultPage(preferences.additional.browserDefaultPage);
      }
      // Open links in app preference (default true)
      if (preferences.additional?.browserOpenLinksInApp !== undefined) {
        setOpenLinksInApp(preferences.additional.browserOpenLinksInApp === "true");
      }
    } catch (error) {
      console.error("Failed to load browser settings:", error);
      toast.error("Failed to load browser settings");
    } finally {
      setIsLoading(false);
    }
  };

  const handleSave = async () => {
    setIsSaving(true);
    try {
      // Normalize the URL - add https:// if no protocol
      let urlToSave = defaultPage.trim();
      if (!urlToSave.startsWith('http://') && !urlToSave.startsWith('https://') && !urlToSave.startsWith('about:')) {
        urlToSave = 'https://' + urlToSave;
      }
      
      await api.settings.updatePreferences({ browserDefaultPage: urlToSave });
      
      // Update the local state to show the normalized URL
      setDefaultPage(urlToSave);
      
      toast.success("Browser settings saved");
    } catch (error) {
      console.error("Failed to save browser settings:", error);
      toast.error("Failed to save browser settings");
    } finally {
      setIsSaving(false);
    }
  };

  const handleToggleOpenLinksInApp = async () => {
    const newValue = !openLinksInApp;
    setOpenLinksInApp(newValue);
    try {
      await api.settings.updatePreferences({ 
        browserOpenLinksInApp: newValue.toString() 
      });
      toast.success(newValue 
        ? "Links will open in Reliant's browser" 
        : "Links will open in your system browser"
      );
    } catch (error) {
      console.error("Failed to save link preference:", error);
      toast.error("Failed to save preference");
      // Revert on error
      setOpenLinksInApp(!newValue);
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <div className="text-muted-foreground">Loading browser settings...</div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-base font-semibold mb-2">Web Browser</h2>
        <p className="text-sm text-muted-foreground">
          Configure your web browser settings and default homepage.
        </p>
      </div>

      {/* Link Opening Preference */}
      <div className="space-y-4">
        <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-3 rounded-lg transition-colors -mx-3">
          <div className="relative flex items-center justify-center mt-0.5">
            <input
              type="checkbox"
              checked={openLinksInApp}
              onChange={handleToggleOpenLinksInApp}
              className="sr-only"
            />
            <div
              className={cn(
                "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                openLinksInApp
                  ? "border-primary bg-primary"
                  : "border-border bg-background"
              )}
            >
              {openLinksInApp && (
                <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />
              )}
            </div>
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <ExternalLink className="w-4 h-4 text-muted-foreground" />
              <span className="text-sm font-medium text-foreground">
                Open links in Reliant
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              When enabled, clicked links open in Reliant's built-in browser. When disabled, links open in your system's default browser.
            </p>
          </div>
        </label>
      </div>

      {/* Default Page Setting */}
      <div className="space-y-4">
        <div>
          <label className="text-sm font-medium mb-2 block">
            Default Homepage
          </label>
          <p className="text-xs text-muted-foreground mb-3">
            The page that opens when you create a new browser tab. You can enter a domain (e.g., "github.com") or a full URL.
          </p>
          <div className="flex items-center gap-3">
            <div className="flex-1 relative">
              <Globe className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
              <input
                type="url"
                value={defaultPage}
                onChange={(e) => setDefaultPage(e.target.value)}
                placeholder="https://www.google.com"
                className="w-full pl-10 pr-3 py-2 border border-border/40 rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring text-sm"
              />
            </div>
            <Button
              onClick={handleSave}
              disabled={isSaving}
              variant="primary"
              size="sm"
              leftIcon={<Save className="w-4 h-4" />}
            >
              {isSaving ? "Saving..." : "Save"}
            </Button>
          </div>
        </div>
      </div>

      {/* UI Context Capture Feature Info */}
      <div className="mt-8 p-4 elevation-1 rounded-lg border border-border/40">
        <h4 className="text-sm font-semibold mb-2 flex items-center gap-2">
          <Globe className="w-4 h-4" />
          UI Context Capture
        </h4>
        <p className="text-sm text-muted-foreground mb-3">
          Click the "Ask about this page" button in the browser toolbar to capture the current webpage's content and send it directly to your chat.
        </p>
        <p className="text-xs text-muted-foreground">
          This feature allows you to select any UI element and get AI assistance with understanding or modifying it.
        </p>
      </div>
    </div>
  );
}