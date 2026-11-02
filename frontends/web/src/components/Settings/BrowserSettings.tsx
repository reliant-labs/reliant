import { useState } from "react";
import { Globe, Check, ExternalLink } from "lucide-react";
import { toast } from "../../lib/toast-manager";
import {
  FALLBACK_BROWSER_PAGE,
  useBrowserPreferences,
  useUpdateBrowserPreferences,
} from "../../hooks/settings-queries";
import { cn } from "../../lib/utils";
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
 * settings: the checkbox when it flips, the homepage when the field loses
 * focus or Enter is pressed. A toast confirms each save.
 */
export function BrowserSettings() {
  const { data: browserPrefs, isPending: isLoading } = useBrowserPreferences();
  const { mutateAsync: updateBrowserPrefs } = useUpdateBrowserPreferences();
  // The homepage field's edit, until saved; null shows the stored page.
  const [pageDraft, setPageDraft] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const storedPage = browserPrefs.defaultPage || FALLBACK_BROWSER_PAGE;
  const defaultPage = pageDraft ?? storedPage;
  const openLinksInApp = browserPrefs.openLinksInApp;

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

  // The checkbox flips at once and back if the server refuses.
  const handleToggleOpenLinksInApp = async () => {
    const newValue = !openLinksInApp;
    try {
      await updateBrowserPrefs({ openLinksInApp: newValue });
      toast.success(newValue
        ? "Links will open in Reliant's browser"
        : "Links will open in your system browser"
      );
    } catch (error) {
      console.error("Failed to save link preference:", error);
      toast.error("Failed to save preference");
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
        description="Controls Reliant's built-in browser, which you and agents use to preview and test pages. Choose where links open and which page new browser tabs start on."
      />

      <div className="rounded-lg border border-border bg-card p-4 space-y-6">
      {/* Link Opening Preference */}
      <div className="space-y-4">
        <label className="flex items-start gap-3 cursor-pointer group hover:bg-foreground/5 p-3 rounded-lg transition-colors -mx-3">
          <div className="relative flex items-center justify-center mt-0.5">
            <input
              type="checkbox"
              checked={openLinksInApp}
              onChange={handleToggleOpenLinksInApp}
              className="peer sr-only"
            />
            <div
              className={cn(
                "w-5 h-5 rounded border-2 transition-all flex items-center justify-center peer-focus-visible:ring-2 peer-focus-visible:ring-ring",
                openLinksInApp
                  ? "border-primary bg-primary"
                  : "border-border bg-background"
              )}
            >
              {openLinksInApp && (
                <Check className="w-3.5 h-3.5 text-primary-foreground" strokeWidth={3} />
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