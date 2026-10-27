import { useState } from "react";
import { ExternalLink, BookOpen, FileText, Shield, Globe, Terminal, Calendar, Github, Slack, WandSparkles } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "../../lib/toast-manager";
import { UpdateSection } from "./UpdateSection";
import { useTourStore } from "../../store/tourStore";
import { ONBOARDING_STEPS } from "../Onboarding/constants";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { BrandMark } from "../icons/BrandMark";
import { VersionDetails } from "./VersionDetails";

type LinkItem = {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  href?: string;
  onClick?: () => void;
};

export function AboutSection() {
  const [isInstallingCLI, setIsInstallingCLI] = useState(false);
  const isElectron = !!window.electronAPI;
  const navigate = useNavigate();

  const handleInstallCLI = async () => {
    if (!window.electronAPI?.installCLI) return;

    setIsInstallingCLI(true);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        toast.success("CLI installed! You can now use 'reliant' command in your terminal.");
      } else {
        toast.error(result.error || "Failed to install CLI");
      }
    } catch (error) {
      toast.error("Failed to install CLI. You may need to run with sudo.");
    } finally {
      setIsInstallingCLI(false);
    }
  };

  const links: LinkItem[] = [
    {
      icon: BookOpen,
      label: "Docs",
      href: "https://docs.reliantlabs.io/",
    },
    {
      icon: Globe,
      label: "Website",
      href: "https://reliantlabs.io",
    },
    {
      icon: Calendar,
      label: "Book a demo",
      href: "https://cal.com/team/reliant/onboarding",
    },
    {
      icon: Github,
      label: "GitHub",
      href: "https://github.com/reliant-labs/reliant",
    },
    {
      icon: Slack,
      label: "Join Slack",
      href: "https://join.slack.com/t/reliant-pn51441/shared_invite/zt-4c7wet6o2-W_Tksw80LaRGxZqUz9GQdA",
    },
    {
      icon: FileText,
      label: "Terms of Service",
      href: "https://reliantlabs.io/terms",
    },
    {
      icon: Shield,
      label: "Privacy Policy",
      href: "https://reliantlabs.io/privacy",
    },
    {
      icon: WandSparkles,
      label: "Restart Onboarding Guide",
      onClick: () => {
        // Drop the user on the home route with `?tour=<first-step>` — the
        // wizard reads the URL and takes it from there. The reset is
        // fire-and-forget (matches OnboardingChecklist.startTour); waiting on
        // the persistence RPC just delays the navigation and, when the RPC
        // is slow, makes the click look like a no-op.
        void useTourStore.getState().resetTourProgress();
        void navigate({
          to: "/",
          search: { tour: ONBOARDING_STEPS[0].id },
        });
        toast.success("Onboarding guide has been reset");
      },
    },
    // Only show CLI install option in Electron
    ...(isElectron ? [{
      icon: Terminal,
      label: isInstallingCLI ? "Installing..." : "Install CLI Command",
      onClick: handleInstallCLI,
    }] : []),
  ];

  return (
    <div className="space-y-4" data-onboarding="about-settings">
      <SettingsPageHeader
        title="About"
        description="Which version of Reliant you are running, whether an update is ready, and where to get help."
      />

      <div className="flex items-center gap-4 rounded-lg border border-border bg-card p-4">
        <BrandMark className="h-12 w-12 shrink-0" />
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-foreground">Reliant</h2>
          <VersionDetails />
        </div>
      </div>

        {/* Quick Links */}
        <div className="rounded-lg border border-border bg-card divide-y divide-border/60">
          {links.map((link) => {
            const content = (
              <>
                <span className="flex items-center gap-3">
                  <link.icon className="w-4 h-4 text-muted-foreground group-hover:text-primary transition-colors" />
                  {link.label}
                </span>
                {link.href && (
                  <ExternalLink className="w-3.5 h-3.5 text-muted-foreground/50 group-hover:text-muted-foreground transition-colors" />
                )}
              </>
            );

            if (link.onClick) {
              return (
                <a
                  key={link.label}
                  href="#"
                  onClick={(e) => {
                    e.preventDefault();
                    link.onClick?.();
                  }}
                  className="flex items-center justify-between px-4 py-3 text-sm text-foreground/80 hover:text-foreground hover:bg-foreground/5 transition-colors first:rounded-t-lg last:rounded-b-lg group"
                >
                  {content}
                </a>
              );
            }

            return (
              <a
                key={link.href}
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center justify-between px-4 py-3 text-sm text-foreground/80 hover:text-foreground hover:bg-foreground/5 transition-colors first:rounded-t-lg last:rounded-b-lg group"
              >
                {content}
              </a>
            );
          })}
        </div>

        {/* Update Section */}
        <UpdateSection />

        {/* Copyright */}
        <p className="text-xs text-muted-foreground">
          © {new Date().getFullYear()} Reliant Labs
        </p>
    </div>
  );
}