import { useState } from "react";
import { Globe } from "lucide-react";
import { toast } from "../../lib/toast-manager";
import {
  FALLBACK_BROWSER_PAGE,
  useBrowserPreferences,
  useUpdateBrowserPreferences,
} from "../../hooks/settings-queries";
import { SettingsPageHeader } from "./SettingsPageHeader";

/** The homepage as stored: a bare domain gets https://. */
function normalizeHomepage(value: string): string {
  const url = value.trim();
  if (!url) return "";
  if (url.startsWith("http://") || url.startsWith("https://") || url.startsWith("about:")) return url;
  return "https://" + url;
}

/**
 * Settings → Built-in browser. Every control saves itself, like the rest of
 * settings: the homepage when the field loses focus or Enter is pressed. A
 * toast confirms each save.
 */
export function BrowserSettings() {
  const { data: browserPrefs, isPending: isLoading } = useBrowserPreferences();
  const { mutateAsync: updateBrowserPrefs } = useUpdateBrowserPreferences();
  // The homepage field's edit, until saved; null shows the stored page.
  const [pageDraft, setPageDraft] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const storedPage = browserPrefs.defaultPage || FALLBACK_BROWSER_PAGE;
  const defaultPage = pageDraft ?? storedPage;

  const saveHomepage = async () => {
    if (pageDraft === null || isSaving) return;
    const urlToSave = normalizeHomepage(pageDraft);
    // An emptied field, or no change, keeps the stored page.
    if (!urlToSave || urlToSave === storedPage) {
      setPageDraft(null);
      return;
    }
    setIsSaving(true);
    try {
      await updateBrowserPrefs({ defaultPage: urlToSave });
      // The field now shows the stored (normalized) URL
      setPageDraft(null);
      toast.success("Homepage saved");
    } catch (error) {
      console.error("Failed to save browser homepage:", error);
      toast.error("Couldn't save the homepage");
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
        title="Built-in browser"
        description="Controls Reliant's built-in browser, which you and agents use to preview and test pages. Links to your dev servers (localhost, and your cloud machine's previews) open here; other links open in your default browser."
      />

      <div className="rounded-lg border border-border bg-card p-4 space-y-6">
      {/* Default Page Setting */}
      <div className="space-y-4">
        <div>
          <label htmlFor="browser-homepage" className="text-sm font-medium mb-2 block">
            Default homepage
          </label>
          <p id="browser-homepage-hint" className="text-xs text-muted-foreground mb-3">
            The page that opens when you create a new browser tab. You can enter a domain (e.g., "github.com") or a full URL. It saves when you leave the field or press Enter.
          </p>
          <div className="flex items-center gap-3">
            <div className="flex-1 relative">
              <Globe className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" aria-hidden="true" />
              <input
                id="browser-homepage"
                aria-describedby="browser-homepage-hint"
                type="url"
                value={defaultPage}
                onChange={(e) => setPageDraft(e.target.value)}
                onBlur={() => void saveHomepage()}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    void saveHomepage();
                  } else if (e.key === "Escape" && pageDraft !== null) {
                    e.preventDefault();
                    setPageDraft(null);
                  }
                }}
                aria-busy={isSaving}
                placeholder="https://www.google.com"
                className="w-full pl-10 pr-3 py-2 border border-border/40 rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring text-sm"
              />
            </div>
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