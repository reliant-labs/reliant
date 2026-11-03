import { useId, useState } from "react";
import { ChevronDown, Smartphone } from "lucide-react";

/** Inline guidance keeps the drawer's existing focus and dismissal ownership. */
export function MobileInstallMenuItem() {
  const [expanded, setExpanded] = useState(false);
  const instructionsId = useId();

  return (
    <div>
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={instructionsId}
        onClick={() => setExpanded((value) => !value)}
        className="flex min-h-[48px] w-full items-center gap-3 rounded-lg px-3 text-left text-sm text-foreground/85 active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Smartphone aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="flex-1">Add to Home Screen</span>
        <ChevronDown aria-hidden="true" className="h-4 w-4 shrink-0" />
      </button>
      <div id={instructionsId} hidden={!expanded} className="space-y-4 px-3 py-3 text-sm text-foreground">
        <section aria-label="iPhone and iPad installation">
          <h3 className="font-semibold">iPhone or iPad · Safari</h3>
          <ol className="mt-2 list-decimal space-y-2 pl-5">
            <li>Open Reliant in Safari, not an in-app browser.</li>
            <li>Open Share (it may be inside the More menu), then choose Add to Home Screen. You may need to scroll the actions or use Edit Actions to find it.</li>
            <li>If offered, turn on Open as Web App, then tap Add.</li>
          </ol>
        </section>
        <section aria-label="Android installation">
          <h3 className="font-semibold">Android · Chrome</h3>
          <ol className="mt-2 list-decimal space-y-2 pl-5">
            <li>Open Reliant in Chrome, not an in-app browser.</li>
            <li>Open the three-dot menu and choose Add to Home screen or Install app.</li>
            <li>Follow the confirmation steps. Chrome may offer Install or Create shortcut; a shortcut may open in a browser tab.</li>
          </ol>
        </section>
        <p className="text-muted-foreground">Menu names and availability vary by browser and version. If the option is missing, try Safari on iPhone or iPad, or Chrome on Android. Reliant needs an internet connection; adding it does not enable offline use.</p>
      </div>
    </div>
  );
}
