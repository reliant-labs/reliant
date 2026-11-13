import { useCallback, useEffect } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { X } from "lucide-react";
import { SettingsViewerTab } from "./SettingsViewerTab";
import { useSettingsClose } from "@/hooks/useSettingsClose";
import {
  DEFAULT_SETTINGS_SECTION,
  isSettingsSection,
  type SettingsSection,
  type SettingsTab,
} from "../../routeSchemas";
import { readSettingsDestination, rememberSettingsDestination } from "./settingsDestination";

export function SettingsPage() {
  // Escape closes the page (matches the WorkflowBuilderPage convention). The
  // arrow-key section navigator inside SettingsViewerTab also listens with
  // capture, so we don't fight it: this handler only fires for Escape.
  // Explicit paths are authoritative. Bare /settings restores the last
  // destination onto the URL, where subtab/detail readers can see it too.
  // Unknown slugs arrive here via /settings?notFound=<slug>.
  const params = useParams({ strict: false }) as { section?: string; machineId?: string };
  const search = useSearch({ strict: false }) as { notFound?: string; tab?: SettingsTab };
  const navigate = useNavigate();
  const onClose = useSettingsClose();

  const section: SettingsSection = params.machineId
    ? "machines"
    : isSettingsSection(params.section)
      ? params.section
      : DEFAULT_SETTINGS_SECTION;
  const hasExplicitDestination = !!params.machineId || isSettingsSection(params.section);

  useEffect(() => {
    if (hasExplicitDestination) {
      rememberSettingsDestination({ section, machineId: params.machineId, tab: search.tab });
      return;
    }
    const destination = readSettingsDestination();
    // Preserve this visit's search (e.g. the unknown-page notice), never saved
    // callback markers. An explicitly supplied tab wins over the remembered one.
    const restoredSearch = { ...search, tab: search.tab ?? destination.tab };
    if (destination.machineId) {
      void navigate({
        to: "/settings/machines/$machineId",
        params: { machineId: destination.machineId },
        search: restoredSearch,
        replace: true,
      });
    } else {
      void navigate({
        to: "/settings/$section",
        params: { section: destination.section },
        search: restoredSearch,
        replace: true,
      });
    }
  }, [hasExplicitDestination, section, params.machineId, search, navigate]);

  const onSectionChange = useCallback(
    (next: SettingsSection) => {
      navigate({ to: "/settings/$section", params: { section: next } });
    },
    [navigate],
  );

  const dismissNotFound = useCallback(() => {
    navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, notFound: undefined }), replace: true });
  }, [navigate]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      const target = e.target as HTMLElement;
      if (
        target.closest("input, textarea, select, [contenteditable='true']") ||
        document.querySelector('[aria-modal="true"]')
      ) {
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      onClose();
    };
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [onClose]);

  // Don't mount Account (and its effects) while restoring another page.
  if (!hasExplicitDestination) return null;

  return (
    <div className="flex h-screen w-full flex-col bg-background">
      {search.notFound && (
        <div
          role="status"
          className="flex items-center justify-between gap-3 border-b border-border/60 bg-card px-6 py-2 text-sm text-foreground"
        >
          <span>
            There is no settings page called{" "}
            <code className="rounded bg-background px-1 font-mono text-xs">{search.notFound}</code>. Showing{" "}
            your settings instead.
          </span>
          <button
            type="button"
            onClick={dismissNotFound}
            aria-label="Dismiss notice"
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      )}
      <div className="flex-1 overflow-hidden">
        <SettingsViewerTab section={section} onSectionChange={onSectionChange} onClose={onClose} />
      </div>
    </div>
  );
}
