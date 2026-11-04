/**
 * Proving harness for the forge → reliant token bridge (the @theme block in
 * src/index.css). NOT a product surface — this exists so the bridge can be
 * verified against REAL forge components rather than against a synthetic
 * swatch grid, in every color scheme and both light and dark.
 *
 * The components below are installed verbatim by `forge component install`
 * and are never edited; that is the whole point of mapping at the theme
 * layer. If a token is unmapped it shows up here as a transparent fill or
 * inherited text, which is what a missing mapping looks like in the UI.
 *
 * The library is wrapped in `.forge-ui`, which is what gives forge's `accent`
 * its own meaning (reliant's primary action color) without disturbing the
 * ~159 existing `bg-accent` call sites elsewhere in the app.
 *
 * e2e/color-schemes.spec.ts screenshots its regions in every
 * schemes, light and dark:
 *
 *   forge-library  every forge-ui component the app used before R5 S1 (the
 *                  two S1 brought in, alert_banner and progress_bar, are
 *                  exercised in s1-sites, so this region's baselines did not
 *                  move when S1 landed — which is what showed it changed
 *                  nothing it did not mean to)
 *   s1-sites       the call sites the R5 S1 wave moved off components/ui
 *                  onto forge (card, badge/tag, alert_banner, progress_bar),
 *                  rendered with the props those sites pass — real components
 *                  where they are pure, the site's own markup where not
 *   buttons        forge's Button, every variant at every size and in every
 *                  state the app uses (icons, icon-only, loading, disabled,
 *                  expanded), on a card and on the page background. Outside
 *                  .forge-ui, as almost every Button in the app is: the
 *                  button scopes the accent family itself (index.css)
 *   ?view=overlays the components that cover the viewport (sidebar_layout,
 *                  modal, confirmation_dialog), each pinned inside its own
 *                  box so all three fit one screenshot
 *   s2-sites,      the R5 S2 primitives (Toggle, Input/Textarea, Dropdown,
 *   ?view=s2-menus Modal) as their call sites render them — S2Sandbox.tsx
 *   ?view=s2-modal
 *   ?view=r5-tooltips, the R5 overlays (Tooltip, ContextMenu, HelpPopover,
 *   ?view=r5-menus,    the searchable pickers) as their call sites render
 *   ?view=r5-pickers   them — R5Sandbox.tsx
 *
 * A change to anything here moves those baselines, so it shows up in review
 * as a picture rather than as a diff nobody can read.
 */
import { useState } from "react";
import { useSearch } from "@tanstack/react-router";
import { ChevronRight, Database, Layers, Plus, Settings as SettingsIcon, X } from "lucide-react";

import { ConfigScope } from "../../hooks/mcp-queries";
import { CheckTable } from "../Forge/Status/CheckTable";
import { DispositionLegend } from "../Forge/Status/DispositionLegend";
import { MCPServerListItem } from "../Settings/MCPServerListItem";
import { ErrorSummary } from "../Automations/ErrorSummary";
import { COLOR_SCHEMES } from "../../lib/forgeTokens";
import { checksOf, type ForgeEnvStatusReport } from "../../services/forge/status";

import AlertBanner from "./alert_banner";
import Badge from "./badge";
import Button, { type ButtonSize, type ButtonVariant } from "./button";
import Card, { CardBody, CardFooter, CardHeader, CardInset } from "./card";
import ConfirmationDialog from "./confirmation_dialog";
import DataTable from "./data_table";
import EmptyState from "./empty_state";
import KeyValueList from "./key_value_list";
import Link from "./link";
import Modal from "./modal";
import Mono from "./mono";
import PageHeader from "./page_header";
import ProgressBar from "./progress_bar";
import SidebarLayout from "./sidebar_layout";
import SkeletonLoader from "./skeleton_loader";
import StatGrid from "./stat_grid";
import Status from "./status";
import Tabs from "./tabs";
import Tag from "./tag";
import { S2Menus, S2Modal, S2Sites } from "./S2Sandbox";
import { R5Menus, R5Pickers, R5Tooltips } from "./R5Sandbox";

const SCHEMES = COLOR_SCHEMES;

type Row = Record<string, unknown> & {
  service: string;
  env: string;
  status: string;
  replicas: string;
};

const ROWS: Row[] = [
  { service: "admin-server", env: "prod", status: "Running", replicas: "3/3" },
  { service: "daemon-gateway", env: "prod", status: "Degraded", replicas: "1/3" },
  { service: "reliant-api-server", env: "staging", status: "Running", replicas: "2/2" },
  { service: "temporal-worker", env: "staging", status: "Failed", replicas: "0/2" },
];

/** One check per disposition, so the legend and the rows show all five. */
const REPORT: ForgeEnvStatusReport = {
  env: "dev",
  checks: [
    { name: "api reachable", status: "pass", duration_ms: 42 },
    { name: "metrics scraped", status: "warn", message: "2 of 3 targets up", duration_ms: 130 },
    { name: "migrations applied", status: "fail", message: "1 pending", duration_ms: 18 },
    { name: "traces exported", status: "skip", duration_ms: 0 },
    { name: "profiles", status: "unknown", evidence: "collector did not answer", duration_ms: 5000 },
  ],
};

const noop = () => undefined;

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <h2 className="mb-3 text-sm font-medium text-foreground">{children}</h2>;
}

/** Every forge-ui component the app imports, in the states it uses. */
function ForgeLibrary() {
  return (
    <div className="space-y-8" data-testid="forge-library">
      <PageHeader
        breadcrumbs={[{ label: "Environments", href: "/forge-token-sandbox" }, { label: "prod" }]}
        title="prod"
        titleAccessory={<Status tone="success" label="Healthy" />}
        meta={
          <>
            <span>us-central1</span>
            <Mono value="sha256:9f2c4e1ab37d55e0c81f2b6a4d9e3c7f00112233" />
          </>
        }
        actions={[
          { label: "Promote", variant: "primary", onClick: noop },
          { label: "Roll back", variant: "secondary", onClick: noop },
          { label: "Destroy", variant: "danger", onClick: noop },
        ]}
        className="mb-0"
      />

      <StatGrid
        columns={4}
        stats={[
          { value: "14", label: "Workloads", trend: { direction: "up", value: "+2" } },
          { value: "3", label: "Environments", trend: { direction: "flat", value: "0" } },
          { value: "1", label: "Degraded", trend: { direction: "down", value: "-1" } },
          { value: "98.2%", label: "Availability", trend: { direction: "up", value: "+0.3%" } },
        ]}
      />

      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <Badge variant="success" label="Running" />
          <Badge variant="warning" label="Pending" />
          <Badge variant="danger" label="Failed" />
          <Badge variant="info" label="Queued" />
          <Badge variant="neutral" label="Unknown" />
          <Badge variant="success" label="Dot" dot size="sm" />
          <Badge variant="info" label="Large" size="lg" />
          <Badge variant="neutral" label="removable" removable onRemove={noop} />
        </div>
        <div className="flex flex-wrap items-center gap-4">
          <Status tone="success" label="healthy" />
          <Status tone="warning" label="degraded" />
          <Status tone="danger" label="down" />
          <Status tone="neutral" label="deploying" />
          <Tag>prod</Tag>
          <Tag tone="warning">drifted</Tag>
          <Tag tone="danger">rolled back</Tag>
          <Tag icon={<Database />}>workspace</Tag>
          <Link href="/forge-token-sandbox">An internal link</Link>
          <Mono value="4be2d0a1c9f8e7d6" />
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <Card>
          <CardHeader
            title="Panel heading"
            description="A description under the heading."
            actions={<Tag>job</Tag>}
          />
          <CardBody>
            Body text on the primary surface.
            <CardInset className="mt-3 font-mono text-xs">forge env deploy prod</CardInset>
          </CardBody>
          <CardFooter>
            <Status tone="neutral" label="Saved" />
          </CardFooter>
        </Card>
        <Card interactive padding="lg">
          <KeyValueList
            groups={[
              {
                title: "Identity",
                fields: [
                  { label: "Project", value: "my-project" },
                  { label: "Region", value: "us-central1" },
                  { label: "Commit", value: "4be2d0a1", mono: true },
                ],
              },
            ]}
          />
        </Card>
      </div>

      <div className="grid grid-cols-3 gap-4">
        <Tabs
          ariaLabel="Underline"
          tabs={[
            { id: "a", label: "Overview" },
            { id: "b", label: "Logs", badge: 3 },
            { id: "c", label: "Disabled", disabled: true },
          ]}
          defaultTab="a"
        />
        <Tabs
          ariaLabel="Pills"
          variant="pills"
          tabs={[
            { id: "a", label: "Day" },
            { id: "b", label: "Week" },
          ]}
          defaultTab="b"
        />
        <Tabs
          ariaLabel="Boxed"
          variant="boxed"
          tabs={[
            { id: "a", label: "Table" },
            { id: "b", label: "Chart" },
          ]}
          defaultTab="a"
        />
      </div>

      <DataTable<Row>
        columns={[
          { key: "service", header: "Service", sortable: true },
          { key: "env", header: "Environment" },
          { key: "status", header: "Status" },
          { key: "replicas", header: "Replicas" },
        ]}
        data={ROWS}
        selectable
        page={1}
        pageSize={10}
        totalItems={ROWS.length}
      />

      <DataTable<Row>
        columns={[
          { key: "service", header: "Service" },
          { key: "env", header: "Environment" },
        ]}
        data={[]}
        loading
      />

      <div className="grid grid-cols-2 gap-4">
        <EmptyState
          title="No environments yet"
          description="Deploy a service to see it here."
          actionLabel="Deploy"
          onAction={noop}
        />
        <div className="space-y-3">
          <SkeletonLoader variant="text" count={2} />
          <SkeletonLoader variant="card" />
        </div>
      </div>
    </div>
  );
}

const BUTTON_VARIANTS: ButtonVariant[] = ["primary", "secondary", "outline", "ghost", "danger"];
const BUTTON_SIZES: ButtonSize[] = ["xs", "sm", "md", "lg", "xl"];

/** forge's Button in every variant, size and state, on both page surfaces. */
function ButtonSites() {
  return (
    <div className="space-y-6" data-testid="buttons">
      {(["bg-card", "bg-background"] as const).map((surface) => (
        <div key={surface} className={`space-y-3 rounded-lg border border-border p-4 ${surface}`}>
          <SectionTitle>Button on {surface}</SectionTitle>
          {BUTTON_VARIANTS.map((variant) => (
            <div key={variant} className="flex flex-wrap items-center gap-2">
              {BUTTON_SIZES.map((size) => (
                <Button key={size} variant={variant} size={size}>
                  {variant} {size}
                </Button>
              ))}
              <Button variant={variant} size="sm" leftIcon={<Plus />}>
                New
              </Button>
              <Button variant={variant} size="sm" rightIcon={<ChevronRight />}>
                Next
              </Button>
              <Button variant={variant} size="sm" icon={<X />} aria-label="Close" />
              <Button variant={variant} size="sm" loading>
                Save
              </Button>
              <Button variant={variant} size="sm" loading leftIcon={<Plus />}>
                Creating
              </Button>
              <Button variant={variant} size="sm" disabled>
                Disabled
              </Button>
              <Button variant={variant} size="sm" aria-expanded>
                Open
              </Button>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

/**
 * The call sites the R5 S1 wave migrated, with the props they pass.
 * Pure components render for real; the rest reproduce their site's markup.
 */
function S1Sites() {
  return (
    <div className="space-y-8" data-testid="s1-sites">
      <div>
        <SectionTitle>Badge — MCPServerListItem, ConnectorConsent</SectionTitle>
        <div className="max-w-xl space-y-2">
          <MCPServerListItem
            name="github"
            displayName="GitHub"
            connected
            scope={ConfigScope.GLOBAL}
            toolCount={12}
            resourcesEnabled
            promptsEnabled
            onConfigure={noop}
            onRestart={noop}
            onRemove={noop}
          />
          <MCPServerListItem
            name="linear"
            displayName="Linear"
            connected={false}
            scope={ConfigScope.PROJECT_LOCAL}
            toolCount={0}
            error="connection refused"
            onConfigure={noop}
            onRestart={noop}
            onRemove={noop}
          />
          <MCPServerListItem
            name="sentry"
            connected
            enabled={false}
            toolCount={4}
            onConfigure={noop}
            onRestart={noop}
            onRemove={noop}
          />
        </div>
        <div className="mt-3 flex items-center gap-1.5 text-sm">
          <span className="font-medium">filesystem</span>
          <Badge variant="warning" label="Shell" />
          <span className="ml-4 font-mono">write_file</span>
          <Badge variant="warning" label="writes" />
        </div>
      </div>

      <div>
        <SectionTitle>Badge — forge status legend and check rows</SectionTitle>
        <DispositionLegend report={REPORT} />
        <Card padding="none" className="mt-3 overflow-hidden">
          <CheckTable checks={checksOf(REPORT)} />
        </Card>
      </div>

      <div>
        <SectionTitle>Card / alert_banner — DeveloperSettings, MCPSetupWizard, DevStackPanel notices</SectionTitle>
        <div className="grid grid-cols-2 gap-4">
          <Card padding="none">
            <div className="p-4">
              <h3 className="mb-3 text-sm font-semibold">Backend Status</h3>
              <p className="text-xs text-muted-foreground">Connected to localhost:8080</p>
            </div>
          </Card>
          <Card padding="sm" className="space-y-2">
            <p className="text-sm text-muted-foreground">Read and write issues in your GitHub repositories.</p>
            <p className="whitespace-pre-line text-xs text-muted-foreground">Create a token with the repo scope.</p>
          </Card>
          <Card padding="lg" className="border-dashed text-center text-sm text-ink-muted">
            Running runtime checks for <span className="font-mono">dev</span>…
          </Card>
          <AlertBanner
            variant="error"
            message={<>Could not reach your daemon to run forge&apos;s runtime checks: connection refused</>}
          />
        </div>
      </div>

      <div>
        <SectionTitle>ErrorSummary (on alert_banner) — the automation dialog</SectionTitle>
        <div className="max-w-xl">
          <ErrorSummary
            title="Fix these to continue"
            autoFocus={false}
            items={[
              { fieldId: "s1-repos", message: "Choose at least one repository." },
              { message: "The server is unreachable." },
            ]}
          />
        </div>
      </div>

      <div>
        <SectionTitle>Progress — OnboardingChecklist, MachineDiskCard, FreeDiskSpaceDialog</SectionTitle>
        <div className="grid max-w-xl gap-4">
          <div>
            <ProgressBar value={60} variant="default" size="sm" ariaLabel="Setup progress" className="forge-ui mb-1.5" />
            <p className="text-xs text-muted-foreground">3 of 5 completed</p>
          </div>
          <ProgressBar value={100} variant="default" size="sm" ariaLabel="Setup progress" className="forge-ui" />
          <div className="flex flex-col gap-1.5">
            <span className="text-sm text-foreground">Workspace disk · 42 GB of 100 GB used</span>
            <ProgressBar value={42} size="sm" variant="default" ariaLabel="Workspace disk usage" className="forge-ui" />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-sm text-foreground">Docker disk · 93 GB of 100 GB used</span>
            <ProgressBar value={93} size="sm" variant="warning" ariaLabel="Docker disk usage" className="forge-ui" />
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * The full-viewport components. A transformed ancestor is the containing
 * block for `position: fixed` descendants, so each one is pinned inside its
 * own box (the inline transform is that geometry, not styling) and all three
 * share one screenshot.
 */
function Overlays() {
  const box = "relative h-[420px] overflow-hidden rounded-lg border border-border";
  const pin = { transform: "translateZ(0)" };
  return (
    <div className="grid grid-cols-3 gap-4" data-testid="overlays">
      <div className={box} style={pin}>
        <SidebarLayout
          brand={<span className="text-sm font-semibold text-ink">reliant</span>}
          navLabel="Console"
          navItems={[
            { label: "Overview", href: "/forge-token-sandbox", icon: <Layers />, active: true },
            { label: "Databases", href: "/forge-token-sandbox", icon: <Database />, badge: { type: "count", value: 3 } },
            { label: "Settings", href: "/forge-token-sandbox", icon: <SettingsIcon />, section: "Admin", badge: { type: "dot", tone: "warning", label: "Needs attention" } },
          ]}
          user={{ name: "Ada Lovelace", email: "ada@example.com" }}
        >
          <p className="text-sm text-ink">Page content</p>
        </SidebarLayout>
      </div>
      <div className={box} style={pin}>
        <Modal open portal={false} onClose={noop} title="Rename environment" description="Names are unique per project." footer={<Tag>footer</Tag>}>
          <p className="text-sm text-ink">Modal body.</p>
        </Modal>
      </div>
      <div className={box} style={pin}>
        <ConfirmationDialog
          open
          onConfirm={noop}
          onCancel={noop}
          title="Destroy prod?"
          description="Every workload in it stops."
          confirmLabel="Destroy"
          variant="danger"
        />
      </div>
    </div>
  );
}

export default function ForgeTokenSandbox() {
  const { view } = useSearch({ strict: false }) as { view?: string };
  const [scheme, setScheme] = useState<string>(
    () => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue",
  );
  const [dark, setDark] = useState<boolean>(
    () => document.documentElement.classList.contains("dark"),
  );

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  return (
    <div className="min-h-screen overflow-auto bg-background p-8">
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <label className="text-sm text-foreground" htmlFor="forge-scheme">
          Color scheme
        </label>
        <select
          id="forge-scheme"
          data-testid="scheme-select"
          value={scheme}
          onChange={(e) => applyScheme(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {SCHEMES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <button
          type="button"
          data-testid="toggle-dark"
          onClick={() => applyDark(!dark)}
          className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
        >
          {dark ? "Dark" : "Light"}
        </button>
      </div>

      {/* Everything forge renders lives under .forge-ui. */}
      <div className="forge-ui" data-testid="forge-scope">
        {view === "overlays" ? <Overlays /> : view?.startsWith("s2-") || view?.startsWith("r5-") ? null : <ForgeLibrary />}
      </div>

      {/* The S1 and S2 sites sit OUTSIDE the scope, as they do in the app: a
          site that needs forge's accent wraps itself in .forge-ui, and this
          is where a missing wrap would show. The S2 sites come last, so the
          regions above them keep the page offsets their baselines were
          captured at (a region moved by a fraction of a pixel re-rasterizes). */}
      {view === undefined && (
        <>
          <div className="mt-12">
            <S1Sites />
          </div>
          <div className="mt-12">
            <ButtonSites />
          </div>
          <div className="mt-12">
            <S2Sites />
          </div>
        </>
      )}
      {view === "s2-menus" && <S2Menus />}
      {view === "s2-modal" && <S2Modal />}
      {view === "r5-tooltips" && <R5Tooltips />}
      {view === "r5-menus" && <R5Menus />}
      {view === "r5-pickers" && <R5Pickers />}
    </div>
  );
}
