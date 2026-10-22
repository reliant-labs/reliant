import { serverVersionLabel, useVersionInfo } from "../../hooks/useVersionInfo";

/**
 * The version block under the About screens' brand mark (desktop
 * `AboutSection` and `MobileAboutScreen`).
 *
 * The headline is THIS app's build. It used to be the server's Version RPC
 * alone, so an unstamped server — prod's, for a while — made the page say
 * "version unknown" about a bundle that knew exactly what it was. The server
 * and the forge release sit underneath, because a bug report needs all three.
 * Everything is selectable text so it can be pasted into one.
 */
export function VersionDetails() {
  const { app, server, release } = useVersionInfo();
  return (
    <div className="mt-1 select-text">
      <p className="text-sm text-muted-foreground" data-testid="about-app-version">
        {app ?? "version unknown"}
      </p>
      <dl className="mt-2 space-y-0.5 text-xs text-muted-foreground/80">
        <div className="flex justify-center gap-1.5">
          <dt>Server</dt>
          <dd className="font-mono" data-testid="about-server-version">
            {serverVersionLabel(server)}
          </dd>
        </div>
        {release && (
          <div className="flex justify-center gap-1.5">
            <dt>Release</dt>
            <dd className="font-mono" data-testid="about-release">
              {release}
            </dd>
          </div>
        )}
      </dl>
    </div>
  );
}
