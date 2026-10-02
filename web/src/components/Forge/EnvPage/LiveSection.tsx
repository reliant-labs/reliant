// Copyright (c) 2025 Reliant Labs

/**
 * LIVE — what the control plane knows about one environment.
 *
 * ── THE RULE THIS COMPONENT EXISTS TO HOLD ──────────────────────────────────
 *
 * NOTHING ON THIS PATH CALLS THE DAEMON. Not a hook, not a fallback, not an
 * "enrichment when it happens to be online". The header, the provenance, the
 * releases timeline, the workloads and the secrets are all read straight from
 * the control plane with the user's session (services/forge/live.ts), and a
 * test renders this whole subtree with the daemon transport mocked to THROW
 * and asserts zero calls (__tests__/ForgeEnvPage.liveNoDaemon.test.tsx).
 *
 * The reason is a specific failure, not tidiness. The old page asked the
 * daemon for `forge.env_status`, and forge then called control-plane
 * ListEnvironments with the DAEMON's token — so the page 403'd for a user who
 * was entitled to see it, and an asleep laptop degraded a page describing a
 * production environment the control plane had been watching all along.
 * Anything that needs the user's checkout lives in Preview, which is the only
 * daemon-dependent surface.
 *
 * ── THE THREE STATES, AND NONE OF THEM IS AN ERROR ──────────────────────────
 *
 *   deployed            a promotion and a release. The provenance line says
 *                       where the images and the config each came from.
 *   declared, not built a row with a shape and no promotion — someone
 *                       registered it from Preview, or a build ran and nothing
 *                       was promoted. Secrets can be set.
 *   never built         no row at all. The env is absent from Live, and the
 *                       page says "Not built yet", pointing at Preview or
 *                       `forge env build`.
 *
 * All three render in the ordinary quiet register, with no error styling and
 * no banner. A never-built environment is the normal state of a new
 * environment, and painting it red taught users that the tool was broken.
 */

import Badge from "@/components/forge-ui/badge";
import {
  declaredNotBuilt,
  isPlacedKind,
  liveKindLabel,
  neverBuilt,
  type LiveEnv,
} from "@/services/forge/live";
import type { CloudEnvStatus, CloudPromotion } from "@/services/forge/cloudEnvs";

import { formatTimestamp } from "../Overview/EnvironmentTable";
import { LiveReleases } from "./LiveReleases";
import { LiveSecretsSection } from "./LiveSecretsSection";
import { LiveWorkloads } from "./LiveWorkloads";

export interface LiveSectionProps {
  /** The environment name from the route — known even when Live has no row for it. */
  envName: string;
  /** The control plane's row, or null when it holds none (never built). */
  env: LiveEnv | null;
  /** The forge project name: the (org, project, name) identity's project half. */
  forgeProject: string | null;
  /** Reliant's project id, for the managed-secret query keys. */
  projectId: string | null;

  /** GetStatus, for a PLACED env only. The platform observes nothing else. */
  status: CloudEnvStatus | undefined;
  statusLoading: boolean;
  statusError: Error | null;

  promotions: CloudPromotion[] | undefined;
  promotionsLoading: boolean;
  promotionsError: Error | null;

  selectedSecret: string | null;
  onSelectSecret: (name: string | null) => void;
  /** Open the Preview tab — the remedy offered for a never-built env. */
  onOpenPreview: () => void;
}

export function LiveSection(props: LiveSectionProps) {
  const { env, envName, onOpenPreview } = props;

  // ── Never built: no control-plane row. A NORMAL state. ──
  if (!env) {
    return (
      <div className="space-y-4" data-testid="live-never-built">
        <p className="text-sm text-muted-foreground">
          Not built yet.{" "}
          <button
            type="button"
            onClick={onOpenPreview}
            className="rounded-sm text-foreground underline hover:text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Open Preview
          </button>
          , or run <code className="font-mono text-foreground">forge env build {envName}</code>.
        </p>
      </div>
    );
  }

  const notBuilt = declaredNotBuilt(env);
  const blank = neverBuilt(env);

  return (
    <div className="space-y-8" data-testid="live-section" data-kind={env.kind}>
      <header className="space-y-3" data-testid="live-header">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="font-mono text-xl font-semibold text-foreground">{env.name}</h2>
          <Badge label={liveKindLabel(env.kind)} variant="neutral" size="sm" />
          {notBuilt && <Badge label="declared, not built yet" variant="neutral" size="sm" />}
        </div>

        <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
          <Fact label="Release">
            {env.release !== "" ? (
              <span className="font-mono text-foreground">{env.release}</span>
            ) : (
              <span>never promoted</span>
            )}
          </Fact>
          {env.promotedAt && (
            <Fact label="Promoted">
              <span className="text-foreground">{formatTimestamp(env.promotedAt)}</span>
            </Fact>
          )}
          {env.declaredAt && (
            <Fact label="Declared">
              <span className="text-foreground">{formatTimestamp(env.declaredAt)}</span>
            </Fact>
          )}
        </dl>

        {/* THE PROVENANCE LINE (§2.1): images from the release's source,
            config from the render's. Every field in it is a claim the render
            made about itself — nothing verifies ancestry in this cut — so the
            wording stays descriptive rather than approving. */}
        {env.provenance !== "" && (
          <p data-testid="live-provenance" className="font-mono text-xs text-muted-foreground">
            {env.provenance}
          </p>
        )}

        {notBuilt && (
          <p data-testid="live-declared-not-built" className="text-xs text-muted-foreground">
            Declared, not built yet. Its secrets can be set below; a build records the first
            release.
          </p>
        )}
        {blank && (
          <p data-testid="live-row-blank" className="text-xs text-muted-foreground">
            Not built yet.{" "}
            <button
              type="button"
              onClick={onOpenPreview}
              className="rounded-sm text-foreground underline hover:text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              Open Preview
            </button>
            , or run <code className="font-mono text-foreground">forge env build {env.name}</code>.
          </p>
        )}
      </header>

      <Section title="Workloads" testId="section-workloads">
        <LiveWorkloads
          env={env}
          status={props.status}
          isLoading={props.statusLoading}
          error={props.statusError}
        />
      </Section>

      <Section title="Secrets" testId="section-secrets">
        <LiveSecretsSection
          projectId={props.projectId}
          env={env}
          forgeProject={props.forgeProject}
          selectedSecret={props.selectedSecret}
          onSelectSecret={props.onSelectSecret}
        />
      </Section>

      <Section title="Releases" testId="section-releases">
        <LiveReleases
          env={env}
          promotions={props.promotions}
          isLoading={props.promotionsLoading}
          error={props.promotionsError}
        />
      </Section>

      {/* A self-managed env is promoted but NOT converged: forge applies it,
          and nothing on our side places or observes it. Said once, here,
          rather than leaving the Workloads section to imply something is
          watching a cluster it has never connected to.

          In the customer's nouns (#366): the fact that matters to them is
          that these are THEIR clusters and the readings come from their own
          deploys, not that our platform has no observer there. */}
      {!isPlacedKind(env.kind) && env.kind !== "local" && (
        <p data-testid="live-not-placed" className="text-xs text-muted-foreground">
          forge deploys this environment to your own cluster. Reliant records what each deploy sends
          there; it doesn&apos;t watch the cluster itself.
        </p>
      )}
    </div>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <dt className="text-2xs font-medium uppercase tracking-wide">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function Section({
  title,
  testId,
  children,
}: {
  title: string;
  testId: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3" data-testid={testId} aria-labelledby={`${testId}-heading`}>
      <div className="space-y-0.5 border-b border-border pb-2">
        <h3 id={`${testId}-heading`} className="text-sm font-semibold text-foreground">
          {title}
        </h3>
      </div>
      {children}
    </section>
  );
}
