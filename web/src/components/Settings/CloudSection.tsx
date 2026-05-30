import {
  ExternalLink,
  LayoutDashboard,
  Server,
  Sparkles,
  CreditCard,
  Building2,
} from "lucide-react";
import { getAdminURL } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";
import type { SettingsSection } from "./SettingsNavigation";

interface CloudLink {
  id: SettingsSection;
  label: string;
  description: string;
  path: string;
  icon: React.ComponentType<{ className?: string }>;
}

const cloudLinks: CloudLink[] = [
  {
    id: "cloud-overview",
    label: "Overview",
    description:
      "Dashboard with AI access status, wallet balance, token health, and environment summary.",
    path: "/dashboard",
    icon: LayoutDashboard,
  },
  {
    id: "cloud-environments",
    label: "Environments",
    description:
      "Create, manage, and monitor cloud compute environments. Configure resources, volumes, and idle timeouts.",
    path: "/workspaces",
    icon: Server,
  },
  {
    id: "cloud-ai",
    label: "AI Management",
    description:
      "Manage model access, API credentials, usage tracking, and spend limits.",
    path: "/ai",
    icon: Sparkles,
  },
  {
    id: "cloud-billing",
    label: "Billing",
    description:
      "Wallet credits, compute plans, invoices, and subscription management.",
    path: "/billing",
    icon: CreditCard,
  },
  {
    id: "cloud-organization",
    label: "Organization",
    description:
      "Team members, invitations, and organization settings.",
    path: "/settings",
    icon: Building2,
  },
];

interface CloudSectionProps {
  section: SettingsSection;
}

export function CloudSection({ section }: CloudSectionProps) {
  const adminURL = getAdminURL();

  if (!adminURL) {
    return (
      <div className="space-y-4">
        <h2 className="text-lg font-semibold">Cloud</h2>
        <p className="text-sm text-muted-foreground">
          Cloud features are not configured. Set the admin URL to enable cloud management.
        </p>
      </div>
    );
  }

  // When a specific cloud section is selected, show all links as a landing page
  // with the selected one highlighted
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold mb-1">Cloud</h2>
        <p className="text-sm text-muted-foreground">
          Manage your cloud account, environments, and billing. These open in the Reliant admin portal.
        </p>
      </div>

      <div className="space-y-2">
        {cloudLinks.map((link) => {
          const isActive = link.id === section;
          return (
            <button
              key={link.id}
              onClick={() => {
                void openExternalLink(`${adminURL}${link.path}`);
              }}
              className={`w-full rounded-lg border p-4 text-left transition-colors hover:bg-muted/50 ${
                isActive
                  ? "border-primary/30 bg-primary/5"
                  : "border-border/40"
              }`}
            >
              <div className="flex items-start gap-3">
                <div className="mt-0.5 rounded-md bg-muted p-2">
                  <link.icon className="h-4 w-4 text-muted-foreground" />
                </div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{link.label}</span>
                    <ExternalLink className="h-3 w-3 text-muted-foreground/60" />
                  </div>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {link.description}
                  </p>
                </div>
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}
