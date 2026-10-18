import { useEffect, useState } from "react";
import { Smartphone } from "lucide-react";

interface InstallPrompt extends Event {
  prompt(): Promise<{ outcome: "accepted" | "dismissed" }>;
}

export function MobileInstallMenuItem() {
  const [installed, setInstalled] = useState(() =>
    window.matchMedia?.("(display-mode: standalone)")?.matches ||
    window.matchMedia?.("(display-mode: fullscreen)")?.matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true,
  );
  const [prompt, setPrompt] = useState<InstallPrompt | null>(null);
  const [showHelp, setShowHelp] = useState(false);
  const isApple = /iPhone|iPad|iPod/.test(navigator.userAgent) ||
    (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);

  useEffect(() => {
    const mode = window.matchMedia?.("(display-mode: standalone)");
    const capture = (event: Event) => {
      event.preventDefault();
      setPrompt(event as InstallPrompt);
    };
    const complete = () => { setInstalled(true); setPrompt(null); };
    const changed = () => setInstalled(mode?.matches === true || (navigator as Navigator & { standalone?: boolean }).standalone === true);
    window.addEventListener("beforeinstallprompt", capture);
    window.addEventListener("appinstalled", complete);
    mode?.addEventListener("change", changed);
    return () => {
      window.removeEventListener("beforeinstallprompt", capture);
      window.removeEventListener("appinstalled", complete);
      mode?.removeEventListener("change", changed);
    };
  }, []);

  async function install() {
    if (!prompt) { setShowHelp(value => !value); return; }
    setPrompt(null); // Browser install events can only be used once.
    try {
      const result = await prompt.prompt();
      if (result.outcome === "accepted") setInstalled(true);
      else setShowHelp(true);
    } catch { setShowHelp(true); }
  }

  if (installed) return null;
  return (
    <div className="mt-3 border-t border-border pt-2">
      <button type="button" onClick={() => void install()} aria-expanded={showHelp}
        className="flex min-h-[48px] w-full items-center gap-3 rounded-lg px-3 text-left text-sm text-foreground/85 active:bg-foreground/5">
        <Smartphone className="h-4 w-4 shrink-0" />
        Add to Home Screen
      </button>
      {showHelp && <div className="rounded-lg bg-muted px-3 py-3 text-sm" role="region" aria-label="Add to Home Screen instructions">
        <p className="mb-3 text-muted-foreground">Open Reliant from your Home Screen for quick access and more room to chat.</p>
        <ol className="list-decimal space-y-2 pl-5">
          {isApple ? <>
            <li>Open Reliant in Safari and tap Share. You may need to open the browser menu first.</li>
            <li>Choose Add to Home Screen. If needed, tap More to find it.</li>
            <li>Keep Open as Web App enabled if shown, then tap Add.</li>
          </> : <>
            <li>Open your browser menu.</li>
            <li>Choose Install app or Add to Home screen, then confirm.</li>
          </>}
          <li>Launch Reliant from its new Home Screen icon.</li>
        </ol>
      </div>}
    </div>
  );
}
