import { useState } from "react";
import { Globe, Save } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { toast } from "../../lib/toast-manager";
import {
  FALLBACK_BROWSER_PAGE,
  useBrowserPreferences,
  useUpdateBrowserPreferences,
} from "../../hooks/settings-queries";
import { SettingsPageHeader } from "./SettingsPageHeader";

export function BrowserSettings() {
  const { data: browserPrefs, isPending: isLoading } = useBrowserPreferences();
  const { mutateAsync: updateBrowserPrefs } = useUpdateBrowserPreferences();
  // The homepage field's edit, until saved; null shows the stored page.
  const [pageDraft, setPageDraft] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const defaultPage = pageDraft ?? (browserPrefs.defaultPage || FALLBACK_BROWSER_PAGE);

  const handleSave = async () => {
    setIsSaving(true);
    try {
      // Normalize the URL - add https:// if no protocol
      let urlToSave = defaultPage.trim();
      if (!urlToSave.startsWith('http://') && !urlToSave.startsWith('https://') && !urlToSave.startsWith('about:')) {
        urlToSave = 'https://' + urlToSave;
      }

      await updateBrowserPrefs({ defaultPage: urlToSave });

      // The field now shows the stored (normalized) URL
      setPageDraft(null);

      toast.success("Browser settings saved");
    } catch (error) {
      console.error("Failed to save browser settings:", error);
      toast.error("Failed to save browser settings");
    } finally {
      setIsSaving(false);
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
      <SettingsPageHeader
        title="Web browser"
        description="Controls Reliant's built-in browser, which you and agents use to preview and test pages. Links to your dev servers (localhost, and your cloud machine's previews) open here; other links open in your default browser."
      />

      <div className="rounded-lg border border-border bg-card p-4 space-y-6">
      {/* Default Page Setting */}
      <div className="space-y-4">
        <div>
          <label className="text-sm font-medium mb-2 block">
            Default homepage
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
                onChange={(e) => setPageDraft(e.target.value)}
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
      </div>

      {/* UI Context Capture Feature Info */}
      <div className="p-4 rounded-lg border border-border bg-card">
        <h4 className="text-sm font-semibold mb-2 flex items-center gap-2">
          <Globe className="w-4 h-4" />
          UI context capture
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