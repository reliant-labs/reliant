import { useCallback, useEffect } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { X } from "lucide-react";
import { SettingsHeader } from "./SettingsHeader";
import { SettingsViewerTab } from "./SettingsViewerTab";
import { useSettingsClose } from "@/hooks/useSettingsClose";
import {
  DEFAULT_SETTINGS_SECTION,
  isSettingsSection,
  type SettingsSection,
} from "../../routeSchemas";

export function SettingsPage() {
  // Escape closes the page (matches the WorkflowBuilderPage convention). The
  // arrow-key section navigator inside SettingsViewerTab also listens with
  // capture, so we don't fight it: this handler only fires for Escape.
  // `section` is the URL — bare /settings yields no param and falls back to
  // DEFAULT_SETTINGS_SECTION. /settings/$section provides it, and
  // /settings/environments/$machineId is the Machines section with one machine
  // open. An unknown slug never gets here: the route redirects it to bare
  // /settings with `notFound=<slug>` (settingsSectionRoutes.tsx).
  const params = useParams({ strict: false }) as { section?: string; machineId?: string };
  const search = useSearch({ strict: false }) as { notFound?: string };
  const navigate = useNavigate();
  const onClose = useSettingsClose();

  const section: SettingsSection = params.machineId
    ? "environments"
    : isSettingsSection(params.section)
      ? params.section
      : DEFAULT_SETTINGS_SECTION;

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
      if (e.key !== "Escape") return;
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true"
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

  return (
    <div className="flex h-screen w-full flex-col bg-background">
      <SettingsHeader onClose={onClose} />
      {search.notFound && (
        <div
          role="status"
          className="flex items-center justify-between gap-3 border-b border-border/60 bg-card px-6 py-2 text-sm text-foreground"
        >
          <span>
            There is no settings page called{" "}
            <code className="rounded bg-background px-1 font-mono text-xs">{search.notFound}</code>. Showing{" "}
            your account settings instead.
          </span>
          <button
            type="button"
            onClick={dismissNotFound}
            aria-label="Dismiss notice"
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      )}
      <div className="flex-1 overflow-hidden">
        <SettingsViewerTab section={section} onSectionChange={onSectionChange} />
      </div>
    </div>
  );
}
